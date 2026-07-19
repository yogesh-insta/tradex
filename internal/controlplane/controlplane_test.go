package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/yogesh-insta/tradex/pkg/types"
)

func discard() *slog.Logger {
	return slog.New(slog.NewTextHandler(discardWriter{}, nil))
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

func TestStateMachineTransitions(t *testing.T) {
	tests := []struct {
		name string
		run  func(m *Machine) error
		from types.SystemState
		want types.SystemState
		ok   bool
	}{
		{"pause from active", func(m *Machine) error { return m.Pause() }, types.StateActive, types.StatePaused, true},
		{"pause idempotent", func(m *Machine) error { return m.Pause() }, types.StatePaused, types.StatePaused, true},
		{"pause refused when locked", func(m *Machine) error { return m.Pause() }, types.StateSystemLock, types.StateSystemLock, false},
		{"resume from paused", func(m *Machine) error { return m.Resume() }, types.StatePaused, types.StateActive, true},
		{"resume refused when locked", func(m *Machine) error { return m.Resume() }, types.StateSystemLock, types.StateSystemLock, false},
		{"resume refused when disabled", func(m *Machine) error { return m.Resume() }, types.StateDisabled, types.StateDisabled, false},
		{"force lock from active", func(m *Machine) error { m.ForceLock("breaker"); return nil }, types.StateActive, types.StateSystemLock, true},
		{"force lock from paused", func(m *Machine) error { m.ForceLock("breaker"); return nil }, types.StatePaused, types.StateSystemLock, true},
		{"force lock ignored when disabled", func(m *Machine) error { m.ForceLock("breaker"); return nil }, types.StateDisabled, types.StateDisabled, true},
		{"re-arm exits lock", func(m *Machine) error { _, _, err := m.ReArm(); return err }, types.StateSystemLock, types.StateActive, true},
		{"re-arm exits disabled", func(m *Machine) error { _, _, err := m.ReArm(); return err }, types.StateDisabled, types.StateActive, true},
		{"re-arm no-op when active", func(m *Machine) error { _, _, err := m.ReArm(); return err }, types.StateActive, types.StateActive, true},
		{"disable from anywhere", func(m *Machine) error { m.Disable("FLATTEN"); return nil }, types.StateSystemLock, types.StateDisabled, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := NewMachine(tt.from, discard(), nil)
			err := tt.run(m)
			if tt.ok && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tt.ok && err == nil {
				t.Fatal("expected error")
			}
			if got := m.State(); got != tt.want {
				t.Fatalf("state = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestReArmReportsChange(t *testing.T) {
	m := NewMachine(types.StateSystemLock, discard(), nil)
	_, changed, err := m.ReArm()
	if err != nil || !changed {
		t.Fatalf("changed=%v err=%v, want true,nil", changed, err)
	}
	_, changed, err = m.ReArm()
	if err != nil || changed {
		t.Fatalf("second re-arm changed=%v err=%v, want false,nil (idempotent)", changed, err)
	}
}

func TestAccountLocksDoNotChangeGlobalStateAndReArmClearsThem(t *testing.T) {
	m := NewMachine(types.StateActive, discard(), nil)
	m.ForceLockAccount("eu", "daily_loss_breaker")
	m.ForceLockAccount("fx", "consecutive_loss_breaker")
	if got := m.State(); got != types.StateActive {
		t.Fatalf("state = %s, want ACTIVE", got)
	}
	if locked, reason := m.AccountLock("eu"); !locked || reason != "daily_loss_breaker" {
		t.Fatalf("EU lock = %v, %q", locked, reason)
	}
	accounts, changed, err := m.ReArm()
	if err != nil || !changed || len(accounts) != 2 {
		t.Fatalf("ReArm = %#v, %v, %v; want both accounts and changed", accounts, changed, err)
	}
	if locked, _ := m.AccountLock("eu"); locked {
		t.Fatal("EU lock remains after RE_ARM")
	}
	if got := m.State(); got != types.StateActive {
		t.Fatalf("state = %s, want ACTIVE", got)
	}
}

func signedBody(t *testing.T, secret []byte, cmd Command) ([]byte, string) {
	t.Helper()
	raw, err := json.Marshal(cmd)
	if err != nil {
		t.Fatal(err)
	}
	return raw, Sign(secret, raw)
}

func newTestServer(t *testing.T, actions Actions) (*Server, []byte) {
	t.Helper()
	secret := []byte("test-secret")
	m := NewMachine(types.StateActive, discard(), nil)
	s := NewServer(m, actions, AuthConfig{
		Secret:   secret,
		MaxSkew:  60 * time.Second,
		NonceTTL: 300 * time.Second,
	}, discard())
	return s, secret
}

func TestHMACAuth(t *testing.T) {
	s, secret := newTestServer(t, Actions{})
	now := time.Now()

	t.Run("valid signature accepted", func(t *testing.T) {
		raw, sig := signedBody(t, secret, Command{Command: "STATUS", Nonce: "n1", TS: now.Unix()})
		if _, err := s.Authenticate(raw, sig); err != nil {
			t.Fatalf("expected accept, got %v", err)
		}
	})

	t.Run("wrong secret rejected", func(t *testing.T) {
		raw, _ := signedBody(t, secret, Command{Command: "STATUS", Nonce: "n2", TS: now.Unix()})
		badSig := Sign([]byte("wrong"), raw)
		if _, err := s.Authenticate(raw, badSig); err == nil {
			t.Fatal("expected reject")
		}
	})

	t.Run("tampered body rejected", func(t *testing.T) {
		raw, sig := signedBody(t, secret, Command{Command: "STATUS", Nonce: "n3", TS: now.Unix()})
		tampered := append([]byte{}, raw...)
		tampered[10] ^= 0xFF
		if _, err := s.Authenticate(tampered, sig); err == nil {
			t.Fatal("expected reject")
		}
	})

	t.Run("stale timestamp rejected", func(t *testing.T) {
		raw, sig := signedBody(t, secret, Command{Command: "STATUS", Nonce: "n4", TS: now.Add(-2 * time.Minute).Unix()})
		if _, err := s.Authenticate(raw, sig); err == nil {
			t.Fatal("expected reject (skew)")
		}
	})

	t.Run("nonce replay rejected", func(t *testing.T) {
		raw, sig := signedBody(t, secret, Command{Command: "STATUS", Nonce: "n5", TS: now.Unix()})
		if _, err := s.Authenticate(raw, sig); err != nil {
			t.Fatalf("first request should pass: %v", err)
		}
		if _, err := s.Authenticate(raw, sig); err == nil {
			t.Fatal("expected replay reject")
		}
	})

	t.Run("non-hex signature rejected", func(t *testing.T) {
		raw, _ := signedBody(t, secret, Command{Command: "STATUS", Nonce: "n6", TS: now.Unix()})
		if _, err := s.Authenticate(raw, "zzzz"); err == nil {
			t.Fatal("expected reject")
		}
	})
}

func TestExecuteCommands(t *testing.T) {
	t.Run("FLATTEN closes everything and disables", func(t *testing.T) {
		flattened := false
		s, _ := newTestServer(t, Actions{
			Flatten: func(context.Context) error { flattened = true; return nil },
		})
		if _, err := s.Execute(context.Background(), Command{Command: "FLATTEN"}); err != nil {
			t.Fatal(err)
		}
		if !flattened {
			t.Fatal("flatten action not invoked")
		}
		if s.machine.State() != types.StateDisabled {
			t.Fatalf("state = %s, want DISABLED", s.machine.State())
		}
	})

	t.Run("FLATTEN stays disabled on partial failure", func(t *testing.T) {
		s, _ := newTestServer(t, Actions{
			Flatten: func(context.Context) error { return fmt.Errorf("one close failed") },
		})
		if _, err := s.Execute(context.Background(), Command{Command: "FLATTEN"}); err == nil {
			t.Fatal("expected error surfaced")
		}
		if s.machine.State() != types.StateDisabled {
			t.Fatalf("state = %s, want DISABLED", s.machine.State())
		}
	})

	t.Run("RE_ARM snapshots baseline only when it unlocks", func(t *testing.T) {
		snapshots := 0
		s, _ := newTestServer(t, Actions{
			ReArm: func(_ context.Context, accounts []string) error {
				snapshots += len(accounts)
				return nil
			},
		})
		// Not locked: no-op, no snapshot.
		if _, err := s.Execute(context.Background(), Command{Command: "RE_ARM"}); err != nil {
			t.Fatal(err)
		}
		if snapshots != 0 {
			t.Fatal("no-op RE_ARM must not snapshot")
		}
		s.machine.ForceLockAccount("eu", "daily_loss_breaker")
		if _, err := s.Execute(context.Background(), Command{Command: "RE_ARM"}); err != nil {
			t.Fatal(err)
		}
		if snapshots != 1 {
			t.Fatalf("snapshots = %d, want 1", snapshots)
		}
		if s.machine.State() != types.StateActive {
			t.Fatalf("state = %s, want ACTIVE", s.machine.State())
		}
	})

	t.Run("unknown command errors", func(t *testing.T) {
		s, _ := newTestServer(t, Actions{})
		if _, err := s.Execute(context.Background(), Command{Command: "SELF_DESTRUCT"}); err == nil {
			t.Fatal("expected error")
		}
	})
}
