package api

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/emersion/go-sasl"
	smtp "github.com/emersion/go-smtp"

	"relayward-mail/internal/relay"
	"relayward-mail/internal/store"
)

// openStore opens a throwaway store for tests.
func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "relayward.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// startFakeUpstream starts a local plaintext SMTP server that accepts
// everything, for upstream probes.
func startFakeUpstream(t *testing.T) string {
	t.Helper()
	backend := &probeBackend{}
	srv := smtp.NewServer(backend)
	srv.Domain = "upstream.test"
	srv.AllowInsecureAuth = true

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

type probeBackend struct{}

func (probeBackend) NewSession(*smtp.Conn) (smtp.Session, error) { return probeSession{}, nil }

type probeSession struct{}

func (probeSession) Reset()        {}
func (probeSession) Logout() error { return nil }
func (probeSession) AuthMechanisms() []string {
	return []string{sasl.Plain}
}
func (probeSession) Auth(_ string) (sasl.Server, error) {
	return sasl.NewPlainServer(func(_, _, _ string) error { return nil }), nil
}
func (probeSession) Mail(string, *smtp.MailOptions) error { return nil }
func (probeSession) Rcpt(string, *smtp.RcptOptions) error { return nil }
func (probeSession) Data(io.Reader) error                 { return nil }

// healthyMonitor probes a live fake upstream once.
func healthyMonitor(t *testing.T) *UpstreamMonitor {
	t.Helper()
	return monitorFor(t, newRelay(t, startFakeUpstream(t)))
}

// unhealthyMonitor probes a closed port once.
func unhealthyMonitor(t *testing.T) *UpstreamMonitor {
	t.Helper()
	return monitorFor(t, newRelay(t, ""))
}

func monitorFor(t *testing.T, cl *relay.Client) *UpstreamMonitor {
	t.Helper()
	m := NewUpstreamMonitor(cl, time.Minute, nil)
	m.probe(t.Context())
	return m
}

func newRelay(t *testing.T, addr string) *relay.Client {
	t.Helper()
	host, portStr := "127.0.0.1", "1"
	if addr != "" {
		host, portStr, _ = net.SplitHostPort(addr)
	}
	port, _ := strconv.Atoi(portStr)
	return relay.New(relay.Options{
		Host: host, Port: port,
		Username: "u", Password: "p",
		TLSMode: relay.TLSNone, HelloDomain: "relayward.test",
		Timeout: 5 * time.Second,
	})
}

func newTestServer(t *testing.T, st *store.Store, m *UpstreamMonitor) *httptest.Server {
	t.Helper()
	srv := New(st, m, "test-version", time.Now(), nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func getHealthz(t *testing.T, url string, token string) (int, healthResponse, http.Header) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	var body healthResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	return resp.StatusCode, body, resp.Header
}

func TestHealthzOK(t *testing.T) {
	st := openStore(t)
	ts := newTestServer(t, st, healthyMonitor(t))

	code, body, _ := getHealthz(t, ts.URL, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want 200", code)
	}
	if body.Status != "ok" {
		t.Errorf("status = %q, want ok", body.Status)
	}
	if body.Details != nil {
		t.Errorf("anonymous request must not carry details")
	}
}

func TestHealthzDegradedUpstream(t *testing.T) {
	st := openStore(t)
	ts := newTestServer(t, st, unhealthyMonitor(t))

	code, body, _ := getHealthz(t, ts.URL, "")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", code)
	}
	if body.Status != "degraded" {
		t.Errorf("status = %q, want degraded", body.Status)
	}
}

func TestHealthzDegradedDatabase(t *testing.T) {
	st := openStore(t)
	ts := newTestServer(t, st, healthyMonitor(t))
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	code, body, _ := getHealthz(t, ts.URL, "")
	if code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", code)
	}
	if body.Status != "degraded" {
		t.Errorf("status = %q, want degraded", body.Status)
	}
}

