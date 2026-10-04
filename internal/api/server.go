// Package api serves the management HTTP surface: /healthz plus the
// token-authenticated management API introduced in M2.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"runtime/debug"
	"time"

	"relayward-mail/internal/ratelimit"
	"relayward-mail/internal/store"
)

// maxBodyBytes caps request bodies for every management endpoint.
const maxBodyBytes = 1 << 20

// requestTimeout bounds each management request.
const requestTimeout = 10 * time.Second

// Options configures the management API server. Limiter and Lockout may be
// nil (tests without throttles); IPAllowlist empty disables the filter.
// Turnstile keys non-empty enable the captcha on the page login; CookieSecure
// sets the Secure flag on the session cookie (set it when the admin port is
// exposed through an HTTPS reverse proxy).
type Options struct {
	Store       *store.Store
	Monitor     *UpstreamMonitor
	Limiter     *ratelimit.Limiter
	Lockout     *ratelimit.Lockout
	DataDir     string
	Version     string
	StartedAt   time.Time
	Logger      *slog.Logger
	IPAllowlist []string

	TurnstileSiteKey   string
	TurnstileSecretKey string
	CookieSecure       bool
}

// Server is the management HTTP server.
type Server struct {
	store     *store.Store
	monitor   *UpstreamMonitor
	limiter   *ratelimit.Limiter
	lockout   *ratelimit.Lockout
	allowlist []ipRule
	dataDir   string
	logger    *slog.Logger
	version   string
	startedAt time.Time

	turnstile        *turnstileVerifier
	turnstileSiteKey string
	cookieSecure     bool
}

// New builds the management API server.
func New(opts Options) *Server {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	s := &Server{
		store:        opts.Store,
		monitor:      opts.Monitor,
		limiter:      opts.Limiter,
		lockout:      opts.Lockout,
		allowlist:    parseIPAllowlist(opts.IPAllowlist),
		dataDir:      opts.DataDir,
		logger:       opts.Logger,
		version:      opts.Version,
		startedAt:    opts.StartedAt,
		cookieSecure: opts.CookieSecure,
	}
	if opts.TurnstileSiteKey != "" && opts.TurnstileSecretKey != "" {
		s.turnstile = newTurnstileVerifier(opts.TurnstileSecretKey)
		s.turnstileSiteKey = opts.TurnstileSiteKey
	}
	return s
}

// csp returns the response Content-Security-Policy. When the Turnstile
// captcha is enabled the Cloudflare challenge script and iframe must load
// from challenges.cloudflare.com; everything else stays same-origin.
func (s *Server) csp() string {
	if s.turnstile != nil {
		return "default-src 'self'; script-src 'self' https://challenges.cloudflare.com; " +
			"frame-src https://challenges.cloudflare.com; frame-ancestors 'none'"
	}
	return "default-src 'self'; frame-ancestors 'none'"
}

// Handler returns the http.Handler for the management surface.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /admin", s.handleAdmin)
	mux.HandleFunc("GET /admin/app.js", s.handleAdmin)
	mux.HandleFunc("GET /admin/style.css", s.handleAdmin)

	mux.HandleFunc("POST /api/login", s.handleLogin)
	mux.HandleFunc("POST /api/logout", s.handleLogout)
	mux.HandleFunc("GET /api/session", s.handleSessionStatus)

	mux.HandleFunc("GET /api/stats", s.requireRole(store.RoleViewer, s.handleStats))
	mux.HandleFunc("GET /api/messages", s.requireRole(store.RoleViewer, s.handleMessages))
	mux.HandleFunc("GET /api/apps", s.requireRole(store.RoleViewer, s.handleListApps))
	mux.HandleFunc("GET /api/apps/{name}", s.requireRole(store.RoleViewer, s.handleGetApp))
	mux.HandleFunc("POST /api/apps", s.requireRole(store.RoleOperator, s.handleCreateApp))
	mux.HandleFunc("PATCH /api/apps/{name}", s.requireRole(store.RoleOperator, s.handlePatchApp))
	mux.HandleFunc("POST /api/apps/{name}/rotate", s.requireRole(store.RoleOperator, s.handleRotateApp))
	mux.HandleFunc("DELETE /api/apps/{name}", s.requireRole(store.RoleOperator, s.handleDeleteApp))
	mux.HandleFunc("GET /api/unsubscribes", s.requireRole(store.RoleViewer, s.handleListUnsubscribes))
	mux.HandleFunc("POST /api/unsubscribes", s.requireRole(store.RoleOperator, s.handleCreateUnsubscribe))
	mux.HandleFunc("DELETE /api/unsubscribes/{id}", s.requireRole(store.RoleOperator, s.handleDeleteUnsubscribe))
	mux.HandleFunc("GET /api/tokens", s.requireRole(store.RoleAdmin, s.handleListTokens))
	mux.HandleFunc("POST /api/tokens", s.requireRole(store.RoleAdmin, s.handleCreateToken))
	mux.HandleFunc("DELETE /api/tokens/{id}", s.requireRole(store.RoleAdmin, s.handleRevokeToken))
	mux.HandleFunc("GET /api/audit", s.requireRole(store.RoleAdmin, s.handleAudit))

	return s.securityHeaders(s.recoverMiddleware(requestTimeoutMiddleware(http.MaxBytesHandler(mux, maxBodyBytes))))
}

