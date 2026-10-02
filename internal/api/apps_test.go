package api

import (
	"errors"
	"net/http"
	"testing"

	"relayward-mail/internal/ratelimit"
	"relayward-mail/internal/store"
)

// ---- small decoding helpers shared by the api test files ----

// str reads a string field from a decoded JSON object.
func str(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

// num reads a numeric field from a decoded JSON object.
func num(m map[string]any, key string) float64 {
	v, _ := m[key].(float64)
	return v
}

// boolField reads a boolean field from a decoded JSON object.
func boolField(m map[string]any, key string) bool {
	v, _ := m[key].(bool)
	return v
}

// strSlice reads a []string field from a decoded JSON object.
func strSlice(t *testing.T, m map[string]any, key string) []string {
	t.Helper()
	raw, ok := m[key].([]any)
	if !ok {
		if _, present := m[key]; !present {
			return nil
		}
		t.Fatalf("%s = %T, want array", key, m[key])
	}
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			t.Fatalf("%s element = %T, want string", key, v)
		}
		out = append(out, s)
	}
	return out
}

// bodyMap reads a nested object from a decoded JSON body.
func bodyMap(t *testing.T, body map[string]any, key string) map[string]any {
	t.Helper()
	m, ok := body[key].(map[string]any)
	if !ok {
		t.Fatalf("body %v: %s = %T, want object", body, key, body[key])
	}
	return m
}

// bodySlice reads a nested array of objects from a decoded JSON body.
func bodySlice(t *testing.T, body map[string]any, key string) []map[string]any {
	t.Helper()
	raw, ok := body[key].([]any)
	if !ok {
		t.Fatalf("body %v: %s = %T, want array", body, key, body[key])
	}
	out := make([]map[string]any, 0, len(raw))
	for _, v := range raw {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("%s element = %T, want object", key, v)
		}
		out = append(out, m)
	}
	return out
}

// wantError asserts the unified error shape for an expected status and code.
func wantError(t *testing.T, status int, body map[string]any, wantStatus int, wantCode string) {
	t.Helper()
	if status != wantStatus {
		t.Errorf("status = %d, want %d (body %v)", status, wantStatus, body)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf("body %v carries no error object", body)
	}
	if got := str(errObj, "code"); got != wantCode {
		t.Errorf("error.code = %q, want %q", got, wantCode)
	}
	if str(errObj, "message") == "" {
		t.Errorf("error.message empty in %v", body)
	}
}

// createAppBody builds a valid POST /api/apps body for name.
func createAppBody(name string) map[string]any {
	return map[string]any{
		"name":         name,
		"allowed_from": []string{"NoReply@example.com"},
		"display_name": "App " + name,
		"unsubscribe":  true,
	}
}

// ---- endpoint tests ----

// TestCreateApp checks the happy path: 201, a one-time plaintext password that
// verifies against the stored hash, and a payload that never leaks the hash.
func TestCreateApp(t *testing.T) {
	st, ts := startAPI(t, nil)
	admin := seedToken(t, st, "admin", store.RoleAdmin)

	status, body, _ := apiCall(t, ts, http.MethodPost, "/api/apps", admin, map[string]any{
		"name":          "gitea",
		"allowed_from":  []string{"NoReply@example.com"},
		"rate_per_hour": 500,
		"display_name":  "Gitea",
		"unsubscribe":   true,
	})
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %v)", status, body)
	}
	password := str(body, "smtp_password")
	if password == "" {
		t.Fatal("smtp_password empty")
	}

	app := bodyMap(t, body, "app")
	if got := str(app, "name"); got != "gitea" {
		t.Errorf("name = %q, want gitea", got)
	}
	if got := str(app, "display_name"); got != "Gitea" {
		t.Errorf("display_name = %q, want Gitea", got)
	}
	if !boolField(app, "enabled") {
		t.Errorf("enabled = %v, want true", app["enabled"])
	}
	if !boolField(app, "unsubscribe") {
		t.Errorf("unsubscribe = %v, want true", app["unsubscribe"])
	}
	if got := strSlice(t, app, "allowed_from"); len(got) != 1 || got[0] != "NoReply@example.com" {
		t.Errorf("allowed_from = %v, want [NoReply@example.com]", got)
	}
	if got := num(app, "rate_per_hour"); got != 500 {
		t.Errorf("rate_per_hour = %v, want 500", got)
	}
	if str(app, "created_at") == "" || str(app, "updated_at") == "" {
		t.Errorf("timestamps missing: %v", app)
	}
	if _, present := app["password_hash"]; present {
		t.Errorf("app payload leaks password_hash: %v", app)
	}

	stored, err := st.GetAppByName(t.Context(), "gitea")
	if err != nil {
		t.Fatalf("load app: %v", err)
	}
	ok, err := store.VerifyPassword(stored.PasswordHash, password)
	if err != nil {
		t.Fatalf("verify password: %v", err)
	}
	if !ok {
		t.Error("returned smtp_password does not verify against the stored hash")
	}
}

