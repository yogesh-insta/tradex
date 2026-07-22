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
	mux.HandleFunc("GET /api/overview", s.protect(s.handleOverview))
	mux.HandleFunc("GET /api/calendar", s.protect(s.handleCalendar))
	mux.HandleFunc("GET /api/pl", s.protect(s.handlePL))
	mux.HandleFunc("GET /api/pl/daily", s.protect(s.handlePLDaily))
	mux.HandleFunc("GET /api/etf", s.protect(s.handleETF))
	mux.HandleFunc("GET /api/nse", s.protect(s.handleNSE))
	mux.HandleFunc("GET /api/ui-config", s.protect(s.handleUIConfig))
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

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.Overview(r.Context()))
}

func (s *Server) handleCalendar(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.svc.Calendar(r.Context()))
}

func (s *Server) handlePL(w http.ResponseWriter, r *http.Request) {
	account := r.URL.Query().Get("account")
	window := r.URL.Query().Get("window")
	if window == "" {
		window = "7d"
	}
	writeJSON(w, http.StatusOK, s.svc.PL(r.Context(), account, window))
}

func (s *Server) handlePLDaily(w http.ResponseWriter, r *http.Request) {
	account := r.URL.Query().Get("account")
	loc := s.svc.loc
	now := s.svc.now().In(loc)
	lookback := s.svc.cfg.UI.PLDailyLookbackDays
	if lookback <= 0 {
		lookback = 30
	}
	to := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	from := to.AddDate(0, 0, -(lookback - 1))
	if v := r.URL.Query().Get("from"); v != "" {
		t, err := time.ParseInLocation("2006-01-02", v, loc)
		if err != nil {
			http.Error(w, "invalid from (YYYY-MM-DD)", http.StatusBadRequest)
			return
		}
		from = t
	}
	if v := r.URL.Query().Get("to"); v != "" {
		t, err := time.ParseInLocation("2006-01-02", v, loc)
		if err != nil {
			http.Error(w, "invalid to (YYYY-MM-DD)", http.StatusBadRequest)
			return
		}
		to = t
	}
	writeJSON(w, http.StatusOK, s.svc.DailyPLSeries(r.Context(), account, from, to))
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
