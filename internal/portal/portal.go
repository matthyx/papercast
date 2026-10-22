package portal

import (
	"fmt"
	"log"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// Cursor modes defined by xdg-desktop-portal
const (
	CursorModeHidden   uint32 = 1
	CursorModeEmbedded uint32 = 2
	CursorModeMetadata uint32 = 4
)

// Session represents an active ScreenCast session
type Session struct {
	PWFile        *os.File
	NodeID        uint32
	SessionHandle dbus.ObjectPath
	conn          *dbus.Conn
	closed        bool
	mu            sync.Mutex
}

// OpenPipeWireRemote opens a new PipeWire remote file descriptor on the active session.
// This can be called repeatedly to revive GStreamer pipelines without prompting the user.
func (s *Session) OpenPipeWireRemote() (*os.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, fmt.Errorf("session is closed")
	}

	portal := s.conn.Object("org.freedesktop.portal.Desktop", "/org/freedesktop/portal/desktop")
	var fd dbus.UnixFD
	err := portal.Call("org.freedesktop.portal.ScreenCast.OpenPipeWireRemote", 0, s.SessionHandle, map[string]dbus.Variant{}).Store(&fd)
	if err != nil {
		return nil, fmt.Errorf("OpenPipeWireRemote failed: %w", err)
	}

	return os.NewFile(uintptr(fd), "pipewire"), nil
}

// IsAlive checks if the session D-Bus connection is still valid.
func (s *Session) IsAlive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.conn == nil || s.SessionHandle == "" {
		return false
	}
	dbusObj := s.conn.Object("org.freedesktop.DBus", "/org/freedesktop/DBus")
	var hasOwner bool
	err := dbusObj.Call("org.freedesktop.DBus.NameHasOwner", 0, "org.freedesktop.portal.Desktop").Store(&hasOwner)
	return err == nil && hasOwner
}

func (s *Session) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true

	if s.PWFile != nil {
		_ = s.PWFile.Close()
	}

	if s.conn != nil && s.SessionHandle != "" {
		sessionObj := s.conn.Object("org.freedesktop.portal.Desktop", s.SessionHandle)
		_ = sessionObj.Call("org.freedesktop.portal.Session.Close", 0).Err
		log.Printf("[portal] ScreenCast session closed: %s", s.SessionHandle)
	}
	if s.conn != nil {
		_ = s.conn.Close()
	}
	return nil
}

func getRestoreTokenPath() string {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(configDir, "papercast", "restore_token")
}