// TestCreateAppDefaults checks that an omitted rate_per_hour falls back to 500
// and that enabled/unsubscribe default to true.
func TestCreateAppDefaults(t *testing.T) {
	st, ts := startAPI(t, nil)
	admin := seedToken(t, st, "admin", store.RoleAdmin)

	status, body, _ := apiCall(t, ts, http.MethodPost, "/api/apps", admin, map[string]any{
		"name":         "defaults",
		"allowed_from": []string{"NoReply@example.com"},
	})
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %v)", status, body)
	}
	app := bodyMap(t, body, "app")
	if got := num(app, "rate_per_hour"); got != 500 {
		t.Errorf("rate_per_hour = %v, want the default 500", got)
	}
	if !boolField(app, "enabled") {
		t.Errorf("enabled = %v, want true", app["enabled"])
	}
	if !boolField(app, "unsubscribe") {
		t.Errorf("unsubscribe = %v, want true", app["unsubscribe"])
	}
}

// TestCreateAppValidation checks the 400 paths for malformed create bodies.
func TestCreateAppValidation(t *testing.T) {
	st, ts := startAPI(t, nil)
	admin := seedToken(t, st, "admin", store.RoleAdmin)

	tests := []struct {
		name string
		body map[string]any
	}{
		{"missing name", map[string]any{"allowed_from": []string{"a@example.com"}}},
		{"missing allowed_from", map[string]any{"name": "lonely"}},
		{"empty allowed_from", map[string]any{"name": "hollow", "allowed_from": []string{}}},
		{"zero rate", map[string]any{"name": "free", "allowed_from": []string{"a@example.com"}, "rate_per_hour": 0}},
		{"negative rate", map[string]any{"name": "debt", "allowed_from": []string{"a@example.com"}, "rate_per_hour": -5}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, body, _ := apiCall(t, ts, http.MethodPost, "/api/apps", admin, tc.body)
			wantError(t, status, body, http.StatusBadRequest, "invalid_request")
		})
	}
}

// TestCreateAppConflict checks that a duplicate name is a 409.
func TestCreateAppConflict(t *testing.T) {
	st, ts := startAPI(t, nil)
	seedApp(t, st, "alpha")
	admin := seedToken(t, st, "admin", store.RoleAdmin)

	status, body, _ := apiCall(t, ts, http.MethodPost, "/api/apps", admin, createAppBody("alpha"))
	wantError(t, status, body, http.StatusConflict, "app_exists")
}

// TestCreateAppForbiddenForViewer checks that viewers cannot create apps.
func TestCreateAppForbiddenForViewer(t *testing.T) {
	st, ts := startAPI(t, nil)
	viewer := seedToken(t, st, "view", store.RoleViewer)

	status, body, _ := apiCall(t, ts, http.MethodPost, "/api/apps", viewer, createAppBody("nope"))
	wantError(t, status, body, http.StatusForbidden, "forbidden")

	if _, err := st.GetAppByName(t.Context(), "nope"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("app must not have been created: %v", err)
	}
}