// HealthzHandler returns the standalone /healthz handler so the public
// listener can serve the identical health endpoint without exposing any
// management route.
func (s *Server) HealthzHandler() http.HandlerFunc {
	return s.handleHealthz
}

// securityHeaders applies the response headers mandated by the plan to every
// HTTP surface; the CSP comes from s.csp (it widens when Turnstile is on).
func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", s.csp())
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

// recoverMiddleware logs a handler panic and answers a generic 500 without
// internal details.
func (s *Server) recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.logger.Error("panic in management handler",
					slog.Any("panic", rec),
					slog.String("path", r.URL.Path),
					slog.String("stack", string(debug.Stack())))
				writeError(w, http.StatusInternalServerError, codeInternal, "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// requestTimeoutMiddleware bounds every request with a context deadline.
func requestTimeoutMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// writeJSON writes a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		slog.Error("write json response", slog.String("err", err.Error()))
	}
}

// healthResponse is the /healthz payload.
type healthResponse struct {
	Status  string         `json:"status"`
	Details *healthDetails `json:"details,omitempty"`
}

// healthDetails is included when the request carries a valid viewer-or-above
// token.
type healthDetails struct {
	Database           string  `json:"database"`
	Upstream           string  `json:"upstream"`
	LastRelaySuccessAt *string `json:"last_relay_success_at"`
	Version            string  `json:"version"`
	UptimeSeconds      int64   `json:"uptime_seconds"`
}

// handleHealthz serves GET /healthz: 200 {"status":"ok"} when the database
// and the upstream are reachable, 503 {"status":"degraded"} otherwise.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	dbErr := s.store.Ping(ctx)
	upstreamOK := s.monitor.Healthy()

	resp := healthResponse{Status: "ok"}
	code := http.StatusOK
	if dbErr != nil || !upstreamOK {
		resp.Status = "degraded"
		code = http.StatusServiceUnavailable
	}

	if tok := s.lookupToken(r); tok != nil {
		database := "ok"
		if dbErr != nil {
			database = "unavailable"
		}
		upstream := "ok"
		if !upstreamOK {
			upstream = "unreachable"
		}
		details := &healthDetails{
			Database:      database,
			Upstream:      upstream,
			Version:       s.version,
			UptimeSeconds: int64(time.Since(s.startedAt).Seconds()),
		}
		if dbErr == nil {
			// Plan semantics: the timestamp of the latest message actually
			// relayed (status = sent), not the latest successful probe.
			if last, err := s.store.LastSuccessfulRelayTime(ctx); err == nil && last != nil {
				ts := last.Format(time.RFC3339)
				details.LastRelaySuccessAt = &ts
			}
		}
		resp.Details = details
	}

	writeJSON(w, code, resp)
}

// lookupToken resolves the request credential (bearer or session cookie) for
// /healthz details. Failures here do not feed the lockout: only /api
// authentication attempts do.
func (s *Server) lookupToken(r *http.Request) *store.AdminToken {
	return s.requestToken(r)
}
