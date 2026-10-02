package web

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"relayward-mail/internal/store"
	"relayward-mail/internal/unsub"
)

// env bundles everything a page test needs.
type env struct {
	st      *store.Store
	app     *store.App
	handler http.Handler
	ts      *httptest.Server
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "relayward.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	mgr, err := unsub.NewManager([]byte("web-test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	hash, err := store.HashPassword("pw-gitea")
	if err != nil {
		t.Fatal(err)
	}
	app := &store.App{
		Name:         "gitea",
		PasswordHash: hash,
		Enabled:      true,
		Unsubscribe:  true,
		AllowedFrom:  []string{"NoReply@example.com"},
		RatePerHour:  500,
		DisplayName:  "Gitea",
	}
	if err := st.CreateApp(t.Context(), app, nil); err != nil {
		t.Fatal(err)
	}

	h := New(Options{Store: st, Unsub: mgr, Logger: quietLogger()})
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return &env{st: st, app: app, handler: h, ts: ts}
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// tokenFor mints a valid token for one recipient of the seeded app.
func (e *env) tokenFor(t *testing.T, email string) string {
	t.Helper()
	tok, err := unsub.NewManager([]byte("web-test-secret"))
	if err != nil {
		t.Fatal(err)
	}
	link, err := tok.Token(e.app.ID, email)
	if err != nil {
		t.Fatal(err)
	}
	return link
}

// get performs a GET and returns status, body and the response cookies.
func (e *env) get(t *testing.T, path string) (int, string, []*http.Cookie) {
	t.Helper()
	resp, err := http.Get(e.ts.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(raw), resp.Cookies()
}

// post sends a form POST carrying the given cookie.
func (e *env) post(t *testing.T, path string, form url.Values, cookie *http.Cookie) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, e.ts.URL+path, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if cookie != nil {
		req.AddCookie(cookie)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(raw)
}

// findCSRF returns the rw_csrf cookie granted by the last response.
func findCSRF(t *testing.T, cookies []*http.Cookie) *http.Cookie {
	t.Helper()
	for _, c := range cookies {
		if c.Name == csrfCookie {
			return c
		}
	}
	t.Fatalf("no %s cookie in response", csrfCookie)
	return nil
}

func TestConfirmPageHasNoSideEffect(t *testing.T) {
	e := newEnv(t)
	token := e.tokenFor(t, "user@example.com")

	status, body, cookies := e.get(t, "/u/"+token)
	if status != http.StatusOK {
		t.Fatalf("GET status = %d", status)
	}
	for _, want := range []string{"确认退订", "Gitea", "user@example.com"} {
		if !strings.Contains(body, want) {
			t.Errorf("page missing %q:\n%s", want, body)
		}
	}
	findCSRF(t, cookies) // form needs a CSRF pair

	// GET must not unsubscribe anything (mail scanners fetch links).
	if yes, _ := e.st.IsUnsubscribed(t.Context(), e.app.ID, "user@example.com"); yes {
		t.Fatal("GET must not unsubscribe")
	}
}

func TestConfirmInvalidTokenIs404(t *testing.T) {
	e := newEnv(t)
	status, body, _ := e.get(t, "/u/not-a-real-token")
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", status)
	}
	if strings.Contains(body, "Gitea") {
		t.Fatal("404 must not reveal app information")
	}
}

func TestSecurityHeadersOnPages(t *testing.T) {
	e := newEnv(t)
	token := e.tokenFor(t, "user@example.com")

	req, _ := http.NewRequest(http.MethodGet, e.ts.URL+"/u/"+token, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if got := resp.Header.Get("Content-Security-Policy"); got == "" {
		t.Error("missing Content-Security-Policy")
	}
	if got := resp.Header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := resp.Header.Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", got)
	}
}

func TestOneClickUnsubscribe(t *testing.T) {
	e := newEnv(t)
	token := e.tokenFor(t, "user@example.com")

	status, body := e.post(t, "/u/"+token, url.Values{"List-Unsubscribe": {"One-Click"}}, nil)
	if status != http.StatusOK {
		t.Fatalf("one-click status = %d body = %s", status, body)
	}
	if !strings.Contains(body, "已退订") {
		t.Errorf("one-click response missing confirmation: %s", body)
	}
	yes, err := e.st.IsUnsubscribed(context.Background(), e.app.ID, "user@example.com")
	if err != nil || !yes {
		t.Fatalf("one-click did not record the unsubscribe: yes=%v err=%v", yes, err)
	}

	page, _, err := e.st.ListUnsubscribes(t.Context(), store.UnsubscribeFilter{})
	if err != nil || len(page) != 1 || page[0].Source != store.SourceOneClick {
		t.Fatalf("source = %+v err=%v, want one row from one_click", page, err)
	}
}

func TestFormUnsubscribeNeedsCSRF(t *testing.T) {
	e := newEnv(t)
	token := e.tokenFor(t, "user@example.com")

	// Without the CSRF pair the form is rejected.
	if status, _ := e.post(t, "/u/"+token, url.Values{"csrf": {"wrong"}}, nil); status != http.StatusBadRequest {
		t.Fatalf("bad csrf status = %d, want 400", status)
	}
	if status, _ := e.post(t, "/u/"+token, nil, nil); status != http.StatusBadRequest {
		t.Fatalf("bare POST status = %d, want 400", status)
	}
	if yes, _ := e.st.IsUnsubscribed(t.Context(), e.app.ID, "user@example.com"); yes {
		t.Fatal("rejected POSTs must not unsubscribe")
	}

	// With the cookie granted on GET the form goes through.
	_, _, cookies := e.get(t, "/u/"+token)
	cookie := findCSRF(t, cookies)
	status, body := e.post(t, "/u/"+token, url.Values{"csrf": {cookie.Value}}, cookie)
	if status != http.StatusOK || !strings.Contains(body, "已退订") {
		t.Fatalf("form POST: status = %d body = %s", status, body)
	}
	page, _, _ := e.st.ListUnsubscribes(t.Context(), store.UnsubscribeFilter{})
	if len(page) != 1 || page[0].Source != store.SourceLink {
		t.Fatalf("source = %+v, want one row from link", page)
	}

	// Repeating the unsubscribe is idempotent.
	status, _ = e.post(t, "/u/"+token, url.Values{"csrf": {cookie.Value}}, cookie)
	if status != http.StatusOK {
		t.Fatalf("repeat POST status = %d, want 200", status)
	}
	page, _, _ = e.st.ListUnsubscribes(t.Context(), store.UnsubscribeFilter{})
	if len(page) != 1 {
		t.Fatalf("repeat created a duplicate row: %+v", page)
	}
}

func TestResubscribeRemovesRow(t *testing.T) {
	e := newEnv(t)
	token := e.tokenFor(t, "user@example.com")

	if status, _ := e.post(t, "/u/"+token, url.Values{"List-Unsubscribe": {"One-Click"}}, nil); status != http.StatusOK {
		t.Fatal("setup: one-click failed")
	}

	_, _, cookies := e.get(t, "/u/"+token)
	cookie := findCSRF(t, cookies)
	status, body := e.post(t, "/u/"+token, url.Values{"csrf": {cookie.Value}, "action": {"resubscribe"}}, cookie)
	if status != http.StatusOK || !strings.Contains(body, "已重新订阅") {
		t.Fatalf("resubscribe: status = %d body = %s", status, body)
	}
	if yes, _ := e.st.IsUnsubscribed(t.Context(), e.app.ID, "user@example.com"); yes {
		t.Fatal("row still present after resubscribe")
	}

	// The unsubscribe and the resubscribe both left audit trails.
	page, _, _ := e.st.ListAuditLogs(t.Context(), store.AuditFilter{Limit: 10})
	actions := map[string]bool{}
	for _, entry := range page {
		actions[entry.Action] = true
	}
	if !actions[store.ActionUnsubscribeCreate] || !actions[store.ActionUnsubscribeDelete] {
		t.Fatalf("audit log = %+v, want create and delete entries", page)
	}
}

func TestUnknownPathAndMethod(t *testing.T) {
	e := newEnv(t)
	if status, _, _ := e.get(t, "/definitely-not-here"); status != http.StatusNotFound {
		t.Fatalf("unknown path status = %d, want 404", status)
	}

	token := e.tokenFor(t, "user@example.com")
	req, _ := http.NewRequest(http.MethodPut, e.ts.URL+"/u/"+token, nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("PUT status = %d, want 405", resp.StatusCode)
	}
}
