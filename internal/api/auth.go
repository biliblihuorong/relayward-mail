package api

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"relayward-mail/internal/store"
)

const (
	bearerPrefix       = "Bearer "
	tokenTouchInterval = time.Minute // rewrite last_used at most once per interval
)

// contextKey carries the authenticated token through a request.
type contextKey int

const tokenContextKey contextKey = 0

// roleRank orders roles from least to most privileged.
func roleRank(role string) int {
	switch role {
	case store.RoleViewer:
		return 1
	case store.RoleOperator:
		return 2
	case store.RoleAdmin:
		return 3
	default:
		return 0
	}
}

// bearerFromRequest extracts the raw bearer token, if present.
func bearerFromRequest(r *http.Request) (string, bool) {
	auth := r.Header.Get("Authorization")
	if !strings.HasPrefix(auth, bearerPrefix) {
		return "", false
	}
	raw := strings.TrimSpace(strings.TrimPrefix(auth, bearerPrefix))
	if raw == "" {
		return "", false
	}
	return raw, true
}

// remoteIP returns the host part of the peer address. X-Forwarded-For is
// deliberately not trusted: it is trivially spoofable, and the plan exposes
// this API through a reverse proxy only when the operator opts in.
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// tokenFromContext returns the authenticated token of the request.
func tokenFromContext(ctx context.Context) *store.AdminToken {
	tok, _ := ctx.Value(tokenContextKey).(*store.AdminToken)
	return tok
}

// requireRole wraps a management handler with the allowlist, the failure
// lockout, bearer authentication and the minimum-role check. Every
// authentication failure is a uniform 401, per the plan.
func (s *Server) requireRole(minRole string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := remoteIP(r)

		if len(s.allowlist) > 0 {
			parsed := net.ParseIP(ip)
			if parsed == nil || !ipAllowed(s.allowlist, parsed) {
				writeError(w, http.StatusForbidden, codeIPForbidden, "client address not allowed")
				return
			}
		}

		if s.lockout != nil && s.lockout.Banned(ip) {
			writeError(w, http.StatusTooManyRequests, codeIPBanned, "too many failed attempts, try again later")
			return
		}

		raw, ok := bearerFromRequest(r)
		if !ok {
			s.failedAuth(ip)
			writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing or invalid token")
			return
		}

		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		tok, err := s.store.GetAdminTokenByHash(ctx, store.HashToken(raw))
		if err != nil || (tok.ExpiresAt != nil && tok.ExpiresAt.Before(time.Now())) {
			s.failedAuth(ip)
			writeError(w, http.StatusUnauthorized, codeUnauthorized, "missing or invalid token")
			return
		}

		if roleRank(tok.Role) < roleRank(minRole) {
			writeError(w, http.StatusForbidden, codeForbidden, "insufficient role for this operation")
			return
		}

		s.touchToken(ctx, tok)
		next(w, r.WithContext(context.WithValue(r.Context(), tokenContextKey, tok)))
	}
}

func (s *Server) failedAuth(ip string) {
	if s.lockout != nil {
		s.lockout.RecordFailure(ip)
	}
}

// touchToken updates last_used, throttled to one write per minute per token.
func (s *Server) touchToken(ctx context.Context, tok *store.AdminToken) {
	if tok.LastUsed != nil && time.Since(*tok.LastUsed) < tokenTouchInterval {
		return
	}
	if err := s.store.TouchAdminToken(ctx, tok.ID); err != nil {
		s.logger.Warn("touch token", slog.Int("token_id", int(tok.ID)), slog.String("err", err.Error()))
	}
}

// auditEntry builds the audit record for a management write performed by the
// request's token.
func (s *Server) auditEntry(r *http.Request, action, target, detail string) *store.AuditEntry {
	entry := &store.AuditEntry{IP: remoteIP(r), Action: action, Target: target, Detail: detail}
	if tok := tokenFromContext(r.Context()); tok != nil {
		entry.TokenID = &tok.ID
		entry.TokenName = tok.Name
	}
	return entry
}
