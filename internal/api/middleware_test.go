package api

import (
	"net/http"
	"reflect"
	"testing"
	"time"

	"relayward-mail/internal/ratelimit"
	"relayward-mail/internal/store"
)

// TestUnauthorizedResponsesAreUniform checks that missing, unknown and expired
// tokens all produce byte-identical 401 error payloads.
func TestUnauthorizedResponsesAreUniform(t *testing.T) {
	st, ts := startAPI(t, nil)

	past := time.Now().Add(-time.Hour)
	if err := st.CreateAdminToken(t.Context(), &store.AdminToken{
		Name:      "expired",
		Role:      store.RoleAdmin,
		TokenHash: store.HashToken("rw_expired_middleware"),
		ExpiresAt: &past,
	}, nil); err != nil {
		t.Fatalf("seed expired token: %v", err)
	}

	var first map[string]any
	for _, tc := range []struct{ name, token string }{
		{"missing token", ""},
		{"unknown token", "rw_unknown_middleware"},
		{"expired token", "rw_expired_middleware"},
	} {
		status, body, _ := apiCall(t, ts, http.MethodGet, "/api/apps", tc.token, nil)
		if status != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401 (body %v)", tc.name, status, body)
			continue
		}
		errObj, ok := body["error"].(map[string]any)
		if !ok {
			t.Fatalf("%s: body %v carries no error object", tc.name, body)
		}
		if got := str(errObj, "code"); got != "unauthorized" {
			t.Errorf("%s: error.code = %q, want unauthorized", tc.name, got)
		}
		if got := str(errObj, "message"); got != "missing or invalid token" {
			t.Errorf("%s: error.message = %q, want %q", tc.name, got, "missing or invalid token")
		}
		if first == nil {
			first = errObj
		} else if !reflect.DeepEqual(first, errObj) {
			t.Errorf("%s: error object %v differs from the first case %v", tc.name, errObj, first)
		}
	}
}

// TestLockoutBansIPAfterRepeatedFailures checks that repeated authentication
// failures ban the client IP, and that a valid token no longer helps while the
// ban is active.
func TestLockoutBansIPAfterRepeatedFailures(t *testing.T) {
	st, ts := startAPI(t, func(o *Options) {
		o.Lockout = ratelimit.NewLockout(3, time.Minute, time.Hour)
	})
	admin := seedToken(t, st, "admin", store.RoleAdmin)

	// Three failures from 127.0.0.1 reach the configured limit of 3.
	for i := 0; i < 3; i++ {
		status, body, _ := apiCall(t, ts, http.MethodGet, "/api/apps", "rw_bad_token", nil)
		if status != http.StatusUnauthorized {
			t.Fatalf("failure %d: status = %d, want 401 (body %v)", i+1, status, body)
		}
	}

	status, body, _ := apiCall(t, ts, http.MethodGet, "/api/apps", admin, nil)
	wantError(t, status, body, http.StatusTooManyRequests, "ip_banned")
}

// TestIPAllowlistBlocksForeignIP checks that a client outside the allowlist is
// refused with 403 ip_forbidden even when it carries a valid token.
func TestIPAllowlistBlocksForeignIP(t *testing.T) {
	st, ts := startAPI(t, func(o *Options) {
		o.IPAllowlist = []string{"203.0.113.0/24"}
	})
	admin := seedToken(t, st, "admin", store.RoleAdmin)

	status, body, _ := apiCall(t, ts, http.MethodGet, "/api/apps", admin, nil)
	wantError(t, status, body, http.StatusForbidden, "ip_forbidden")
}

// TestIPAllowlistAllowsLoopback checks that the httptest client's own address
// passes when the allowlist contains it.
func TestIPAllowlistAllowsLoopback(t *testing.T) {
	st, ts := startAPI(t, func(o *Options) {
		o.IPAllowlist = []string{"127.0.0.1"}
	})
	admin := seedToken(t, st, "admin", store.RoleAdmin)

	status, body, _ := apiCall(t, ts, http.MethodGet, "/api/apps", admin, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", status, body)
	}
}
