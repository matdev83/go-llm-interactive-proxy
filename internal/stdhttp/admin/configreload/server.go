package configreload

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/matdev83/go-llm-interactive-proxy/internal/core/safety"
)

// Server is the process-owned management HTTP listener (req 12.1-12.2).
// Full host-wide shutdown ordering with the data plane is owned by task 5.6;
// this type still owns its own listen/serve/shutdown lifecycle.
type Server struct {
	opts    Options
	handler *Handler

	mu         sync.Mutex
	httpServer *http.Server
	listener   net.Listener
	serveErr   chan error
	started    bool
}

// New constructs a validated management server around coord.
func New(opts Options, coord ReloadCoordinator) (*Server, error) {
	h, err := NewHandler(opts, coord)
	if err != nil {
		return nil, err
	}
	return &Server{opts: h.opts, handler: h}, nil
}

// Handler returns the management HTTP handler (for httptest without Listen).
func (s *Server) Handler() http.Handler {
	if s == nil || s.handler == nil {
		return http.NotFoundHandler()
	}
	return s.handler.Mux()
}

// Addr returns the bound address after Start, or the configured address before.
func (s *Server) Addr() string {
	if s == nil {
		return ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.listener != nil {
		return s.listener.Addr().String()
	}
	return s.opts.Address
}

// Start binds the startup-fixed listener and serves in a background worker.
func (s *Server) Start(ctx context.Context) error {
	if s == nil {
		return errors.New("configreload management: nil server")
	}
	if ctx == nil {
		return errors.New("configreload management: nil context")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return errors.New("configreload management: already started")
	}
	ln, err := net.Listen("tcp", s.opts.Address)
	if err != nil {
		return fmt.Errorf("configreload management: listen %s: %w", s.opts.Address, err)
	}
	srv := &http.Server{
		Handler:           s.handler.Mux(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	s.listener = ln
	s.httpServer = srv
	s.serveErr = make(chan error, 1)
	s.started = true
	go func() {
		err := func() (err error) {
			defer func() {
				if p := recover(); p != nil {
					err = safety.Capture(safety.BoundaryWorker, "management_listen_and_serve", p)
				}
			}()
			return srv.Serve(ln)
		}()
		s.serveErr <- err
	}()
	return nil
}

// Shutdown stops the management listener with a bounded drain.
func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	srv := s.httpServer
	started := s.started
	timeout := s.opts.ShutdownTimeout
	s.mu.Unlock()
	if !started || srv == nil {
		return nil
	}
	if timeout <= 0 {
		timeout = DefaultShutdownTimeout
	}
	baseCtx := ctx
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	shutdownCtx, cancel := context.WithTimeout(baseCtx, timeout)
	defer cancel()

	err := srv.Shutdown(shutdownCtx)
	s.mu.Lock()
	ch := s.serveErr
	s.started = false
	s.mu.Unlock()
	if ch != nil {
		select {
		case serveErr := <-ch:
			if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
				err = errors.Join(err, serveErr)
			}
		case <-shutdownCtx.Done():
			err = errors.Join(err, shutdownCtx.Err())
		}
	}
	return err
}
