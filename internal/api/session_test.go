package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"relayward-mail/internal/ratelimit"
	"relayward-mail/internal/store"
)

// cookieCall performs one request carrying the given cookie; mirrors apiCall.
func cookieCall(t *testing.T, ts *httptest.Server, method, path string, c *http.Cookie, body any) (int, map[string]any, http.Header) {
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
	req.AddCookie(c)
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

// login performs POST /api/login and returns the session cookie (nil when
// none was set).
func login(t *testing.T, ts *httptest.Server, token, turnstile string) (int, map[string]any, *http.Cookie) {
	t.Helper()

	body := map[string]any{"token": token}
	if turnstile != "" {
		body["turnstile"] = turnstile
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal login body: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/api/login", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("build login: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST /api/login: %v", err)
	}
	defer resp.Body.Close()

	decoded := map[string]any{}
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		t.Fatalf("decode login response: %v", err)
	}
	var cookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookieName {
			cookie = c
		}
	}
	return resp.StatusCode, decoded, cookie
}

// setSiteverifyURL points the Turnstile verifier elsewhere for the test and
// restores the real endpoint afterwards.
func setSiteverifyURL(t *testing.T, url string) {
	t.Helper()
	orig := siteverifyURL
	siteverifyURL = url
	t.Cleanup(func() { siteverifyURL = orig })
}

// startSiteverifyStub points the verifier at a stub that always answers with
// the given verdict.
func startSiteverifyStub(t *testing.T, success bool) {
	t.Helper()
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("stub: parse form: %v", err)
		}
		if r.FormValue("secret") != "0xSECRET" || r.FormValue("response") == "" {
			t.Errorf("stub: unexpected siteverify call: secret=%q response=%q",
				r.FormValue("secret"), r.FormValue("response"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"success": success})
	}))
	t.Cleanup(ts.Close)
	setSiteverifyURL(t, ts.URL)
}

// breakSiteverify points the verifier at a closed port (Cloudflare down).
func breakSiteverify(t *testing.T) {
	t.Helper()
	setSiteverifyURL(t, "http://127.0.0.1:1/turnstile/v0/siteverify")
}

func TestLoginCreatesWorkingSession(t *testing.T) {
	st, ts := startAPI(t, nil)
	admin := seedToken(t, st, "admin", store.RoleAdmin)

	status, body, cookie := login(t, ts, admin, "")
	if status != http.StatusOK {
		t.Fatalf("login status = %d, want 200 (body %v)", status, body)
	}
	if cookie == nil || cookie.Value == "" {
		t.Fatal("login set no session cookie")
	}
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode {
		t.Errorf("cookie flags wrong: %+v", cookie)
	}
	if str(body, "role") != store.RoleAdmin || str(body, "token_name") != "admin" {
		t.Errorf("login body mismatch: %v", body)
	}

	// The cookie grants API access without any Authorization header.
	status, body, _ = cookieCall(t, ts, http.MethodGet, "/api/apps", cookie, nil)
	if status != http.StatusOK {
		t.Fatalf("session /api/apps status = %d, want 200 (body %v)", status, body)
	}

	// /api/session reports who is logged in.
	status, body, _ = cookieCall(t, ts, http.MethodGet, "/api/session", cookie, nil)
	if status != http.StatusOK || body["authenticated"] != true {
		t.Fatalf("/api/session: status=%d body=%v", status, body)
	}
	if str(body, "role") != store.RoleAdmin || str(body, "token_name") != "admin" {
		t.Errorf("/api/session body mismatch: %v", body)
	}

	// Logout drops the session; the old cookie no longer works.
	status, _, _ = cookieCall(t, ts, http.MethodPost, "/api/logout", cookie, nil)
	if status != http.StatusNoContent {
		t.Fatalf("logout status = %d, want 204", status)
	}
	status, body, _ = cookieCall(t, ts, http.MethodGet, "/api/apps", cookie, nil)
	wantError(t, status, body, http.StatusUnauthorized, "unauthorized")
}

