// Package api serves the management HTTP surface: /healthz plus the
// token-authenticated management API introduced in M2.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
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
}

// New builds the management API server.
func New(opts Options) *Server {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Server{
		store:     opts.Store,
		monitor:   opts.Monitor,
		limiter:   opts.Limiter,
		lockout:   opts.Lockout,
		allowlist: parseIPAllowlist(opts.IPAllowlist),
		dataDir:   opts.DataDir,
		logger:    opts.Logger,
		version:   opts.Version,
		startedAt: opts.StartedAt,
	}
}

// Handler returns the http.Handler for the management surface.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)

	mux.HandleFunc("GET /api/stats", s.requireRole(store.RoleViewer, s.handleStats))
	mux.HandleFunc("GET /api/messages", s.requireRole(store.RoleViewer, s.handleMessages))
	mux.HandleFunc("GET /api/apps", s.requireRole(store.RoleViewer, s.handleListApps))
	mux.HandleFunc("GET /api/apps/{name}", s.requireRole(store.RoleViewer, s.handleGetApp))
	mux.HandleFunc("POST /api/apps", s.requireRole(store.RoleOperator, s.handleCreateApp))
	mux.HandleFunc("PATCH /api/apps/{name}", s.requireRole(store.RoleOperator, s.handlePatchApp))
	mux.HandleFunc("POST /api/apps/{name}/rotate", s.requireRole(store.RoleOperator, s.handleRotateApp))
	mux.HandleFunc("DELETE /api/apps/{name}", s.requireRole(store.RoleOperator, s.handleDeleteApp))
	mux.HandleFunc("GET /api/tokens", s.requireRole(store.RoleAdmin, s.handleListTokens))
	mux.HandleFunc("POST /api/tokens", s.requireRole(store.RoleAdmin, s.handleCreateToken))
	mux.HandleFunc("DELETE /api/tokens/{id}", s.requireRole(store.RoleAdmin, s.handleRevokeToken))
	mux.HandleFunc("GET /api/audit", s.requireRole(store.RoleAdmin, s.handleAudit))

	return securityHeaders(requestTimeoutMiddleware(http.MaxBytesHandler(mux, maxBodyBytes)))
}

// securityHeaders applies the response headers mandated by the plan to every
// HTTP surface.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
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

// lookupToken validates the Bearer token (if any) and returns the token row
// when it is valid and not expired; otherwise nil. Failures here do not feed
// the lockout: only /api authentication attempts do.
func (s *Server) lookupToken(r *http.Request) *store.AdminToken {
	raw, ok := bearerFromRequest(r)
	if !ok {
		return nil
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	tok, err := s.store.GetAdminTokenByHash(ctx, store.HashToken(raw))
	if err != nil {
		return nil
	}
	if tok.ExpiresAt != nil && tok.ExpiresAt.Before(time.Now()) {
		return nil
	}

	if err := s.store.TouchAdminToken(ctx, tok.ID); err != nil {
		s.logger.Warn("touch token", slog.Int("token_id", int(tok.ID)), slog.String("err", err.Error()))
	}
	return tok
}
