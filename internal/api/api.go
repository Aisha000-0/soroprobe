// Package api serves an HTTP mirror of the SoroProbe CLI, intended for CI
// systems and other services.
//
// Like the CLI, the API is strictly read-only: it simulates and inspects, and
// offers no route that submits a transaction.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/soroworks/soroprobe/internal/probe"
)

// Server wraps the HTTP API.
type Server struct {
	prober *probe.Prober
	addr   string
	log    *slog.Logger
	router chi.Router
}

// Options configures a Server.
type Options struct {
	// Prober runs the probes. Required.
	Prober *probe.Prober
	// Addr is the listen address. Defaults to ":8080".
	Addr string
	// Logger receives request logs. Defaults to a discarding logger.
	Logger *slog.Logger
	// Timeout bounds a single request. Defaults to 60s.
	Timeout time.Duration
}

// New builds a Server with its routes mounted.
func New(opts Options) *Server {
	if opts.Addr == "" {
		opts.Addr = ":8080"
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Timeout <= 0 {
		opts.Timeout = 60 * time.Second
	}

	s := &Server{prober: opts.Prober, addr: opts.Addr, log: opts.Logger}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(opts.Timeout))
	r.Use(nosniff)
	r.Use(s.logRequests)

	r.Get("/healthz", s.handleLiveness)
	r.Route("/v1", func(r chi.Router) {
		// Answers reflect live on-chain state, so they must not be replayed
		// from a shared or intermediary cache.
		r.Use(noStore)
		r.Post("/simulate", s.handleSimulate)
		r.Get("/inspect/{contract}", s.handleInspect)
		r.Get("/check/{contract}", s.handleCheck)
		r.Post("/checks", s.handleChecks)
	})

	s.router = r
	return s
}

// Handler exposes the router, which makes the API testable with httptest
// without binding a port.
func (s *Server) Handler() http.Handler { return s.router }

// ListenAndServe runs the server until ctx is cancelled, then shuts it down
// gracefully.
func (s *Server) ListenAndServe(ctx context.Context) error {
	srv := &http.Server{
		Addr:              s.addr,
		Handler:           s.router,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		s.log.Info("http api listening", "addr", s.addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		s.log.Info("shutting down http api")
		return srv.Shutdown(shutdownCtx)
	}
}

// nosniff stops a browser from reinterpreting a response as a type it did not
// ask for. The API only serves JSON, so a response rendered directly as HTML
// would be a sniffing bug, not a feature.
func nosniff(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(w, r)
	})
}

// noStore marks a response as uncacheable. The /v1 answers mirror live chain
// state and a health result is only meaningful at the moment it was asked for,
// so an intermediary must not serve a stale copy.
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		s.log.Debug("request",
			"method", r.Method,
			"path", r.URL.Path,
			"took", time.Since(start),
			"request_id", middleware.GetReqID(r.Context()),
		)
	})
}

// errorResponse is the body returned for any non-2xx response.
type errorResponse struct {
	Error string `json:"error"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, errorResponse{Error: err.Error()})
}
