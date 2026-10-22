package server

import (
	"context"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/matthyx/papercast/web"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		return true // Allow all origins for local casting
	},
	ReadBufferSize:  1024,
	WriteBufferSize: 64 * 1024,
}

type client struct {
	ws          *websocket.Conn
	ch          chan []byte
	droppingGOP bool
}

type Server struct {
	addr       string
	httpServer *http.Server
	clients    map[*client]struct{}
	mu         sync.RWMutex
}

func NewServer(addr string) *Server {
	s := &Server{
		addr:    addr,
		clients: make(map[*client]struct{}),
	}

	mux := http.NewServeMux()
	mux.Handle("/", web.Handler())
	mux.HandleFunc("/ws", s.handleWS)

	s.httpServer = &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	return s
}

func (s *Server) Start() error {
	log.Printf("[server] HTTP and WebSocket listening on http://%s", s.addr)
	err := s.httpServer.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	for c := range s.clients {
		close(c.ch)
		_ = c.ws.Close()
	}
	s.clients = make(map[*client]struct{})
	s.mu.Unlock()

	return s.httpServer.Shutdown(ctx)
}

func (s *Server) BroadcastFrame(frame []byte, isKey bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if len(s.clients) == 0 {
		return
	}

	for c := range s.clients {
		if c.droppingGOP {
			if !isKey {
				// Continue dropping delta frames until a fresh keyframe arrives
				continue
			}
			// Keyframe arrived! Safely resume streaming without missing references
			c.droppingGOP = false
		}

		select {
		case c.ch <- frame:
		default:
			// Buffer full (30 frames waiting in transit). Enter GOP-dropping mode:
			// Drop this and following delta frames until the next keyframe arrives.
			c.droppingGOP = true
			if isKey {
				// If this frame is a keyframe, drain queue and send it
				for len(c.ch) > 0 {
					select {
					case <-c.ch:
					default:
					}
				}
				select {
				case c.ch <- frame:
					c.droppingGOP = false
				default:
				}
			}
		}
	}
}

func (s *Server) ClientCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.clients)
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[server] WebSocket upgrade error from %s: %v", r.RemoteAddr, err)
		return
	}

	c := &client{
		ws: ws,
		ch: make(chan []byte, 30), // 30-frame buffer (absorbs YouTube spikes with zero drops)
	}

	s.mu.Lock()
	s.clients[c] = struct{}{}
	clientCount := len(s.clients)
	s.mu.Unlock()

	log.Printf("[server] Client connected from %s (Active clients: %d)", r.RemoteAddr, clientCount)

	// Sender goroutine
	go func() {
		defer ws.Close()
		for frame := range c.ch {
			_ = ws.SetWriteDeadline(time.Now().Add(2 * time.Second))
			if err := ws.WriteMessage(websocket.BinaryMessage, frame); err != nil {
				break
			}
		}
	}()

	// Reader loop (monitors disconnection and reads input events)
	for {
		_, _, err := ws.ReadMessage()
		if err != nil {
			break
		}
	}

	s.mu.Lock()
	delete(s.clients, c)
	close(c.ch)
	remaining := len(s.clients)
	s.mu.Unlock()

	log.Printf("[server] Client disconnected from %s (Remaining: %d)", r.RemoteAddr, remaining)
}
