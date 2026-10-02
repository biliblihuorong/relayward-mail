package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func seedToken(t *testing.T, s *Store, name, role string) *AdminToken {
	t.Helper()
	tok := &AdminToken{Name: name, Role: role, TokenHash: HashToken("raw-" + name)}
	if err := s.CreateAdminToken(context.Background(), tok, nil); err != nil {
		t.Fatalf("seed token %s: %v", name, err)
	}
	return tok
}

func TestCreateAppRevivesSoftDeleted(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	app := seedApp(t, s, "gitea")

	if err := s.SoftDeleteApp(ctx, "gitea", nil); err != nil {
		t.Fatalf("SoftDeleteApp: %v", err)
	}
	if _, err := s.GetAppByName(ctx, "gitea"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted app still resolvable: %v", err)
	}

	replacement := &App{
		Name:         "gitea",
		PasswordHash: app.PasswordHash,
		Enabled:      true,
		Unsubscribe:  false,
		AllowedFrom:  []string{"new@example.com"},
		RatePerHour:  10,
	}
	if err := s.CreateApp(ctx, replacement, nil); err != nil {
		t.Fatalf("recreate app: %v", err)
	}
	if replacement.ID != app.ID {
		t.Errorf("revived row should keep the original id: got %d, want %d", replacement.ID, app.ID)
	}
	got, err := s.GetAppByName(ctx, "gitea")
	if err != nil {
		t.Fatalf("GetAppByName: %v", err)
	}
	if got.RatePerHour != 10 || got.Unsubscribe {
		t.Errorf("revived app fields not updated: %+v", got)
	}

	// A still-active name must keep conflicting.
	active := &App{Name: "gitea", PasswordHash: "h", AllowedFrom: []string{"a@b.com"}}
	if err := s.CreateApp(ctx, active, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestUpdateApp(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	seedApp(t, s, "gitea")

	enabled := false
	rate := 42
	display := "Gitea"
	updated, err := s.UpdateApp(ctx, "gitea", AppUpdate{
		Enabled:     &enabled,
		RatePerHour: &rate,
		DisplayName: &display,
	}, nil)
	if err != nil {
		t.Fatalf("UpdateApp: %v", err)
	}
	if updated.Enabled || updated.RatePerHour != 42 || updated.DisplayName != "Gitea" {
		t.Errorf("unexpected update result: %+v", updated)
	}
	if !updated.Unsubscribe || updated.AllowedFrom[0] != "NoReply@example.com" {
		t.Errorf("untouched fields changed: %+v", updated)
	}

	bad := AppUpdate{AllowedFrom: []string{}}
	if _, err := s.UpdateApp(ctx, "gitea", bad, nil); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("empty allowed_from err = %v, want ErrInvalidInput", err)
	}
	zero := 0
	if _, err := s.UpdateApp(ctx, "gitea", AppUpdate{RatePerHour: &zero}, nil); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("zero rate err = %v, want ErrInvalidInput", err)
	}
	if _, err := s.UpdateApp(ctx, "ghost", AppUpdate{}, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing app err = %v, want ErrNotFound", err)
	}
}

func TestRotateAppPassword(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	seedApp(t, s, "gitea")

	if err := s.RotateAppPassword(ctx, "gitea", "new-hash", nil); err != nil {
		t.Fatalf("RotateAppPassword: %v", err)
	}
	app, err := s.GetAppByName(ctx, "gitea")
	if err != nil {
		t.Fatal(err)
	}
	if app.PasswordHash != "new-hash" {
		t.Errorf("hash not rotated: %q", app.PasswordHash)
	}
	if err := s.RotateAppPassword(ctx, "ghost", "x", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing app err = %v", err)
	}
}

