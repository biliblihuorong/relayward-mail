package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"relayward-mail/internal/store"
)

// The management page login: POST /api/login exchanges an admin token (plus
// a Turnstile verdict when the captcha is enabled) for a browser session
// cookie, so the raw token never has to live in the page's JavaScript.
// POST /api/logout drops the session, GET /api/session reports the current
// login state plus the public captcha settings. Bearer-token authentication
// on the other /api routes is unchanged for scripts and CLIs.

const (
	sessionCookieName = "rw_admin_session"
	// sessionSecretBytes is the entropy of the cookie value.
	sessionSecretBytes = 32
	// sessionTouchInterval throttles last_seen writes.
	sessionTouchInterval = time.Minute
)

// loginRequest is the POST /api/login body.
type loginRequest struct {
	Token     string `json:"token"`
	Turnstile string `json:"turnstile"`
}

// loginResponse is the POST /api/login reply.
type loginResponse struct {
	Role      string    `json:"role"`
	TokenName string    `json:"token_name"`
	ExpiresAt time.Time `json:"expires_at"`
}

// sessionResponse is the GET /api/session reply; the role and name fields
// are present only when authenticated.
type sessionResponse struct {
	Authenticated    bool   `json:"authenticated"`
	Role             string `json:"role,omitempty"`
	TokenName        string `json:"token_name,omitempty"`
	TurnstileSiteKey string `json:"turnstile_site_key"`
}

// handleLogin serves POST /api/login. Failed captchas and failed tokens feed
// the shared IP lockout, exactly like failed bearer authentication.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	ip := remoteIP(r)
	if s.lockout != nil && s.lockout.Banned(ip) {
		writeError(w, http.StatusTooManyRequests, codeIPBanned, "too many failed attempts, try again later")
		return
	}

	var req loginRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	req.Token = strings.TrimSpace(req.Token)
	if req.Token == "" {
		s.failedAuth(ip)
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing or invalid token")
		return
	}

	if s.turnstile != nil {
		if !s.captchaOK(w, r, ip, req.Turnstile) {
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	tok, err := s.store.GetAdminTokenByHash(ctx, store.HashToken(req.Token))
	if err != nil || (tok.ExpiresAt != nil && tok.ExpiresAt.Before(time.Now())) {
		s.failedAuth(ip)
		writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing or invalid token")
		return
	}

	raw, err := store.RandomToken("rws_", sessionSecretBytes)
	if err != nil {
		s.writeStoreError(w, err)
		return
	}
	expires := time.Now().UTC().Add(store.AdminSessionTTL)
	sess := &store.AdminSession{TokenHash: store.HashToken(raw), TokenID: tok.ID, ExpiresAt: expires}

	audit := &store.AuditEntry{TokenID: &tok.ID, TokenName: tok.Name, IP: ip, Action: store.ActionSessionCreate, Target: tok.Name}
	if err := s.store.CreateAdminSession(ctx, sess, audit); err != nil {
		s.writeStoreError(w, err)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    raw,
		Path:     "/",
		MaxAge:   int(time.Until(expires).Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
		Secure:   s.cookieSecure,
	})

	s.logger.Info("admin session created",
		slog.String("token_name", tok.Name), slog.String("role", tok.Role), slog.String("remote_ip", ip))
	writeJSON(w, http.StatusOK, loginResponse{Role: tok.Role, TokenName: tok.Name, ExpiresAt: expires})
}

// captchaOK verifies the widget token; when it returns false the response has
// already been written. A failed captcha counts toward the lockout: repeated
// failures from one IP are hostile regardless of which step stops them.
func (s *Server) captchaOK(w http.ResponseWriter, r *http.Request, ip, response string) bool {
	err := s.turnstile.verify(r.Context(), response, ip)
	switch {
	case err == nil:
		return true
	case errors.Is(err, errCaptchaFailed):
		s.failedAuth(ip)
		writeError(w, http.StatusUnauthorized, codeCaptchaFailed, "captcha verification failed")
	default:
		s.logger.Warn("turnstile siteverify",
			slog.String("remote_ip", ip), slog.String("err", err.Error()))
		writeError(w, http.StatusServiceUnavailable, codeCaptchaUnavailable, "captcha verification unavailable, try again later")
	}
	return false
}

// handleLogout serves POST /api/logout: drop the session server-side and
// clear the cookie. Idempotent; successful logins are audited but logouts
// are not (the audit log records who gained access, not who left).
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookieName); err == nil && c.Value != "" {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := s.store.DeleteAdminSession(ctx, store.HashToken(c.Value)); err != nil {
			s.logger.Warn("delete admin session", slog.String("err", err.Error()))
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: s.cookieSecure,
	})
	w.WriteHeader(http.StatusNoContent)
}

// handleSessionStatus serves GET /api/session. Unauthenticated, it reveals
// nothing but the public Turnstile site key.
func (s *Server) handleSessionStatus(w http.ResponseWriter, r *http.Request) {
	resp := sessionResponse{TurnstileSiteKey: s.turnstileSiteKey}
	if tok := s.requestToken(r); tok != nil {
		resp.Authenticated = true
		resp.Role = tok.Role
		resp.TokenName = tok.Name
	}
	writeJSON(w, http.StatusOK, resp)
}
