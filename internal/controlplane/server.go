package controlplane

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// Command payload: {"command": "...", "args": {...}, "nonce": "...", "ts": <unix>}.
type Command struct {
	Command string            `json:"command"`
	Args    map[string]string `json:"args"`
	Nonce   string            `json:"nonce"`
	TS      int64             `json:"ts"`
}

// Actions are the side effects the server drives; the machine handles state.
type Actions struct {
	// Flatten cancels all resting orders and market-closes all open trades.
	Flatten func(ctx context.Context) error
	// ReArm resets the supplied breaker-locked accounts and snapshots their
	// new baseline equity. A nil/empty list represents a legacy global re-arm.
	ReArm func(ctx context.Context, accounts []string) error
	// Status returns the read-only status document.
	Status func(ctx context.Context) any
}

// AuthConfig for the HMAC check.
type AuthConfig struct {
	Secret   []byte
	MaxSkew  time.Duration
	NonceTTL time.Duration
}

// Server is the inbound command webhook.
type Server struct {
	machine *Machine
	actions Actions
	auth    AuthConfig
	log     *slog.Logger
	now     func() time.Time

	nonceMu sync.Mutex
	nonces  map[string]time.Time
}

// NewServer wires the webhook to the machine and its actions.
func NewServer(machine *Machine, actions Actions, auth AuthConfig, log *slog.Logger) *Server {
	return &Server{
		machine: machine,
		actions: actions,
		auth:    auth,
		log:     log,
		now:     time.Now,
		nonces:  map[string]time.Time{},
	}
}

// Handler returns the HTTP handler (POST /command).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /command", s.handleCommand)
	return mux
}

// ListenAndServe runs the webhook until ctx is cancelled. With cert/key paths
// TLS terminates here; empty paths serve plain HTTP (front with TLS).
func (s *Server) ListenAndServe(ctx context.Context, listen, certFile, keyFile string) error {
	srv := &http.Server{Addr: listen, Handler: s.Handler()}
	errCh := make(chan error, 1)
	go func() {
		if certFile != "" && keyFile != "" {
			errCh <- srv.ListenAndServeTLS(certFile, keyFile)
		} else {
			errCh <- srv.ListenAndServe()
		}
	}()
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

func (s *Server) handleCommand(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<16))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	cmd, authErr := s.Authenticate(body, r.Header.Get("X-Signature"))
	if authErr != nil {
		s.log.Warn("control command rejected", "error", authErr, "remote", r.RemoteAddr)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	result, err := s.Execute(r.Context(), cmd)
	resp := map[string]any{"state": s.machine.State(), "result": result}
	if err != nil {
		resp["error"] = err.Error()
		w.WriteHeader(http.StatusConflict)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// Authenticate verifies the HMAC signature, timestamp skew, and nonce replay
// cache. Returns the parsed command on success.
func (s *Server) Authenticate(rawBody []byte, signature string) (Command, error) {
	var zero Command
	mac := hmac.New(sha256.New, s.auth.Secret)
	mac.Write(rawBody)
	expected := hex.EncodeToString(mac.Sum(nil))
	sigBytes, err := hex.DecodeString(signature)
	if err != nil || !hmac.Equal(sigBytes, mac.Sum(nil)) {
		_ = expected
		return zero, errors.New("invalid signature")
	}
	var cmd Command
	if err := json.Unmarshal(rawBody, &cmd); err != nil {
		return zero, errors.New("malformed payload")
	}
	now := s.now()
	skew := now.Sub(time.Unix(cmd.TS, 0))
	if skew < -s.auth.MaxSkew || skew > s.auth.MaxSkew {
		return zero, errors.New("timestamp skew exceeded")
	}
	if cmd.Nonce == "" {
		return zero, errors.New("missing nonce")
	}
	s.nonceMu.Lock()
	defer s.nonceMu.Unlock()
	for n, seen := range s.nonces {
		if now.Sub(seen) > s.auth.NonceTTL {
			delete(s.nonces, n)
		}
	}
	if _, seen := s.nonces[cmd.Nonce]; seen {
		return zero, errors.New("nonce replay")
	}
	s.nonces[cmd.Nonce] = now
	return cmd, nil
}

// Execute runs an authenticated command against the machine + actions.
func (s *Server) Execute(ctx context.Context, cmd Command) (string, error) {
	switch cmd.Command {
	case "FLATTEN":
		// Block everything first, then unwind. Stay DISABLED even on partial
		// failure — the operator retries FLATTEN until flat.
		s.machine.Disable("FLATTEN command")
		if s.actions.Flatten != nil {
			if err := s.actions.Flatten(ctx); err != nil {
				return "flatten partial; retry FLATTEN", err
			}
		}
		return "flattened", nil
	case "PAUSE":
		if err := s.machine.Pause(); err != nil {
			return "", err
		}
		return "paused", nil
	case "RESUME":
		if err := s.machine.Resume(); err != nil {
			return "", err
		}
		return "resumed", nil
	case "RE_ARM":
		accounts, changed, err := s.machine.ReArm()
		if err != nil {
			return "", err
		}
		if !changed {
			return "no-op: not locked", nil
		}
		if s.actions.ReArm != nil {
			if err := s.actions.ReArm(ctx, accounts); err != nil {
				return "re-armed but baseline snapshot failed", err
			}
		}
		if len(accounts) == 0 {
			return "re-armed: global lock cleared", nil
		}
		return "re-armed accounts: new baseline equity snapshotted", nil
	case "STATUS":
		if s.actions.Status != nil {
			doc, _ := json.Marshal(s.actions.Status(ctx))
			return string(doc), nil
		}
		return string(s.machine.State()), nil
	default:
		return "", errors.New("unknown command " + cmd.Command)
	}
}

// Sign computes the request signature for a payload (client helper + tests).
func Sign(secret, rawBody []byte) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write(rawBody)
	return hex.EncodeToString(mac.Sum(nil))
}