// TestCreateAppAsOperator checks that operators may create apps.
func TestCreateAppAsOperator(t *testing.T) {
	st, ts := startAPI(t, nil)
	op := seedToken(t, st, "op", store.RoleOperator)

	status, body, _ := apiCall(t, ts, http.MethodPost, "/api/apps", op, createAppBody("opapp"))
	if status != http.StatusCreated {
		t.Fatalf("status = %d, want 201 (body %v)", status, body)
	}
	if got := str(bodyMap(t, body, "app"), "name"); got != "opapp" {
		t.Errorf("name = %q, want opapp", got)
	}
}

// TestListAppsAsViewer checks that viewers can list apps and that the entries
// never carry a password hash.
func TestListAppsAsViewer(t *testing.T) {
	st, ts := startAPI(t, nil)
	seedApp(t, st, "alpha")
	seedApp(t, st, "beta")
	viewer := seedToken(t, st, "view", store.RoleViewer)

	status, body, _ := apiCall(t, ts, http.MethodGet, "/api/apps", viewer, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", status, body)
	}
	apps := bodySlice(t, body, "apps")
	if len(apps) != 2 {
		t.Fatalf("got %d apps, want 2: %v", len(apps), body)
	}
	names := map[string]bool{}
	for _, app := range apps {
		if _, present := app["password_hash"]; present {
			t.Errorf("app list leaks password_hash: %v", app)
		}
		names[str(app, "name")] = true
	}
	if !names["alpha"] || !names["beta"] {
		t.Errorf("app list = %v, want alpha and beta", names)
	}
}

// TestGetApp checks the single-app view, including its stats block.
func TestGetApp(t *testing.T) {
	st, ts := startAPI(t, nil)
	alpha := seedApp(t, st, "alpha")
	for i := 0; i < 2; i++ {
		if err := st.InsertMessage(t.Context(), &store.Message{
			AppID:    alpha.ID,
			MailFrom: "NoReply@example.com",
			RcptTo:   "user@example.com",
			Status:   store.StatusSent,
		}); err != nil {
			t.Fatalf("insert message: %v", err)
		}
	}
	viewer := seedToken(t, st, "view", store.RoleViewer)

	status, body, _ := apiCall(t, ts, http.MethodGet, "/api/apps/alpha", viewer, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", status, body)
	}
	app := bodyMap(t, body, "app")
	if got := str(app, "name"); got != "alpha" {
		t.Errorf("name = %q, want alpha", got)
	}
	if _, present := app["password_hash"]; present {
		t.Errorf("app payload leaks password_hash: %v", app)
	}
	stats := bodyMap(t, body, "stats")
	if got := num(stats, "total"); got != 2 {
		t.Errorf("stats.total = %v, want 2", got)
	}
	if got := num(stats, "sent"); got != 2 {
		t.Errorf("stats.sent = %v, want 2", got)
	}
	if got := str(stats, "app"); got != "alpha" {
		t.Errorf("stats.app = %q, want alpha", got)
	}
}

// TestGetAppUnknown checks the 404 for an unknown app name.
func TestGetAppUnknown(t *testing.T) {
	st, ts := startAPI(t, nil)
	viewer := seedToken(t, st, "view", store.RoleViewer)

	status, body, _ := apiCall(t, ts, http.MethodGet, "/api/apps/ghost", viewer, nil)
	wantError(t, status, body, http.StatusNotFound, "app_not_found")
}

