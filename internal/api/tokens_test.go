package api

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"relayward-mail/internal/store"
)

// TestListTokensAsAdmin checks the token list: admin only, no hashes exposed.
func TestListTokensAsAdmin(t *testing.T) {
	st, ts := startAPI(t, nil)
	admin := seedToken(t, st, "admin", store.RoleAdmin)
	seedToken(t, st, "ci", store.RoleOperator)

	status, body, _ := apiCall(t, ts, http.MethodGet, "/api/tokens", admin, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", status, body)
	}
	tokens := bodySlice(t, body, "tokens")
	if len(tokens) != 2 {
		t.Fatalf("got %d tokens, want 2: %v", len(tokens), body)
	}
	names := map[string]bool{}
	for _, tok := range tokens {
		if _, present := tok["token_hash"]; present {
			t.Errorf("token list leaks token_hash: %v", tok)
		}
		if _, present := tok["hash"]; present {
			t.Errorf("token list leaks hash: %v", tok)
		}
		names[str(tok, "name")] = true
	}
	if !names["admin"] || !names["ci"] {
		t.Errorf("token list = %v, want admin and ci", names)
	}
}

// TestListTokensForbidden checks that non-admin roles cannot list tokens.
func TestListTokensForbidden(t *testing.T) {
	st, ts := startAPI(t, nil)

	for _, role := range []string{store.RoleViewer, store.RoleOperator} {
		token := seedToken(t, st, "list-"+role, role)
		status, body, _ := apiCall(t, ts, http.MethodGet, "/api/tokens", token, nil)
		wantError(t, status, body, http.StatusForbidden, "forbidden")
	}
}

// TestCreateToken checks the 201, the rw_ plaintext prefix and the token_info
// payload.
func TestCreateToken(t *testing.T) {
	st, ts := startAPI(t, nil)
	admin := seedToken(t, st, "admin", store.RoleAdmin)

	status, body, _ := apiCall(t, ts, http.MethodPost, "/api/tokens", admin, map[string]any{
		"name": "ci", "role": "operator",
	})
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %v)", status, body)
	}
	raw := str(body, "token")
	if !strings.HasPrefix(raw, "rw_") {
		t.Errorf("token = %q, want rw_ prefix", raw)
	}

	info := bodyMap(t, body, "token_info")
	if got := str(info, "name"); got != "ci" {
		t.Errorf("token_info.name = %q, want ci", got)
	}
	if got := str(info, "role"); got != "operator" {
		t.Errorf("token_info.role = %q, want operator", got)
	}
	if got := num(info, "id"); got <= 0 {
		t.Errorf("token_info.id = %v, want > 0", got)
	}
	if _, present := info["token_hash"]; present {
		t.Errorf("token_info leaks token_hash: %v", info)
	}
	if _, present := info["hash"]; present {
		t.Errorf("token_info leaks hash: %v", info)
	}
}

// TestCreateTokenValidation checks the 400 paths for malformed create bodies.
func TestCreateTokenValidation(t *testing.T) {
	st, ts := startAPI(t, nil)
	admin := seedToken(t, st, "admin", store.RoleAdmin)

	tests := []struct {
		name string
		body map[string]any
	}{
		{"bad role", map[string]any{"name": "x", "role": "root"}},
		{"missing role", map[string]any{"name": "x"}},
		{"empty name", map[string]any{"name": "", "role": "viewer"}},
		{"expires_at in the past", map[string]any{"name": "x", "role": "viewer", "expires_at": "2000-01-01T00:00:00Z"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, body, _ := apiCall(t, ts, http.MethodPost, "/api/tokens", admin, tc.body)
			wantError(t, status, body, http.StatusBadRequest, "invalid_request")
		})
	}
}

// TestNewTokenAuthenticates checks that a freshly minted token immediately
// works against a role-appropriate endpoint.
func TestNewTokenAuthenticates(t *testing.T) {
	st, ts := startAPI(t, nil)
	admin := seedToken(t, st, "admin", store.RoleAdmin)

	_, body, _ := apiCall(t, ts, http.MethodPost, "/api/tokens", admin, map[string]any{
		"name": "fresh", "role": "viewer",
	})
	raw := str(body, "token")
	if raw == "" {
		t.Fatal("created token empty")
	}

	status, _, _ := apiCall(t, ts, http.MethodGet, "/api/apps", raw, nil)
	if status != http.StatusOK {
		t.Fatalf("new token GET /api/apps: status = %d, want 200", status)
	}
	tok, err := st.GetAdminTokenByHash(t.Context(), store.HashToken(raw))
	if err != nil {
		t.Fatalf("load token: %v", err)
	}
	if tok.LastUsed == nil {
		t.Error("last_used not updated after use")
	}
}