func TestSessionStatusUnauthenticatedHidesEverything(t *testing.T) {
	_, ts := startAPI(t, nil)

	status, body, _ := apiCall(t, ts, http.MethodGet, "/api/session", "", nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if body["authenticated"] != false {
		t.Errorf("authenticated = %v, want false", body["authenticated"])
	}
	if str(body, "role") != "" || str(body, "token_name") != "" {
		t.Errorf("unauthenticated session leaks identity: %v", body)
	}
	if str(body, "turnstile_site_key") != "" {
		t.Errorf("site key present with captcha disabled: %v", body)
	}
}

func TestSessionStatusExposesSiteKeyWhenEnabled(t *testing.T) {
	_, ts := startAPI(t, func(o *Options) {
		o.TurnstileSiteKey = "0xSITEKEY"
		o.TurnstileSecretKey = "0xSECRET"
	})

	status, body, _ := apiCall(t, ts, http.MethodGet, "/api/session", "", nil)
	if status != http.StatusOK || str(body, "turnstile_site_key") != "0xSITEKEY" {
		t.Fatalf("status=%d body=%v", status, body)
	}
}

func TestLoginRejectsBadToken(t *testing.T) {
	st, ts := startAPI(t, nil)
	seedToken(t, st, "admin", store.RoleAdmin)

	status, body, cookie := login(t, ts, "rw_wrong_token", "")
	wantError(t, status, body, http.StatusUnauthorized, "unauthorized")
	if cookie != nil {
		t.Errorf("failed login set a cookie: %v", cookie)
	}
}

func TestLoginRejectsExpiredToken(t *testing.T) {
	st, ts := startAPI(t, nil)

	past := time.Now().Add(-time.Hour)
	if err := st.CreateAdminToken(t.Context(), &store.AdminToken{
		Name:      "expired",
		Role:      store.RoleAdmin,
		TokenHash: store.HashToken("rw_expired_login"),
		ExpiresAt: &past,
	}, nil); err != nil {
		t.Fatalf("seed expired token: %v", err)
	}

	status, body, _ := login(t, ts, "rw_expired_login", "")
	wantError(t, status, body, http.StatusUnauthorized, "unauthorized")
}

func TestRevokedTokenKillsItsSession(t *testing.T) {
	st, ts := startAPI(t, nil)
	admin := seedToken(t, st, "admin", store.RoleAdmin)

	_, _, cookie := login(t, ts, admin, "")
	if cookie == nil {
		t.Fatal("no session cookie")
	}

	tokens, err := st.ListAdminTokens(t.Context())
	if err != nil || len(tokens) != 1 {
		t.Fatalf("list tokens: %v %d", err, len(tokens))
	}
	if err := st.DeleteAdminToken(t.Context(), tokens[0].ID, nil); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	status, body, _ := cookieCall(t, ts, http.MethodGet, "/api/apps", cookie, nil)
	wantError(t, status, body, http.StatusUnauthorized, "unauthorized")
}

/* ---- Turnstile ----------------------------------------------------------- */

func startTurnstileAPI(t *testing.T) (*store.Store, *httptest.Server) {
	t.Helper()
	return startAPI(t, func(o *Options) {
		o.TurnstileSiteKey = "0xSITEKEY"
		o.TurnstileSecretKey = "0xSECRET"
	})
}

func TestLoginRequiresTurnstileWhenEnabled(t *testing.T) {
	_, ts := startTurnstileAPI(t)
	startSiteverifyStub(t, true)

	// A missing widget token fails closed without ever calling siteverify
	// (the stub would flag an unexpected call with an empty response).
	status, body, cookie := login(t, ts, "rw_whatever", "")
	wantError(t, status, body, http.StatusUnauthorized, "captcha_failed")
	if cookie != nil {
		t.Error("failed captcha login set a cookie")
	}
}

func TestLoginVerifiesTurnstile(t *testing.T) {
	st, ts := startTurnstileAPI(t)
	admin := seedToken(t, st, "admin", store.RoleAdmin)

	startSiteverifyStub(t, false)
	status, body, _ := login(t, ts, admin, "XXXX.DUMMY.TOKEN.XXXX")
	wantError(t, status, body, http.StatusUnauthorized, "captcha_failed")

	startSiteverifyStub(t, true)
	status, body, cookie := login(t, ts, admin, "XXXX.DUMMY.TOKEN.XXXX")
	if status != http.StatusOK || cookie == nil {
		t.Fatalf("valid captcha login failed: status=%d body=%v", status, body)
	}

	// The session then works like any other.
	got, _, _ := cookieCall(t, ts, http.MethodGet, "/api/session", cookie, nil)
	if got != http.StatusOK {
		t.Fatalf("/api/session status = %d", got)
	}
}

func TestLoginFailsClosedWhenSiteverifyUnreachable(t *testing.T) {
	st, ts := startTurnstileAPI(t)
	admin := seedToken(t, st, "admin", store.RoleAdmin)
	breakSiteverify(t)

	status, body, _ := login(t, ts, admin, "XXXX.DUMMY.TOKEN.XXXX")
	wantError(t, status, body, http.StatusServiceUnavailable, "captcha_unavailable")
}

func TestLoginTurnstileFailureCountsTowardLockout(t *testing.T) {
	_, ts := startAPI(t, func(o *Options) {
		o.TurnstileSiteKey = "0xSITEKEY"
		o.TurnstileSecretKey = "0xSECRET"
		o.Lockout = ratelimit.NewLockout(2, time.Minute, time.Hour)
	})
	startSiteverifyStub(t, false)

	for i := 0; i < 2; i++ {
		status, _, _ := login(t, ts, "rw_anything", "XXXX.DUMMY.TOKEN.XXXX")
		if status != http.StatusUnauthorized {
			t.Fatalf("failure %d: status = %d, want 401", i+1, status)
		}
	}
	status, body, _ := login(t, ts, "rw_anything", "XXXX.DUMMY.TOKEN.XXXX")
	wantError(t, status, body, http.StatusTooManyRequests, "ip_banned")
}