func TestHealthzDetailsWithToken(t *testing.T) {
	st := openStore(t)
	ts := newTestServer(t, st, healthyMonitor(t))

	raw := "rw_admin_testtoken"
	if err := st.CreateAdminToken(t.Context(), &store.AdminToken{
		Name: "test", Role: store.RoleAdmin, TokenHash: store.HashToken(raw),
	}); err != nil {
		t.Fatalf("create token: %v", err)
	}

	code, body, _ := getHealthz(t, ts.URL, raw)
	if code != http.StatusOK || body.Status != "ok" {
		t.Fatalf("status = %d / %q", code, body.Status)
	}
	if body.Details == nil {
		t.Fatal("details missing for valid token")
	}
	if body.Details.Version != "test-version" {
		t.Errorf("version = %q", body.Details.Version)
	}
	if body.Details.Database != "ok" || body.Details.Upstream != "ok" {
		t.Errorf("unexpected details: %+v", body.Details)
	}
	if body.Details.LastRelaySuccessAt != nil {
		t.Errorf("no relays yet, last_relay_success_at = %v", body.Details.LastRelaySuccessAt)
	}
	if body.Details.UptimeSeconds < 0 {
		t.Errorf("uptime = %d", body.Details.UptimeSeconds)
	}

	// The token's last_used gets updated by a detail request.
	tok, err := st.GetAdminTokenByHash(t.Context(), store.HashToken(raw))
	if err != nil {
		t.Fatal(err)
	}
	if tok.LastUsed == nil {
		t.Error("last_used not touched")
	}

	// Once a message has been relayed, details report its timestamp.
	if err := st.CreateApp(t.Context(), &store.App{
		Name: "gitea", PasswordHash: "not-used", AllowedFrom: []string{"a@b.com"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.InsertMessage(t.Context(), &store.Message{
		AppID: 1, MailFrom: "a@b.com", RcptTo: "x@y.com", Status: store.StatusSent,
	}); err != nil {
		t.Fatal(err)
	}
	_, body, _ = getHealthz(t, ts.URL, raw)
	if body.Details == nil || body.Details.LastRelaySuccessAt == nil {
		t.Fatalf("last_relay_success_at missing: %+v", body.Details)
	}
	if _, err := time.Parse(time.RFC3339, *body.Details.LastRelaySuccessAt); err != nil {
		t.Errorf("last_relay_success_at not RFC 3339: %v", err)
	}
}

func TestHealthzDetailsRejectedForBadTokens(t *testing.T) {
	st := openStore(t)
	ts := newTestServer(t, st, healthyMonitor(t))

	expired := time.Now().Add(-time.Hour)
	if err := st.CreateAdminToken(t.Context(), &store.AdminToken{
		Name: "expired", Role: store.RoleAdmin, TokenHash: store.HashToken("expired-token"), ExpiresAt: &expired,
	}); err != nil {
		t.Fatal(err)
	}

	tests := []string{"unknown-token", "expired-token", ""}
	for _, raw := range tests {
		_, body, _ := getHealthz(t, ts.URL, raw)
		if body.Details != nil {
			t.Errorf("token %q must not yield details", raw)
		}
	}
}

func TestSecurityHeaders(t *testing.T) {
	st := openStore(t)
	ts := newTestServer(t, st, healthyMonitor(t))

	_, _, headers := getHealthz(t, ts.URL, "")
	if got := headers.Get("Content-Security-Policy"); got == "" {
		t.Error("Content-Security-Policy missing")
	}
	if got := headers.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
	if got := headers.Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q", got)
	}
}

func TestUpstreamMonitorRunsAndStops(t *testing.T) {
	cl := newRelay(t, startFakeUpstream(t))
	m := NewUpstreamMonitor(cl, 10*time.Millisecond, nil)

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	if err := m.Run(ctx); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !m.Healthy() {
		t.Error("monitor should be healthy after probing a live upstream")
	}
}