// TestPatchApp checks the full and partial PATCH flows against the store.
func TestPatchApp(t *testing.T) {
	st, ts := startAPI(t, nil)
	seedApp(t, st, "alpha")
	op := seedToken(t, st, "op", store.RoleOperator)

	status, body, _ := apiCall(t, ts, http.MethodPatch, "/api/apps/alpha", op, map[string]any{
		"enabled":       false,
		"unsubscribe":   false,
		"rate_per_hour": 100,
		"allowed_from":  []string{"Bot@example.com"},
		"display_name":  "Alpha Mail",
	})
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", status, body)
	}
	app := bodyMap(t, body, "app")
	if boolField(app, "enabled") {
		t.Errorf("enabled = %v, want false", app["enabled"])
	}
	if boolField(app, "unsubscribe") {
		t.Errorf("unsubscribe = %v, want false", app["unsubscribe"])
	}
	if got := num(app, "rate_per_hour"); got != 100 {
		t.Errorf("rate_per_hour = %v, want 100", got)
	}
	if got := str(app, "display_name"); got != "Alpha Mail" {
		t.Errorf("display_name = %q, want Alpha Mail", got)
	}
	if got := strSlice(t, app, "allowed_from"); len(got) != 1 || got[0] != "Bot@example.com" {
		t.Errorf("allowed_from = %v, want [Bot@example.com]", got)
	}

	stored, err := st.GetAppByName(t.Context(), "alpha")
	if err != nil {
		t.Fatalf("load app: %v", err)
	}
	if stored.Enabled || stored.Unsubscribe || stored.RatePerHour != 100 || stored.DisplayName != "Alpha Mail" {
		t.Errorf("stored app = %+v, want patched values", stored)
	}
	if len(stored.AllowedFrom) != 1 || stored.AllowedFrom[0] != "Bot@example.com" {
		t.Errorf("stored allowed_from = %v, want [Bot@example.com]", stored.AllowedFrom)
	}

	// A partial patch changes only the given field.
	status, body, _ = apiCall(t, ts, http.MethodPatch, "/api/apps/alpha", op, map[string]any{
		"display_name": "Zeta",
	})
	if status != http.StatusOK {
		t.Fatalf("partial patch: status = %d, want 200 (body %v)", status, body)
	}
	app = bodyMap(t, body, "app")
	if got := str(app, "display_name"); got != "Zeta" {
		t.Errorf("display_name = %q, want Zeta", got)
	}
	if boolField(app, "enabled") {
		t.Error("partial patch changed enabled")
	}
	if got := num(app, "rate_per_hour"); got != 100 {
		t.Errorf("partial patch changed rate_per_hour to %v", got)
	}

	stored, err = st.GetAppByName(t.Context(), "alpha")
	if err != nil {
		t.Fatalf("reload app: %v", err)
	}
	if stored.DisplayName != "Zeta" || stored.Enabled || stored.RatePerHour != 100 {
		t.Errorf("stored app after partial patch = %+v", stored)
	}
}

// TestPatchAppValidation checks the 400 and 404 paths for PATCH.
func TestPatchAppValidation(t *testing.T) {
	st, ts := startAPI(t, nil)
	seedApp(t, st, "alpha")
	op := seedToken(t, st, "op", store.RoleOperator)

	tests := []struct {
		name string
		path string
		body map[string]any
		want int
		code string
	}{
		{"empty allowed_from", "/api/apps/alpha", map[string]any{"allowed_from": []string{}}, http.StatusBadRequest, "invalid_request"},
		{"zero rate", "/api/apps/alpha", map[string]any{"rate_per_hour": 0}, http.StatusBadRequest, "invalid_request"},
		{"unknown app", "/api/apps/ghost", map[string]any{"display_name": "X"}, http.StatusNotFound, "app_not_found"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			status, body, _ := apiCall(t, ts, http.MethodPatch, tc.path, op, tc.body)
			wantError(t, status, body, tc.want, tc.code)
		})
	}
}