// TestRevokeTokenLifecycle checks revoke, double revoke and stale plaintext.
func TestRevokeTokenLifecycle(t *testing.T) {
	st, ts := startAPI(t, nil)
	admin := seedToken(t, st, "admin", store.RoleAdmin)

	_, body, _ := apiCall(t, ts, http.MethodPost, "/api/tokens", admin, map[string]any{
		"name": "shortlived", "role": "viewer",
	})
	raw := str(body, "token")
	id := int64(num(bodyMap(t, body, "token_info"), "id"))
	if raw == "" || id == 0 {
		t.Fatalf("bad create response: %v", body)
	}

	status, respBody, _ := apiCall(t, ts, http.MethodDelete, fmt.Sprintf("/api/tokens/%d", id), admin, nil)
	if status != http.StatusNoContent {
		t.Fatalf("revoke: status = %d, want 204 (body %v)", status, respBody)
	}

	// Revoking again is a 404.
	status, respBody, _ = apiCall(t, ts, http.MethodDelete, fmt.Sprintf("/api/tokens/%d", id), admin, nil)
	wantError(t, status, respBody, http.StatusNotFound, "token_not_found")

	// The old plaintext no longer authenticates.
	status, respBody, _ = apiCall(t, ts, http.MethodGet, "/api/apps", raw, nil)
	wantError(t, status, respBody, http.StatusUnauthorized, "unauthorized")
}

// TestRevokeTokenInvalidID checks that a non-numeric id is a 400.
func TestRevokeTokenInvalidID(t *testing.T) {
	st, ts := startAPI(t, nil)
	admin := seedToken(t, st, "admin", store.RoleAdmin)

	status, body, _ := apiCall(t, ts, http.MethodDelete, "/api/tokens/not-a-number", admin, nil)
	wantError(t, status, body, http.StatusBadRequest, "invalid_request")
}

// TestRevokeInitialTokenDeletesFile checks that revoking the bootstrap token
// named "initial" also removes the DataDir/initial_admin_token file.
func TestRevokeInitialTokenDeletesFile(t *testing.T) {
	dir := t.TempDir()
	st, ts := startAPI(t, func(o *Options) { o.DataDir = dir })
	admin := seedToken(t, st, "initial", store.RoleAdmin)

	tokenFile := filepath.Join(dir, "initial_admin_token")
	if err := os.WriteFile(tokenFile, []byte("rw_test_initial"), 0o600); err != nil {
		t.Fatalf("write initial token file: %v", err)
	}

	tokens, err := st.ListAdminTokens(t.Context())
	if err != nil {
		t.Fatalf("list tokens: %v", err)
	}
	var id int64
	for _, tok := range tokens {
		if tok.Name == "initial" {
			id = tok.ID
		}
	}
	if id == 0 {
		t.Fatal("no token named initial found")
	}

	status, body, _ := apiCall(t, ts, http.MethodDelete, fmt.Sprintf("/api/tokens/%d", id), admin, nil)
	if status != http.StatusNoContent {
		t.Fatalf("revoke initial: status = %d, want 204 (body %v)", status, body)
	}
	if _, err := os.Stat(tokenFile); !os.IsNotExist(err) {
		t.Errorf("initial_admin_token file still exists after revoke (stat err = %v)", err)
	}
}

// TestRevokeCreatedTokenNamedInitialKeepsFile checks that a token created
// through the API is never mistaken for the bootstrap token.
func TestRevokeCreatedTokenNamedInitialKeepsFile(t *testing.T) {
	dir := t.TempDir()
	st, ts := startAPI(t, func(o *Options) { o.DataDir = dir })
	admin := seedToken(t, st, "admin", store.RoleAdmin)

	tokenFile := filepath.Join(dir, "initial_admin_token")
	if err := os.WriteFile(tokenFile, []byte("rw_test_initial"), 0o600); err != nil {
		t.Fatalf("write initial token file: %v", err)
	}

	status, body, _ := apiCall(t, ts, http.MethodPost, "/api/tokens", admin, map[string]any{"name": "initial", "role": "viewer"})
	if status != http.StatusCreated {
		t.Fatalf("create token: status = %d (body %v)", status, body)
	}
	info, _ := body["token_info"].(map[string]any)
	id := int64(num(info, "id"))

	status, body, _ = apiCall(t, ts, http.MethodDelete, fmt.Sprintf("/api/tokens/%d", id), admin, nil)
	if status != http.StatusNoContent {
		t.Fatalf("revoke: status = %d (body %v)", status, body)
	}
	if _, err := os.Stat(tokenFile); err != nil {
		t.Errorf("initial_admin_token file must survive, stat err = %v", err)
	}
}

