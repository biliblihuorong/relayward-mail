package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
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

// probeBackend accepts everything, for upstream probes and relays.
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

// startFakeUpstream starts a local plaintext SMTP server that accepts
// everything.
func startFakeUpstream(t *testing.T) string {
	t.Helper()
	srv := smtp.NewServer(probeBackend{})
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

// newRelay points a relay client at addr ("" = closed port).
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

// quietLogger keeps test output clean.
func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// startAPI boots the management API for a test; mutate adjusts the options
// before the server starts. Returns the store for seeding.
func startAPI(t *testing.T, mutate func(*Options)) (*store.Store, *httptest.Server) {
	t.Helper()
	st := openStore(t)
	opts := Options{
		Store:     st,
		Monitor:   healthyMonitor(t),
		Version:   "test-version",
		StartedAt: time.Now(),
		DataDir:   t.TempDir(),
		Logger:    quietLogger(),
	}
	if mutate != nil {
		mutate(&opts)
	}
	ts := httptest.NewServer(New(opts).Handler())
	t.Cleanup(ts.Close)
	return st, ts
}

// apiCall performs one request and returns status, decoded body and headers.
func apiCall(t *testing.T, ts *httptest.Server, method, path, token string, body any) (int, map[string]any, http.Header) {
	t.Helper()

	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, ts.URL+path, reader)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	decoded := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("decode body %q: %v", raw, err)
		}
	}
	return resp.StatusCode, decoded, resp.Header
}

// seedToken inserts a token and returns its plaintext.
func seedToken(t *testing.T, st *store.Store, name, role string) string {
	t.Helper()
	raw := "rw_test_" + name
	tok := &store.AdminToken{Name: name, Role: role, TokenHash: store.HashToken(raw)}
	if err := st.CreateAdminToken(t.Context(), tok, nil); err != nil {
		t.Fatalf("seed token %s: %v", name, err)
	}
	return raw
}

// seedApp inserts an app with a known password hash.
func seedApp(t *testing.T, st *store.Store, name string) *store.App {
	t.Helper()
	hash, err := store.HashPassword("pw-" + name)
	if err != nil {
		t.Fatal(err)
	}
	app := &store.App{
		Name:          name,
		PasswordHash:  hash,
		Enabled:       true,
		Unsubscribe:   true,
		BodyInjection: true,
		AllowedFrom:   []string{"NoReply@example.com"},
		RatePerHour:   500,
	}
	if err := st.CreateApp(t.Context(), app, nil); err != nil {
		t.Fatalf("seed app %s: %v", name, err)
	}
	return app
}
