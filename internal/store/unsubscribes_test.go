package store

import (
	"errors"
	"testing"
)

func TestCreateUnsubscribeIdempotent(t *testing.T) {
	s := openTestStore(t)
	app := seedApp(t, s, "gitea")
	ctx := t.Context()

	row, created, err := s.CreateUnsubscribe(ctx, app.ID, "user@example.com", SourceLink, nil)
	if err != nil || !created {
		t.Fatalf("first create: created=%v err=%v", created, err)
	}
	if row.ID == 0 || row.Email != "user@example.com" || row.Source != SourceLink || row.Ts.IsZero() {
		t.Fatalf("unexpected row: %+v", row)
	}

	dup, created, err := s.CreateUnsubscribe(ctx, app.ID, "user@example.com", SourceAPI, nil)
	if err != nil {
		t.Fatalf("second create: %v", err)
	}
	if created {
		t.Fatal("duplicate create reported created=true")
	}
	if dup.ID != row.ID || dup.Source != SourceLink {
		t.Fatalf("duplicate changed the row: got %+v, want id=%d source=%s", dup, row.ID, row.Source)
	}
}

func TestCreateUnsubscribeValidation(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()

	cases := []struct {
		name   string
		appID  int64
		email  string
		source string
	}{
		{"zero app", 0, "a@b.c", SourceAPI},
		{"empty email", 1, "", SourceAPI},
		{"unknown source", 1, "a@b.c", "carrier_pigeon"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := s.CreateUnsubscribe(ctx, tc.appID, tc.email, tc.source, nil); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("err = %v, want ErrInvalidInput", err)
			}
		})
	}
}

func TestIsUnsubscribed(t *testing.T) {
	s := openTestStore(t)
	app := seedApp(t, s, "gitea")
	other := seedApp(t, s, "kanboard")
	ctx := t.Context()

	if yes, err := s.IsUnsubscribed(ctx, app.ID, "user@example.com"); err != nil || yes {
		t.Fatalf("before create: yes=%v err=%v", yes, err)
	}
	if _, _, err := s.CreateUnsubscribe(ctx, app.ID, "user@example.com", SourceOneClick, nil); err != nil {
		t.Fatal(err)
	}

	yes, err := s.IsUnsubscribed(ctx, app.ID, "user@example.com")
	if err != nil || !yes {
		t.Fatalf("after create: yes=%v err=%v", yes, err)
	}
	// Unsubscribe is per app: other apps keep delivering.
	if yes, err := s.IsUnsubscribed(ctx, other.ID, "user@example.com"); err != nil || yes {
		t.Fatalf("other app: yes=%v err=%v", yes, err)
	}
	if yes, err := s.IsUnsubscribed(ctx, app.ID, "other@example.com"); err != nil || yes {
		t.Fatalf("other email: yes=%v err=%v", yes, err)
	}
}

func TestListUnsubscribesFiltersAndCursor(t *testing.T) {
	s := openTestStore(t)
	gitea := seedApp(t, s, "gitea")
	kanboard := seedApp(t, s, "kanboard")
	ctx := t.Context()

	for _, tc := range []struct {
		appID  int64
		email  string
		source string
	}{
		{gitea.ID, "a@example.com", SourceLink},
		{gitea.ID, "b@example.com", SourceAPI},
		{kanboard.ID, "c@example.com", SourceOneClick},
	} {
		if _, _, err := s.CreateUnsubscribe(ctx, tc.appID, tc.email, tc.source, nil); err != nil {
			t.Fatal(err)
		}
	}

	page, next, err := s.ListUnsubscribes(ctx, UnsubscribeFilter{Limit: 2})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(page) != 2 || next == "" {
		t.Fatalf("page = %+v next = %q, want 2 rows and a cursor", page, next)
	}
	if page[0].Email != "c@example.com" {
		t.Fatalf("newest first violated: %+v", page)
	}

	page2, next2, err := s.ListUnsubscribes(ctx, UnsubscribeFilter{Limit: 2, Cursor: next})
	if err != nil {
		t.Fatalf("list page 2: %v", err)
	}
	if len(page2) != 1 || next2 != "" {
		t.Fatalf("page 2 = %+v next = %q, want last row and no cursor", page2, next2)
	}

	byApp, _, err := s.ListUnsubscribes(ctx, UnsubscribeFilter{App: "kanboard"})
	if err != nil || len(byApp) != 1 || byApp[0].AppName != "kanboard" {
		t.Fatalf("app filter: %+v err=%v", byApp, err)
	}
	byEmail, _, err := s.ListUnsubscribes(ctx, UnsubscribeFilter{Email: "B@Example.com"})
	if err != nil || len(byEmail) != 1 || byEmail[0].Email != "b@example.com" {
		t.Fatalf("email filter (normalized): %+v err=%v", byEmail, err)
	}
	if _, _, err := s.ListUnsubscribes(ctx, UnsubscribeFilter{Limit: -1}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("bad limit err = %v, want ErrInvalidInput", err)
	}
}

func TestDeleteUnsubscribeAudited(t *testing.T) {
	s := openTestStore(t)
	app := seedApp(t, s, "gitea")
	ctx := t.Context()

	if _, _, err := s.CreateUnsubscribe(ctx, app.ID, "user@example.com", SourceLink, &AuditEntry{
		TokenName: "public", IP: "203.0.113.9", Action: ActionUnsubscribeCreate, Target: "user@example.com",
	}); err != nil {
		t.Fatal(err)
	}
	row, err := s.GetUnsubscribe(ctx, app.ID, "user@example.com")
	if err != nil {
		t.Fatal(err)
	}

	if err := s.DeleteUnsubscribe(ctx, row.ID, &AuditEntry{
		TokenName: "admin", IP: "198.51.100.1", Action: ActionUnsubscribeDelete, Target: "user@example.com",
	}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if err := s.DeleteUnsubscribe(ctx, row.ID, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete err = %v, want ErrNotFound", err)
	}
	if yes, _ := s.IsUnsubscribed(ctx, app.ID, "user@example.com"); yes {
		t.Fatal("row still present after delete")
	}

	page, _, err := s.ListAuditLogs(ctx, AuditFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	actions := map[string]bool{}
	for _, e := range page {
		actions[e.Action] = true
	}
	if !actions[ActionUnsubscribeCreate] || !actions[ActionUnsubscribeDelete] {
		t.Fatalf("audit log missing unsubscribe actions: %+v", page)
	}
}

func TestGetUnsubscribeNotFound(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.GetUnsubscribe(t.Context(), 42, "nobody@example.com"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}