// TestAuditRecordsManagementWrites checks that app.create and token.create
// land in the audit log, newest first, attributed to the acting token.
func TestAuditRecordsManagementWrites(t *testing.T) {
	st, ts := startAPI(t, nil)
	admin := seedToken(t, st, "admin", store.RoleAdmin)

	status, body, _ := apiCall(t, ts, http.MethodPost, "/api/apps", admin, createAppBody("audited"))
	if status != http.StatusCreated {
		t.Fatalf("create app: status = %d, want 201 (body %v)", status, body)
	}
	status, body, _ = apiCall(t, ts, http.MethodPost, "/api/tokens", admin, map[string]any{
		"name": "ci", "role": "viewer",
	})
	if status != http.StatusCreated {
		t.Fatalf("create token: status = %d, want 201 (body %v)", status, body)
	}

	status, body, _ = apiCall(t, ts, http.MethodGet, "/api/audit", admin, nil)
	if status != http.StatusOK {
		t.Fatalf("audit: status = %d, want 200 (body %v)", status, body)
	}
	entries := bodySlice(t, body, "entries")
	if len(entries) < 2 {
		t.Fatalf("got %d audit entries, want >= 2: %v", len(entries), body)
	}

	// Newest first: the token creation is on top.
	first := entries[0]
	if got := str(first, "action"); got != store.ActionTokenCreate {
		t.Errorf("entries[0].action = %q, want %q", got, store.ActionTokenCreate)
	}
	if got := str(first, "token_name"); got != "admin" {
		t.Errorf("entries[0].token_name = %q, want admin", got)
	}
	if got := str(first, "ip"); got != "127.0.0.1" {
		t.Errorf("entries[0].ip = %q, want 127.0.0.1", got)
	}

	found := false
	for _, e := range entries {
		if str(e, "action") == store.ActionAppCreate {
			found = true
		}
	}
	if !found {
		t.Errorf("no app.create entry in %v", entries)
	}
}

// TestAuditForbidden checks that non-admin roles cannot read the audit log.
func TestAuditForbidden(t *testing.T) {
	st, ts := startAPI(t, nil)
	op := seedToken(t, st, "op", store.RoleOperator)

	status, body, _ := apiCall(t, ts, http.MethodGet, "/api/audit", op, nil)
	wantError(t, status, body, http.StatusForbidden, "forbidden")
}

// TestAuditPagination walks the audit log with limit=1 and a cursor.
func TestAuditPagination(t *testing.T) {
	st, ts := startAPI(t, nil)
	admin := seedToken(t, st, "admin", store.RoleAdmin)

	// Exactly two audit rows, both from app creates.
	for _, name := range []string{"page-one", "page-two"} {
		status, body, _ := apiCall(t, ts, http.MethodPost, "/api/apps", admin, createAppBody(name))
		if status != http.StatusCreated {
			t.Fatalf("create app %s: status = %d, want 201 (body %v)", name, status, body)
		}
	}

	seen := map[string]bool{}
	cursor := ""
	for pages := 1; ; pages++ {
		if pages > 10 {
			t.Fatal("audit pagination did not terminate")
		}
		path := "/api/audit?limit=1"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		status, body, _ := apiCall(t, ts, http.MethodGet, path, admin, nil)
		if status != http.StatusOK {
			t.Fatalf("page %d: status = %d, want 200 (body %v)", pages, status, body)
		}
		entries := bodySlice(t, body, "entries")
		if len(entries) != 1 {
			t.Fatalf("page %d: got %d entries, want 1", pages, len(entries))
		}
		seen[str(entries[0], "target")] = true
		cursor = str(body, "next_cursor")
		if cursor == "" {
			break
		}
	}
	if len(seen) != 2 || !seen["page-one"] || !seen["page-two"] {
		t.Errorf("walked audit targets %v, want page-one and page-two", seen)
	}
}
