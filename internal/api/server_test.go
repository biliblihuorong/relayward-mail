package api

import (
	"net/http"
	"testing"
	"time"

	"relayward-mail/internal/store"
)

// TestHealthzOK checks the anonymous health view: status ok and no details.
func TestHealthzOK(t *testing.T) {
	_, ts := startAPI(t, nil)

	status, body, _ := apiCall(t, ts, http.MethodGet, "/healthz", "", nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", status, body)
	}
	if got := str(body, "status"); got != "ok" {
		t.Errorf("status = %q, want ok", got)
	}
	if _, present := body["details"]; present {
		t.Errorf("anonymous request must not carry details: %v", body)
	}
}

// TestHealthzDegradedUpstream checks the 503 path when the upstream probe
// failed.
func TestHealthzDegradedUpstream(t *testing.T) {
	_, ts := startAPI(t, func(o *Options) { o.Monitor = unhealthyMonitor(t) })

	status, body, _ := apiCall(t, ts, http.MethodGet, "/healthz", "", nil)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %v)", status, body)
	}
	if got := str(body, "status"); got != "degraded" {
		t.Errorf("status = %q, want degraded", got)
	}
}

// TestHealthzDegradedDatabase checks the 503 path when the store is closed.
func TestHealthzDegradedDatabase(t *testing.T) {
	st, ts := startAPI(t, nil)
	if err := st.Close(); err != nil {
		t.Fatalf("close store: %v", err)
	}

	status, body, _ := apiCall(t, ts, http.MethodGet, "/healthz", "", nil)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (body %v)", status, body)
	}
	if got := str(body, "status"); got != "degraded" {
		t.Errorf("status = %q, want degraded", got)
	}
}

// TestHealthzDetailsWithToken checks the details payload unlocked by any valid
// token, including the last successful relay timestamp once a message has been
// sent.
func TestHealthzDetailsWithToken(t *testing.T) {
	st, ts := startAPI(t, nil)
	token := seedToken(t, st, "watch", store.RoleViewer)

	status, body, _ := apiCall(t, ts, http.MethodGet, "/healthz", token, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", status, body)
	}
	details, ok := body["details"].(map[string]any)
	if !ok {
		t.Fatalf("details missing for valid token: %v", body)
	}
	if got := str(details, "database"); got != "ok" {
		t.Errorf("database = %q, want ok", got)
	}
	if got := str(details, "upstream"); got != "ok" {
		t.Errorf("upstream = %q, want ok", got)
	}
	if got := str(details, "version"); got != "test-version" {
		t.Errorf("version = %q, want test-version", got)
	}
	if up := num(details, "uptime_seconds"); up < 0 {
		t.Errorf("uptime_seconds = %v, want >= 0", up)
	}
	// The field is always serialized (nil becomes JSON null when nothing has
	// been relayed yet).
	if details["last_relay_success_at"] != nil {
		t.Errorf("no relay yet, last_relay_success_at = %v", details["last_relay_success_at"])
	}

	// A details request touches the token's last_used.
	tok, err := st.GetAdminTokenByHash(t.Context(), store.HashToken(token))
	if err != nil {
		t.Fatalf("load token: %v", err)
	}
	if tok.LastUsed == nil {
		t.Error("last_used not updated")
	}

	// Once a message has been relayed, details report its timestamp.
	app := seedApp(t, st, "alpha")
	if err := st.InsertMessage(t.Context(), &store.Message{
		AppID:    app.ID,
		MailFrom: "NoReply@example.com",
		RcptTo:   "user@example.com",
		Status:   store.StatusSent,
	}); err != nil {
		t.Fatalf("insert message: %v", err)
	}
	_, body, _ = apiCall(t, ts, http.MethodGet, "/healthz", token, nil)
	details, ok = body["details"].(map[string]any)
	if !ok {
		t.Fatalf("details missing after relay: %v", body)
	}
	last := str(details, "last_relay_success_at")
	if last == "" {
		t.Fatalf("last_relay_success_at missing: %v", details)
	}
	if _, err := time.Parse(time.RFC3339, last); err != nil {
		t.Errorf("last_relay_success_at %q is not RFC 3339: %v", last, err)
	}
}

// TestHealthzDetailsRejectedForBadTokens checks that unknown and expired
// tokens never unlock the details payload.
func TestHealthzDetailsRejectedForBadTokens(t *testing.T) {
	st, ts := startAPI(t, nil)

	past := time.Now().Add(-time.Hour)
	if err := st.CreateAdminToken(t.Context(), &store.AdminToken{
		Name:      "expired",
		Role:      store.RoleAdmin,
		TokenHash: store.HashToken("rw_expired_healthz"),
		ExpiresAt: &past,
	}, nil); err != nil {
		t.Fatalf("seed expired token: %v", err)
	}

	for _, tc := range []struct{ name, token string }{
		{"unknown", "rw_unknown_healthz"},
		{"expired", "rw_expired_healthz"},
		{"missing", ""},
	} {
		_, body, _ := apiCall(t, ts, http.MethodGet, "/healthz", tc.token, nil)
		if _, present := body["details"]; present {
			t.Errorf("%s token must not yield details: %v", tc.name, body)
		}
		if got := str(body, "status"); got != "ok" {
			t.Errorf("%s token: status = %q, want ok (healthz never reports auth failures)", tc.name, got)
		}
	}
}

// TestSecurityHeaders checks the mandatory headers on every response, both the
// public health surface and the token-authenticated API.
func TestSecurityHeaders(t *testing.T) {
	_, ts := startAPI(t, nil)

	for _, path := range []string{"/healthz", "/api/apps"} {
		_, _, headers := apiCall(t, ts, http.MethodGet, path, "", nil)
		if got := headers.Get("Content-Security-Policy"); got == "" {
			t.Errorf("%s: Content-Security-Policy missing", path)
		}
		if got := headers.Get("X-Content-Type-Options"); got != "nosniff" {
			t.Errorf("%s: X-Content-Type-Options = %q, want nosniff", path, got)
		}
		if got := headers.Get("Referrer-Policy"); got != "no-referrer" {
			t.Errorf("%s: Referrer-Policy = %q, want no-referrer", path, got)
		}
	}
}
