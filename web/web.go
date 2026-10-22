package web

import (
	_ "embed"
	"net/http"
)

// IndexHTML contains the embedded web client.
//
//go:embed index.html
var IndexHTML []byte

// Handler serves the embedded single-page WebCodecs client.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(IndexHTML)
	})
}
