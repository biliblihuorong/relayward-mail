package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "relayward.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOpenAppliesMigrations(t *testing.T) {
	path := filepath.Join(t.TempDir(), "relayward.db")

	s, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != 4 { // 0001_init + 0002_management + 0003_body_injection + 0004_sessions
		t.Fatalf("user_version = %d, want 4", version)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Reopening must be idempotent.
	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s2.Close()
	if err := s2.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
}

func seedApp(t *testing.T, s *Store, name string) *App {
	t.Helper()
	hash, err := HashPassword("pw-" + name)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{
		Name:         name,
		PasswordHash: hash,
		Enabled:      true,
		Unsubscribe:  true,
		AllowedFrom:  []string{"NoReply@example.com"},
		RatePerHour:  500,
		DisplayName:  "App " + name,
	}
	if err := s.CreateApp(context.Background(), app, nil); err != nil {
		t.Fatalf("CreateApp: %v", err)
	}
	return app
}

func TestAppRoundtrip(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	seeded := seedApp(t, s, "gitea")
	got, err := s.GetAppByName(ctx, "gitea")
	if err != nil {
		t.Fatalf("GetAppByName: %v", err)
	}
	if got.ID != seeded.ID || got.Name != "gitea" || !got.Enabled || !got.Unsubscribe {
		t.Errorf("unexpected app: %+v", got)
	}
	if got.DisplayName != "App gitea" || got.RatePerHour != 500 {
		t.Errorf("unexpected fields: %+v", got)
	}
	if len(got.AllowedFrom) != 1 || got.AllowedFrom[0] != "NoReply@example.com" {
		t.Errorf("AllowedFrom = %v", got.AllowedFrom)
	}
	ok, err := VerifyPassword(got.PasswordHash, "pw-gitea")
	if err != nil || !ok {
		t.Errorf("VerifyPassword = %v, %v", ok, err)
	}
}

