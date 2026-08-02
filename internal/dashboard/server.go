package dashboard

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"
)

// Server is the HTTPS UI + JSON API for the ops dashboard.
type Server struct {
	svc  *Service
	auth Authenticator
	log  *slog.Logger
}

// NewServer wires handlers.
func NewServer(svc *Service, auth Authenticator, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{svc: svc, auth: auth, log: log}
}

// Handler returns the root mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.handleHealth)
	mux.HandleFunc("GET /api/etf", s.protect(s.handleETF))
	mux.HandleFunc("GET /api/nse", s.protect(s.handleNSE))
	mux.HandleFunc("GET /api/ui-config", s.protect(s.handleUIConfig))
	// One document, three paths: the page reads location.pathname and renders
	// only that lane, so ETF and NSE are no longer stacked on one screen.
	mux.HandleFunc("GET /etf", s.protect(s.handleUI))
	mux.HandleFunc("GET /nse", s.protect(s.handleUI))
	mux.HandleFunc("GET /", s.protect(s.handleUI))
	return mux
}

// ListenAndServe runs until ctx is cancelled.
func (s *Server) ListenAndServe(ctx context.Context, addr string) error {
	srv := &http.Server{Addr: addr, Handler: s.Handler()}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (s *Server) protect(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := s.auth.Authorize(r); err != nil {
			s.log.Warn("dashboard auth rejected", "path", r.URL.Path, "remote", r.RemoteAddr, "error", err)
			writeAuthError(w, err)
			return
		}
		next(w, r)
	}
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"service": "dashboard",
		"ts":      time.Now().UTC().Format(time.RFC3339),
	})
}

func (s *Server) handleETF(w http.ResponseWriter, r *http.Request) {
	report, err := s.svc.LatestETFReport(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) handleNSE(w http.ResponseWriter, r *http.Request) {
	report, err := s.svc.LatestNSEReport(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, report)
}

func (s *Server) handleUIConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.UIConfig())
}

func (s *Server) handleUI(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(uiHTML))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(true)
	_ = enc.Encode(v)
}