// TestRotateApp checks that rotation invalidates the old password and issues
// a working new one.
func TestRotateApp(t *testing.T) {
	st, ts := startAPI(t, nil)
	seedApp(t, st, "alpha") // password "pw-alpha"
	op := seedToken(t, st, "op", store.RoleOperator)

	status, body, _ := apiCall(t, ts, http.MethodPost, "/api/apps/alpha/rotate", op, nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %v)", status, body)
	}
	newPassword := str(body, "smtp_password")
	if newPassword == "" {
		t.Fatal("smtp_password empty")
	}

	stored, err := st.GetAppByName(t.Context(), "alpha")
	if err != nil {
		t.Fatalf("load app: %v", err)
	}
	oldOK, err := store.VerifyPassword(stored.PasswordHash, "pw-alpha")
	if err != nil {
		t.Fatalf("verify old password: %v", err)
	}
	if oldOK {
		t.Error("old password still verifies after rotate")
	}
	newOK, err := store.VerifyPassword(stored.PasswordHash, newPassword)
	if err != nil {
		t.Fatalf("verify new password: %v", err)
	}
	if !newOK {
		t.Error("new password does not verify after rotate")
	}
}

// TestRotateAppUnknown checks the 404 for rotating an unknown app.
func TestRotateAppUnknown(t *testing.T) {
	st, ts := startAPI(t, nil)
	op := seedToken(t, st, "op", store.RoleOperator)

	status, body, _ := apiCall(t, ts, http.MethodPost, "/api/apps/ghost/rotate", op, nil)
	wantError(t, status, body, http.StatusNotFound, "app_not_found")
}

// TestDeleteApp checks the 204, the soft-delete behaviour and the repeat 404.
func TestDeleteApp(t *testing.T) {
	st, ts := startAPI(t, nil)
	seedApp(t, st, "alpha")
	op := seedToken(t, st, "op", store.RoleOperator)
	viewer := seedToken(t, st, "view", store.RoleViewer)

	status, body, _ := apiCall(t, ts, http.MethodDelete, "/api/apps/alpha", op, nil)
	if status != http.StatusNoContent {
		t.Fatalf("status = %d, want 204 (body %v)", status, body)
	}
	if len(body) != 0 {
		t.Errorf("204 body = %v, want empty", body)
	}

	status, body, _ = apiCall(t, ts, http.MethodGet, "/api/apps/alpha", viewer, nil)
	wantError(t, status, body, http.StatusNotFound, "app_not_found")

	status, body, _ = apiCall(t, ts, http.MethodGet, "/api/apps", viewer, nil)
	if status != http.StatusOK {
		t.Fatalf("list after delete: status = %d, want 200", status)
	}
	if apps := bodySlice(t, body, "apps"); len(apps) != 0 {
		t.Errorf("deleted app still listed: %v", body)
	}

	// A soft-deleted app is gone for good: deleting again is a 404.
	status, body, _ = apiCall(t, ts, http.MethodDelete, "/api/apps/alpha", op, nil)
	wantError(t, status, body, http.StatusNotFound, "app_not_found")

	if _, err := st.GetAppByName(t.Context(), "alpha"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetAppByName after delete: %v, want ErrNotFound", err)
	}
}

func TestPatchKeepsRateBucketUnlessRateChanges(t *testing.T) {
	limiter := ratelimit.NewLimiter()
	st, ts := startAPI(t, func(o *Options) { o.Limiter = limiter })
	app := seedApp(t, st, "gitea")
	op := seedToken(t, st, "op", store.RoleOperator)

	for range 500 {
		if !limiter.Allow(app.ID, 500) {
			t.Fatal("bucket drained early")
		}
	}
	if limiter.Allow(app.ID, 500) {
		t.Fatal("bucket should be empty")
	}

	if code, _, _ := apiCall(t, ts, http.MethodPatch, "/api/apps/gitea", op, map[string]any{"display_name": "Gitea"}); code != http.StatusOK {
		t.Fatalf("patch status = %d", code)
	}
	if limiter.Allow(app.ID, 500) {
		t.Fatal("patching an unrelated field must not refill the bucket")
	}

	if code, _, _ := apiCall(t, ts, http.MethodPatch, "/api/apps/gitea", op, map[string]any{"rate_per_hour": 10}); code != http.StatusOK {
		t.Fatalf("patch status = %d", code)
	}
	if !limiter.Allow(app.ID, 10) {
		t.Fatal("changing the rate should install a fresh bucket")
	}
}
