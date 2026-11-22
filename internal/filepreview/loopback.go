package filepreview

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// LoopbackServer serves preview documents to the system web browser strictly on
// 127.0.0.1 with an ephemeral port and per-instance random bearer path token.
// It is created lazily only when the user explicitly clicks "Open in browser".
type LoopbackServer struct {
	mu       sync.RWMutex
	sources  map[string]Source
	nextID   atomic.Uint64
	token    string
	addr     string
	server   *http.Server
	listener net.Listener
	closed   bool
}

// NewLoopbackServer binds 127.0.0.1:0 and begins serving registered documents.
func NewLoopbackServer() (*LoopbackServer, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return nil, fmt.Errorf("filepreview: token: %w", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("filepreview: listen: %w", err)
	}
	s := &LoopbackServer{
		sources:  make(map[string]Source),
		token:    hex.EncodeToString(raw[:]),
		addr:     listener.Addr().String(),
		listener: listener,
	}
	s.server = &http.Server{
		Handler:           http.HandlerFunc(s.serveHTTP),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		_ = s.server.Serve(listener)
	}()
	return s, nil
}

// Register binds a document Source and returns an id suitable for URL().
func (s *LoopbackServer) Register(source Source) string {
	id := strconv.FormatUint(s.nextID.Add(1), 10)
	s.mu.Lock()
	if !s.closed {
		s.sources[id] = source
	}
	s.mu.Unlock()
	return id
}

// Unregister removes a previously registered document Source.
func (s *LoopbackServer) Unregister(id string) {
	if s == nil || id == "" {
		return
	}
	s.mu.Lock()
	delete(s.sources, id)
	s.mu.Unlock()
}

// URL returns the authenticated loopback URL for the registered document id.
func (s *LoopbackServer) URL(id string, entryPath string) string {
	return "http://" + s.addr + "/t/" + s.token + "/doc/" + id + "/" + strings.TrimPrefix(entryPath, "/")
}

// Close shuts down the loopback server and drops all registered sources.
func (s *LoopbackServer) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	clear(s.sources)
	server := s.server
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = server.Shutdown(ctx)
}

func (s *LoopbackServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// Expected path: /t/<token>/doc/<id>/<subpath...>
	trimmed := strings.TrimPrefix(r.URL.Path, "/")
	parts := strings.SplitN(trimmed, "/", 5)
	if len(parts) < 5 || parts[0] != "t" || parts[2] != "doc" {
		http.NotFound(w, r)
		return
	}
	if subtle.ConstantTimeCompare([]byte(parts[1]), []byte(s.token)) != 1 {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	id, subPath := parts[3], parts[4]
	s.mu.RLock()
	source, ok := s.sources[id]
	s.mu.RUnlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	resp := Resolve(r.Context(), source, subPath)
	if resp.MIME != "" {
		w.Header().Set("Content-Type", resp.MIME)
	}
	w.Header().Set("Cache-Control", "no-store")
	for k, v := range resp.Headers {
		w.Header().Set(k, v)
	}
	status := resp.Status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	if r.Method != http.MethodHead && len(resp.Data) > 0 {
		_, _ = w.Write(resp.Data)
	}
}
