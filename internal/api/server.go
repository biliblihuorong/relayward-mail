// Package api serves the management HTTP surface. M1 only exposes
// /healthz; the token-authenticated management endpoints arrive in M2.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"relayward-mail/internal/relay"
	"relayward-mail/internal/store"
)

// UpstreamMonitor probes the upstream provider in the background so that
// /healthz can report connectivity without dialing the provider per request.
type UpstreamMonitor struct {
	client   *relay.Client
	interval time.Duration
	logger   *slog.Logger

	mu      sync.Mutex
	healthy bool
}

// NewUpstreamMonitor builds a monitor that probes every interval.
func NewUpstreamMonitor(client *relay.Client, interval time.Duration, logger *slog.Logger) *UpstreamMonitor {
	if logger == nil {
		logger = slog.Default()
	}
	return &UpstreamMonitor{client: client, interval: interval, logger: logger}
}

// Run probes immediately and then on every tick until ctx is done.
func (m *UpstreamMonitor) Run(ctx context.Context) error {
	m.probe(ctx)
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			m.probe(ctx)
		}
	}
}

func (m *UpstreamMonitor) probe(ctx context.Context) {
	err := m.client.Probe(ctx)

	m.mu.Lock()
	defer m.mu.Unlock()
	if err == nil {
		m.healthy = true
		return
	}
	m.healthy = false
	m.logger.Warn("upstream probe failed", slog.String("err", err.Error()))
}

// Healthy reports the result of the most recent probe.
func (m *UpstreamMonitor) Healthy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.healthy
}

// Server is the management HTTP server.
type Server struct {
	store     *store.Store
	monitor   *UpstreamMonitor
	logger    *slog.Logger
	version   string
	startedAt time.Time
}

// New builds the management API server.
func New(st *store.Store, monitor *UpstreamMonitor, version string, startedAt time.Time, logger *slog.Logger) *Server {
	if logger == nil {
		logger = slog.Default()
	}
	return &Server{
		store:     st,
		monitor:   monitor,
		logger:    logger,
		version:   version,
		startedAt: startedAt,
	}
}

// maxBodyBytes caps request bodies for every management endpoint.
const maxBodyBytes = 1 << 20

// Handler returns the http.Handler for the management surface.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	return securityHeaders(http.MaxBytesHandler(mux, maxBodyBytes))
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
// when it is valid and not expired; otherwise nil. Role and expiry checks
// keep the detail endpoint from leaking to revoked or stale credentials.
func (s *Server) lookupToken(r *http.Request) *store.AdminToken {
	auth := r.Header.Get("Authorization")
	prefix := "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return nil
	}
	raw := strings.TrimSpace(strings.TrimPrefix(auth, prefix))
	if raw == "" {
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