func TestGetAppByNameNotFound(t *testing.T) {
	s := openTestStore(t)
	_, err := s.GetAppByName(context.Background(), "missing")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestCreateAppConflict(t *testing.T) {
	s := openTestStore(t)
	seedApp(t, s, "gitea")

	_, err := s.GetAppByName(context.Background(), "GITEA")
	if !errors.Is(err, ErrNotFound) {
		// Names are matched exactly (SMTP AUTH usernames are case-sensitive by
		// convention here); a differently cased name must be insertable.
		t.Fatalf("unexpected case-insensitive lookup: %v", err)
	}
	seedApp(t, s, "GITEA")

	hash, err := HashPassword("x")
	if err != nil {
		t.Fatal(err)
	}
	dup := &App{Name: "gitea", PasswordHash: hash, AllowedFrom: []string{"a@b.com"}}
	if err := s.CreateApp(context.Background(), dup, nil); !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestCreateAppValidation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	hash, err := HashPassword("x")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		app     *App
		wantErr bool
	}{
		{"valid", &App{Name: "app_1.x", PasswordHash: hash, AllowedFrom: []string{"a@b.com"}}, false},
		{"empty name", &App{Name: "", PasswordHash: hash, AllowedFrom: []string{"a@b.com"}}, true},
		{"spaces", &App{Name: "my app", PasswordHash: hash, AllowedFrom: []string{"a@b.com"}}, true},
		{"leading dash", &App{Name: "-app", PasswordHash: hash, AllowedFrom: []string{"a@b.com"}}, true},
		{"too long", &App{Name: strings.Repeat("a", 65), PasswordHash: hash, AllowedFrom: []string{"a@b.com"}}, true},
		{"no allowed_from", &App{Name: "valid", PasswordHash: hash}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := s.CreateApp(ctx, tt.app, nil)
			if (err != nil) != tt.wantErr {
				t.Fatalf("CreateApp() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestMessageLog(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	app := seedApp(t, s, "gitea")

	if ts, err := s.LastSuccessfulRelayTime(ctx); err != nil || ts != nil {
		t.Fatalf("LastSuccessfulRelayTime = %v, %v; want nil, nil", ts, err)
	}

	msg := &Message{
		AppID:     app.ID,
		MailFrom:  "NoReply@example.com",
		RcptTo:    "  User@Example.COM ",
		Subject:   "Welcome",
		Size:      1234,
		MessageID: "<m-1@example.com>",
		Status:    StatusSent,
		ClientIP:  "10.0.0.1",
	}
	if err := s.InsertMessage(ctx, msg); err != nil {
		t.Fatalf("InsertMessage: %v", err)
	}
	if msg.ID == 0 {
		t.Fatal("message ID not set")
	}

	last, err := s.LastSuccessfulRelayTime(ctx)
	if err != nil {
		t.Fatalf("LastSuccessfulRelayTime: %v", err)
	}
	if last == nil || time.Since(*last) > time.Minute {
		t.Errorf("LastSuccessfulRelayTime = %v", last)
	}

	failed := &Message{AppID: app.ID, MailFrom: "NoReply@example.com", RcptTo: "b@e.com", Status: StatusFailed}
	if err := s.InsertMessage(ctx, failed); err != nil {
		t.Fatalf("InsertMessage failed: %v", err)
	}
	// A failed message must not change the last success timestamp.
	last2, _ := s.LastSuccessfulRelayTime(ctx)
	if !last2.Equal(*last) {
		t.Errorf("last success moved: %v -> %v", last, last2)
	}
}

func TestPruneMessages(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	app := seedApp(t, s, "gitea")

	old := &Message{AppID: app.ID, MailFrom: "a@b.com", RcptTo: "x@y.com", Status: StatusSent, Ts: time.Now().AddDate(0, 0, -100)}
	if err := s.InsertMessage(ctx, old); err != nil {
		t.Fatal(err)
	}
	fresh := &Message{AppID: app.ID, MailFrom: "a@b.com", RcptTo: "z@y.com", Status: StatusSent}
	if err := s.InsertMessage(ctx, fresh); err != nil {
		t.Fatal(err)
	}

	n, err := s.PruneMessages(ctx, time.Now().AddDate(0, 0, -90))
	if err != nil {
		t.Fatalf("PruneMessages: %v", err)
	}
	if n != 1 {
		t.Fatalf("pruned %d rows, want 1", n)
	}

	remaining, err := s.LastSuccessfulRelayTime(ctx)
	if err != nil || remaining == nil {
		t.Fatalf("fresh row survived: %v, %v", remaining, err)
	}
	if !remaining.Equal(fresh.Ts.Truncate(time.Second)) {
		t.Errorf("remaining ts = %v, want %v", remaining, fresh.Ts)
	}
}

func TestAdminTokens(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	has, err := s.HasAdminToken(ctx)
	if err != nil || has {
		t.Fatalf("HasAdminToken = %v, %v; want false", has, err)
	}

	raw := "rw_admin_abcdef"
	tok := &AdminToken{Name: "initial", Role: RoleAdmin, TokenHash: HashToken(raw)}
	if err := s.CreateAdminToken(ctx, tok, nil); err != nil {
		t.Fatalf("CreateAdminToken: %v", err)
	}
	if tok.ID == 0 || tok.CreatedAt.IsZero() {
		t.Errorf("token not fully populated: %+v", tok)
	}

	has, err = s.HasAdminToken(ctx)
	if err != nil || !has {
		t.Fatalf("HasAdminToken = %v, %v; want true", has, err)
	}

	got, err := s.GetAdminTokenByHash(ctx, HashToken(raw))
	if err != nil {
		t.Fatalf("GetAdminTokenByHash: %v", err)
	}
	if got.Name != "initial" || got.Role != RoleAdmin {
		t.Errorf("unexpected token: %+v", got)
	}
	if got.LastUsed != nil {
		t.Errorf("LastUsed should start nil, got %v", got.LastUsed)
	}

	if err := s.TouchAdminToken(ctx, got.ID); err != nil {
		t.Fatalf("TouchAdminToken: %v", err)
	}
	used, err := s.GetAdminTokenByHash(ctx, got.TokenHash)
	if err != nil {
		t.Fatal(err)
	}
	if used.LastUsed == nil {
		t.Error("LastUsed not updated")
	}

	if _, err := s.GetAdminTokenByHash(ctx, HashToken("rw_admin_unknown")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown token err = %v, want ErrNotFound", err)
	}

	bad := &AdminToken{Name: "x", Role: "root", TokenHash: "h"}
	if err := s.CreateAdminToken(ctx, bad, nil); err == nil {
		t.Error("invalid role accepted")
	}
}

func TestPasswordHashing(t *testing.T) {
	hash, err := HashPassword("s3cret")
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	const wantPrefix = "$argon2id$v=19$m=19456,t=2,p=1$"
	if got := hash[:len(wantPrefix)]; got != wantPrefix {
		t.Errorf("hash prefix = %q, want %q", got, wantPrefix)
	}

	ok, err := VerifyPassword(hash, "s3cret")
	if err != nil || !ok {
		t.Errorf("correct password: ok=%v err=%v", ok, err)
	}
	ok, err = VerifyPassword(hash, "wrong")
	if err != nil || ok {
		t.Errorf("wrong password: ok=%v err=%v", ok, err)
	}
	if _, err := VerifyPassword("$argon2id$garbage", "x"); err == nil {
		t.Error("malformed hash accepted")
	}

	h1, _ := HashPassword("same")
	h2, _ := HashPassword("same")
	if h1 == h2 {
		t.Error("salts are not random")
	}
}

func TestHashTokenAndRandom(t *testing.T) {
	if got, want := HashToken("abc"), HashToken("abc"); got != want || len(got) != 64 {
		t.Errorf("HashToken = %q", got)
	}
	if HashToken("abc") == HashToken("abd") {
		t.Error("HashToken collides")
	}

	tok, err := RandomToken("rw_admin_", 32)
	if err != nil {
		t.Fatalf("RandomToken: %v", err)
	}
	if len(tok) != len("rw_admin_")+43 {
		t.Errorf("token length = %d, want %d", len(tok), len("rw_admin_")+43)
	}

	pw, err := RandomPassword()
	if err != nil || len(pw) != 32 {
		t.Errorf("RandomPassword = %q, %v", pw, err)
	}
}

func TestNormalizeEmail(t *testing.T) {
	tests := []struct{ in, want string }{
		{"  User@Example.COM ", "user@example.com"},
		{"a@B.c", "a@b.c"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := NormalizeEmail(tt.in); got != tt.want {
			t.Errorf("NormalizeEmail(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
