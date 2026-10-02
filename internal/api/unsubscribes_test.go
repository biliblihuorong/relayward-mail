package api

import (
	"net/http"
	"strconv"
	"testing"

	"relayward-mail/internal/store"
)

func itoa(id int64) string { return strconv.FormatInt(id, 10) }

func TestUnsubscribeEndpointsAuth(t *testing.T) {
	st, ts := startAPI(t, nil)
	seedApp(t, st, "gitea")
	viewer := seedToken(t, st, "view", store.RoleViewer)

	// No token is a uniform 401 on every unsubscribe route.
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/unsubscribes"},
		{http.MethodPost, "/api/unsubscribes"},
		{http.MethodDelete, "/api/unsubscribes/1"},
	} {
		status, body, _ := apiCall(t, ts, tc.method, tc.path, "", nil)
		if status != http.StatusUnauthorized {
			t.Fatalf("%s %s without token: status = %d, want 401 (body %v)", tc.method, tc.path, status, body)
		}
	}

	// Viewer may list.
	status, _, _ := apiCall(t, ts, http.MethodGet, "/api/unsubscribes", viewer, nil)
	if status != http.StatusOK {
		t.Fatalf("viewer list: status = %d, want 200", status)
	}

	// Viewer may not write.
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/api/unsubscribes"},
		{http.MethodDelete, "/api/unsubscribes/1"},
	} {
		status, body, _ := apiCall(t, ts, tc.method, tc.path, viewer, map[string]any{"app": "gitea", "email": "a@example.com"})
		if status != http.StatusForbidden {
			t.Fatalf("viewer %s %s: status = %d, want 403 (body %v)", tc.method, tc.path, status, body)
		}
	}
}

func TestCreateUnsubscribe(t *testing.T) {
	st, ts := startAPI(t, nil)
	admin := seedToken(t, st, "admin", store.RoleAdmin)
	seedApp(t, st, "gitea")

	status, body, _ := apiCall(t, ts, http.MethodPost, "/api/unsubscribes", admin,
		map[string]any{"app": "gitea", "email": " User@Example.com "})
	if status != http.StatusCreated {
		t.Fatalf("create: status = %d, want 201 (body %v)", status, body)
	}
	row := body["unsubscribe"].(map[string]any)
	if got := row["email"].(string); got != "user@example.com" {
		t.Fatalf("email = %q, want normalized lowercase", got)
	}
	if got := row["source"].(string); got != store.SourceAPI {
		t.Fatalf("source = %q, want api", got)
	}
	id := row["id"].(float64)

	// Duplicate create is idempotent and returns the row already on file.
	status, body, _ = apiCall(t, ts, http.MethodPost, "/api/unsubscribes", admin,
		map[string]any{"app": "gitea", "email": "user@example.com"})
	if status != http.StatusOK {
		t.Fatalf("duplicate create: status = %d, want 200", status)
	}
	if dup := body["unsubscribe"].(map[string]any)["id"].(float64); dup != id {
		t.Fatalf("duplicate created a new row %v, want id %v", dup, id)
	}

	// Validation errors.
	status, body, _ = apiCall(t, ts, http.MethodPost, "/api/unsubscribes", admin,
		map[string]any{"app": "missing", "email": "a@example.com"})
	if status != http.StatusNotFound || body["error"].(map[string]any)["code"] != "app_not_found" {
		t.Fatalf("unknown app: status = %d body = %v, want 404 app_not_found", status, body)
	}
	status, _, _ = apiCall(t, ts, http.MethodPost, "/api/unsubscribes", admin,
		map[string]any{"app": "gitea", "email": "not-an-address"})
	if status != http.StatusBadRequest {
		t.Fatalf("bad email: status = %d, want 400", status)
	}
	status, _, _ = apiCall(t, ts, http.MethodPost, "/api/unsubscribes", admin,
		map[string]any{"app": "gitea"})
	if status != http.StatusBadRequest {
		t.Fatalf("missing email: status = %d, want 400", status)
	}

	// The write landed in the audit log with the acting token.
	page, _, err := st.ListAuditLogs(t.Context(), store.AuditFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range page {
		if e.Action == store.ActionUnsubscribeCreate && e.TokenName != nil && *e.TokenName == "admin" {
			found = true
		}
	}
	if !found {
		t.Fatalf("audit log has no unsubscribe.create by admin: %+v", page)
	}
}

func TestDeleteUnsubscribe(t *testing.T) {
	st, ts := startAPI(t, nil)
	admin := seedToken(t, st, "admin", store.RoleAdmin)
	app := seedApp(t, st, "gitea")
	row, _, err := st.CreateUnsubscribe(t.Context(), app.ID, "user@example.com", store.SourceLink, nil)
	if err != nil {
		t.Fatal(err)
	}

	status, _, _ := apiCall(t, ts, http.MethodDelete, "/api/unsubscribes/"+itoa(row.ID), admin, nil)
	if status != http.StatusNoContent {
		t.Fatalf("delete: status = %d, want 204", status)
	}
	status, body, _ := apiCall(t, ts, http.MethodDelete, "/api/unsubscribes/"+itoa(row.ID), admin, nil)
	if status != http.StatusNotFound || body["error"].(map[string]any)["code"] != "unsubscribe_not_found" {
		t.Fatalf("second delete: status = %d body = %v, want 404 unsubscribe_not_found", status, body)
	}
	status, _, _ = apiCall(t, ts, http.MethodDelete, "/api/unsubscribes/not-a-number", admin, nil)
	if status != http.StatusBadRequest {
		t.Fatalf("bad id: status = %d, want 400", status)
	}

	if yes, _ := st.IsUnsubscribed(t.Context(), app.ID, "user@example.com"); yes {
		t.Fatal("row still present after DELETE")
	}
}

func TestListUnsubscribesFilters(t *testing.T) {
	st, ts := startAPI(t, nil)
	viewer := seedToken(t, st, "view", store.RoleViewer)
	gitea := seedApp(t, st, "gitea")
	kanboard := seedApp(t, st, "kanboard")
	ctx := t.Context()
	for _, tc := range []struct {
		appID int64
		email string
	}{{gitea.ID, "a@example.com"}, {gitea.ID, "b@example.com"}, {kanboard.ID, "c@example.com"}} {
		if _, _, err := st.CreateUnsubscribe(ctx, tc.appID, tc.email, store.SourceAPI, nil); err != nil {
			t.Fatal(err)
		}
	}

	status, body, _ := apiCall(t, ts, http.MethodGet, "/api/unsubscribes?app=gitea", viewer, nil)
	if status != http.StatusOK {
		t.Fatalf("list: status = %d", status)
	}
	rows := bodySlice(t, body, "unsubscribes")
	if len(rows) != 2 {
		t.Fatalf("app filter returned %d rows, want 2: %v", len(rows), body)
	}

	status, body, _ = apiCall(t, ts, http.MethodGet, "/api/unsubscribes?email=A@Example.com", viewer, nil)
	if status != http.StatusOK {
		t.Fatalf("list by email: status = %d", status)
	}
	rows = bodySlice(t, body, "unsubscribes")
	if len(rows) != 1 || rows[0]["email"] != "a@example.com" {
		t.Fatalf("email filter returned %+v, want exactly a@example.com", rows)
	}

	status, body, _ = apiCall(t, ts, http.MethodGet, "/api/unsubscribes?limit=1", viewer, nil)
	if status != http.StatusOK {
		t.Fatalf("paged list: status = %d", status)
	}
	if got := body["next_cursor"].(string); got == "" {
		t.Fatal("expected next_cursor on a short page")
	}
}
