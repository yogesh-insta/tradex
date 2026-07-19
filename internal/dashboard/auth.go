package dashboard

import (
	"net/http"
	"strings"
)

// Authenticator gates / and /api/* (except /api/health).
type Authenticator interface {
	Authorize(r *http.Request) error
}

// BearerAuth accepts Authorization: Bearer <token>.
type BearerAuth struct {
	Token string
}

// Authorize implements Authenticator.
func (a BearerAuth) Authorize(r *http.Request) error {
	if a.Token == "" {
		return errAuthMisconfigured
	}
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if !strings.HasPrefix(h, prefix) || h[len(prefix):] != a.Token {
		// Also accept ?token= for simple browser bookmark demos (still secret).
		if r.URL.Query().Get("token") == a.Token {
			return nil
		}
		return errUnauthorized
	}
	return nil
}

// IAPAuth trusts Cloud IAP identity headers. Fail closed when the header is
// absent (misconfigured IAP → deny). Optional email allowlist.
type IAPAuth struct {
	AllowEmails map[string]struct{} // empty = any IAP identity
}

// Authorize implements Authenticator.
func (a IAPAuth) Authorize(r *http.Request) error {
	email := r.Header.Get("X-Goog-Authenticated-User-Email")
	if email == "" {
		// IAP sometimes prefixes accounts.google.com:
		email = r.Header.Get("X-Goog-IAP-JWT-Assertion")
		if email == "" {
			return errUnauthorized
		}
		// JWT present without email header: accept when no allowlist (Cloud Run
		// behind IAP). With an allowlist we require the email header.
		if len(a.AllowEmails) > 0 {
			return errUnauthorized
		}
		return nil
	}
	email = strings.TrimPrefix(email, "accounts.google.com:")
	if len(a.AllowEmails) == 0 {
		return nil
	}
	if _, ok := a.AllowEmails[strings.ToLower(email)]; !ok {
		return errForbidden
	}
	return nil
}

type authError struct {
	code int
	msg  string
}

func (e *authError) Error() string { return e.msg }

var (
	errUnauthorized      = &authError{code: http.StatusUnauthorized, msg: "unauthorized"}
	errForbidden         = &authError{code: http.StatusForbidden, msg: "forbidden"}
	errAuthMisconfigured = &authError{code: http.StatusForbidden, msg: "auth misconfigured"}
)

func writeAuthError(w http.ResponseWriter, err error) {
	if ae, ok := err.(*authError); ok {
		http.Error(w, ae.msg, ae.code)
		return
	}
	http.Error(w, "unauthorized", http.StatusUnauthorized)
}