func TestSoftDeleteAppNotFound(t *testing.T) {
	s := openTestStore(t)
	if err := s.SoftDeleteApp(context.Background(), "ghost", nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestListAppsExcludesDeleted(t *testing.T) {
	s := openTestStore(t)
	seedApp(t, s, "a-app")
	seedApp(t, s, "b-app")
	seedApp(t, s, "gone")
	if err := s.SoftDeleteApp(context.Background(), "gone", nil); err != nil {
		t.Fatal(err)
	}

	apps, err := s.ListApps(context.Background())
	if err != nil {
		t.Fatalf("ListApps: %v", err)
	}
	if len(apps) != 2 || apps[0].Name != "a-app" || apps[1].Name != "b-app" {
		t.Errorf("apps = %v", apps)
	}
}

func TestListMessagesFiltersAndCursor(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	appA := seedApp(t, s, "alpha")
	appB := seedApp(t, s, "beta")

	now := time.Now().UTC()
	for i := 0; i < 5; i++ {
		msg := &Message{AppID: appA.ID, MailFrom: "a@b.com", RcptTo: "x@y.com", Status: StatusSent, Ts: now}
		if err := s.InsertMessage(ctx, msg); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.InsertMessage(ctx, &Message{AppID: appB.ID, MailFrom: "a@b.com", RcptTo: "z@y.com", Status: StatusFailed, Ts: now}); err != nil {
		t.Fatal(err)
	}

	// Filter by app.
	page, next, err := s.ListMessages(ctx, MessageFilter{App: "beta"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 || page[0].Status != StatusFailed || page[0].AppName != "beta" {
		t.Errorf("app filter result = %+v", page)
	}
	if next != "" {
		t.Errorf("single row should have no next cursor, got %q", next)
	}

	// Filter by recipient and status.
	page, _, err = s.ListMessages(ctx, MessageFilter{To: "X@Y.COM"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 5 {
		t.Errorf("to filter result = %d rows, want 5", len(page))
	}
	page, _, err = s.ListMessages(ctx, MessageFilter{Status: StatusFailed})
	if err != nil || len(page) != 1 {
		t.Fatalf("status filter = %d rows, %v", len(page), err)
	}

	// Pagination walks back through all rows.
	var got []int64
	cursor := ""
	for {
		page, next, err = s.ListMessages(ctx, MessageFilter{Limit: 2, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range page {
			got = append(got, m.ID)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(got) != 6 {
		t.Fatalf("paged %d rows, want 6", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i] >= got[i-1] {
			t.Fatalf("rows not in descending id order: %v", got)
		}
	}

	if _, _, err := s.ListMessages(ctx, MessageFilter{Cursor: "not-a-number"}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("bad cursor err = %v, want ErrInvalidInput", err)
	}
	if _, _, err := s.ListMessages(ctx, MessageFilter{Limit: -5}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("bad limit err = %v, want ErrInvalidInput", err)
	}
	if _, _, err := s.ListMessages(ctx, MessageFilter{Status: "bogus"}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("bad status err = %v, want ErrInvalidInput", err)
	}
}

func TestAppStats(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	appA := seedApp(t, s, "alpha")
	seedApp(t, s, "beta") // no messages

	insert := func(appID int64, status string) {
		t.Helper()
		if err := s.InsertMessage(ctx, &Message{AppID: appID, MailFrom: "a@b.com", RcptTo: "x@y.com", Status: status}); err != nil {
			t.Fatal(err)
		}
	}
	insert(appA.ID, StatusSent)
	insert(appA.ID, StatusSent)
	insert(appA.ID, StatusFailed)
	insert(appA.ID, StatusRateLimited)
	insert(appA.ID, StatusSuppressed)

	stats, err := s.AppStats(ctx)
	if err != nil {
		t.Fatalf("AppStats: %v", err)
	}
	if len(stats) != 2 || stats[0].App != "alpha" || stats[1].App != "beta" {
		t.Fatalf("stats = %+v", stats)
	}
	a := stats[0]
	if a.Total != 5 || a.Sent != 2 || a.Failed != 1 || a.RateLimited != 1 || a.Suppressed != 1 {
		t.Errorf("alpha stats = %+v", a)
	}
	if stats[1].Total != 0 {
		t.Errorf("beta stats = %+v", stats[1])
	}

	byName, err := s.AppStatsByName(ctx, "alpha")
	if err != nil || byName.Total != 5 {
		t.Fatalf("AppStatsByName = %+v, %v", byName, err)
	}
	if _, err := s.AppStatsByName(ctx, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing app err = %v", err)
	}
}

func TestTokenListAndDelete(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	first := seedToken(t, s, "initial", RoleAdmin)
	seedToken(t, s, "ci", RoleOperator)

	tokens, err := s.ListAdminTokens(ctx)
	if err != nil {
		t.Fatalf("ListAdminTokens: %v", err)
	}
	if len(tokens) != 2 || tokens[0].Name != "initial" || tokens[1].Name != "ci" {
		t.Fatalf("tokens = %+v", tokens)
	}

	byID, err := s.GetAdminTokenByID(ctx, first.ID)
	if err != nil || byID.Name != "initial" {
		t.Fatalf("GetAdminTokenByID = %+v, %v", byID, err)
	}
	if _, err := s.GetAdminTokenByID(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing token err = %v", err)
	}

	if err := s.DeleteAdminToken(ctx, first.ID, nil); err != nil {
		t.Fatalf("DeleteAdminToken: %v", err)
	}
	if err := s.DeleteAdminToken(ctx, first.ID, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("double delete err = %v, want ErrNotFound", err)
	}
	if _, err := s.GetAdminTokenByHash(ctx, first.TokenHash); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoked token still resolvable: %v", err)
	}
}

func TestResetAdminTokens(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	seedToken(t, s, "initial", RoleAdmin)
	seedToken(t, s, "ci", RoleOperator)

	n, err := s.ResetAdminTokens(ctx, nil)
	if err != nil || n != 2 {
		t.Fatalf("ResetAdminTokens = %d, %v", n, err)
	}
	has, err := s.HasAdminToken(ctx)
	if err != nil || has {
		t.Fatalf("HasAdminToken = %v, %v; want false", has, err)
	}
}

func TestAuditLogWrites(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	seedApp(t, s, "gitea")
	tok := seedToken(t, s, "admin-tok", RoleAdmin)

	// Audit entries commit together with their writes.
	if err := s.RotateAppPassword(ctx, "gitea", "new-hash", &AuditEntry{
		TokenID: &tok.ID, TokenName: tok.Name, IP: "10.1.1.1",
		Action: ActionAppRotate, Target: "gitea", Detail: `{"via":"api"}`,
	}); err != nil {
		t.Fatalf("RotateAppPassword with audit: %v", err)
	}

	page, next, err := s.ListAuditLogs(ctx, AuditFilter{Limit: 10})
	if err != nil {
		t.Fatalf("ListAuditLogs: %v", err)
	}
	if len(page) != 1 || next != "" {
		t.Fatalf("audit page = %+v, next %q", page, next)
	}
	e := page[0]
	if e.Action != ActionAppRotate || e.Target == nil || *e.Target != "gitea" {
		t.Errorf("entry = %+v", e)
	}
	if e.TokenName == nil || *e.TokenName != tok.Name {
		t.Errorf("token name missing: %+v", e)
	}
	if e.IP == nil || *e.IP != "10.1.1.1" {
		t.Errorf("ip missing: %+v", e)
	}
	if e.Detail == nil || *e.Detail != `{"via":"api"}` {
		t.Errorf("detail = %+v", e.Detail)
	}
	if e.TokenID == nil || *e.TokenID != tok.ID {
		t.Errorf("token id missing: %+v", e)
	}

	// Deletion keeps the recorded name (the reason for the token_name column).
	if err := s.DeleteAdminToken(ctx, tok.ID, &AuditEntry{
		TokenID: &tok.ID, TokenName: tok.Name, IP: "10.1.1.1",
		Action: ActionTokenRevoke, Target: tok.Name,
	}); err != nil {
		t.Fatal(err)
	}
	page, _, _ = s.ListAuditLogs(ctx, AuditFilter{})
	if len(page) != 2 {
		t.Fatalf("audit rows = %d, want 2", len(page))
	}
	for _, e := range page {
		if e.TokenName == nil || *e.TokenName != tok.Name {
			t.Errorf("revoked token lost its audit name: %+v", e)
		}
	}
}