func loadRestoreToken() string {
	p := getRestoreTokenPath()
	if p == "" {
		return ""
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func saveRestoreToken(token string) {
	p := getRestoreTokenPath()
	if p == "" || token == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(p), 0755)
	_ = os.WriteFile(p, []byte(token), 0600)
}

// RequestScreenCast performs the D-Bus handshake with xdg-desktop-portal.
// It prompts the user via the desktop compositor to select the display to share.
func RequestScreenCast(cursorMode uint32, timeout time.Duration) (*Session, error) {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to D-Bus session bus: %w", err)
	}

	portal := conn.Object("org.freedesktop.portal.Desktop", "/org/freedesktop/portal/desktop")

	// Subscribe to Request signals before making calls to avoid race conditions
	rule := "type='signal',interface='org.freedesktop.portal.Request',member='Response'"
	call := conn.BusObject().Call("org.freedesktop.DBus.AddMatch", 0, rule)
	if call.Err != nil {
		conn.Close()
		return nil, fmt.Errorf("failed to add D-Bus match rule: %w", call.Err)
	}

	sigChan := make(chan *dbus.Signal, 20)
	conn.Signal(sigChan)

	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	token := fmt.Sprintf("pc_%d", r.Intn(900000)+100000)
	sessionToken := fmt.Sprintf("s_%s", token)

	waitForResponse := func(expectedPath dbus.ObjectPath) (uint32, map[string]dbus.Variant, error) {
		deadline := time.After(timeout)
		for {
			select {
			case sig := <-sigChan:
				if sig.Path == expectedPath && len(sig.Body) >= 2 {
					resp, ok1 := sig.Body[0].(uint32)
					res, ok2 := sig.Body[1].(map[string]dbus.Variant)
					if ok1 && ok2 {
						return resp, res, nil
					}
				}
			case <-deadline:
				return 0, nil, fmt.Errorf("timeout waiting for desktop portal response on %s", expectedPath)
			}
		}
	}

	// 1. CreateSession
	var reqPath dbus.ObjectPath
	err = portal.Call("org.freedesktop.portal.ScreenCast.CreateSession", 0, map[string]dbus.Variant{
		"session_handle_token": dbus.MakeVariant(sessionToken),
		"handle_token":         dbus.MakeVariant(token),
	}).Store(&reqPath)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("CreateSession call failed: %w", err)
	}

	resp, res, err := waitForResponse(reqPath)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if resp != 0 {
		conn.Close()
		return nil, fmt.Errorf("CreateSession was cancelled or rejected by desktop (code %d)", resp)
	}

	sessionHandleStr, ok := res["session_handle"].Value().(string)
	if !ok {
		conn.Close()
		return nil, fmt.Errorf("missing session_handle in CreateSession response")
	}
	sessionHandle := dbus.ObjectPath(sessionHandleStr)
	log.Printf("[portal] ScreenCast session created: %s", sessionHandle)

	// 2. SelectSources
	token2 := fmt.Sprintf("t_%d", r.Intn(900000)+100000)
	var selectReqPath dbus.ObjectPath
	restoreToken := loadRestoreToken()

	selectOpts := map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(token2),
		"types":        dbus.MakeVariant(uint32(1)), // Monitor
		"multiple":     dbus.MakeVariant(false),
		"cursor_mode":  dbus.MakeVariant(cursorMode),
		"persist_mode": dbus.MakeVariant(uint32(2)), // Persist until explicitly revoked
	}
	if restoreToken != "" {
		selectOpts["restore_token"] = dbus.MakeVariant(restoreToken)
		log.Printf("[portal] Attempting headless screen share with saved restore token...")
	}

	err = portal.Call("org.freedesktop.portal.ScreenCast.SelectSources", 0, sessionHandle, selectOpts).Store(&selectReqPath)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("SelectSources call failed: %w", err)
	}

	resp, _, err = waitForResponse(selectReqPath)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if resp != 0 {
		conn.Close()
		return nil, fmt.Errorf("SelectSources cancelled by user (code %d)", resp)
	}

	// 3. Start
	token3 := fmt.Sprintf("t_%d", r.Intn(900000)+100000)
	var startReqPath dbus.ObjectPath
	err = portal.Call("org.freedesktop.portal.ScreenCast.Start", 0, sessionHandle, "", map[string]dbus.Variant{
		"handle_token": dbus.MakeVariant(token3),
	}).Store(&startReqPath)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("Start call failed: %w", err)
	}

	if restoreToken == "" {
		log.Printf("[portal] Prompting desktop for screen share authorization...")
	} else {
		log.Printf("[portal] Restoring screen share session (no prompt needed)...")
	}

	resp, startRes, err := waitForResponse(startReqPath)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if resp != 0 {
		if restoreToken != "" {
			log.Printf("[portal] Saved restore token expired or invalid, clearing...")
			_ = os.Remove(getRestoreTokenPath())
		}
		conn.Close()
		return nil, fmt.Errorf("ScreenCast authorization denied by user (code %d)", resp)
	}

	// Save new restore token if issued by compositor
	if tokenVal, ok := startRes["restore_token"]; ok {
		if tokenStr, ok := tokenVal.Value().(string); ok && tokenStr != "" {
			saveRestoreToken(tokenStr)
			log.Printf("[portal] Successfully saved restore token (headless capture ready for next time)")
		}
	}

	// Parse stream node ID
	streamsVal, exists := startRes["streams"]
	if !exists {
		conn.Close()
		return nil, fmt.Errorf("portal response did not contain streams")
	}

	var nodeID uint32
	switch streams := streamsVal.Value().(type) {
	case [][]interface{}:
		if len(streams) > 0 && len(streams[0]) > 0 {
			if id, ok := streams[0][0].(uint32); ok {
				nodeID = id
			}
		}
	default:
		// Attempt fallback parsing if type differs
		log.Printf("[portal] Unexpected streams format: %T: %+v", streams, streams)
	}

	if nodeID == 0 {
		conn.Close()
		return nil, fmt.Errorf("failed to extract PipeWire stream node_id from portal response")
	}
	log.Printf("[portal] PipeWire stream ready (node_id=%d)", nodeID)

	sess := &Session{
		NodeID:        nodeID,
		SessionHandle: sessionHandle,
		conn:          conn,
	}

	// Initial PipeWire remote FD
	pwFile, err := sess.OpenPipeWireRemote()
	if err != nil {
		sess.Close()
		return nil, err
	}
	sess.PWFile = pwFile

	return sess, nil
}
