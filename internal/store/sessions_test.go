package store

import (
	"context"
	"testing"
	"time"
)

// seedSession creates a live login session for tok and returns it; use
// HashToken("rws_test_<name>") to look it up.
func seedSession(t *testing.T, s *Store, tok *AdminToken, expiresIn time.Duration) *AdminSession {
	t.Helper()
	sess := &AdminSession{
		TokenHash: HashToken("rws_test_" + tok.Name),
		TokenID:   tok.ID,
		ExpiresAt: time.Now().UTC().Add(expiresIn),
	}
	if err := s.CreateAdminSession(context.Background(), sess, nil); err != nil {
		t.Fatalf("seed session for %s: %v", tok.Name, err)
	}
	return sess
}

func TestAdminSessionLifecycle(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	tok := seedToken(t, s, "admin", RoleAdmin)

	seedSession(t, s, tok, time.Hour)

	got, err := s.GetAdminSession(ctx, HashToken("rws_test_admin"))
	if err != nil {
		t.Fatalf("GetAdminSession: %v", err)
	}
	if got.TokenID != tok.ID || got.TokenName != "admin" || got.Role != RoleAdmin {
		t.Errorf("session joined token mismatch: %+v", got)
	}
	if got.ExpiresAt.Before(time.Now()) {
		t.Errorf("session already expired: %+v", got.ExpiresAt)
	}

	if err := s.TouchAdminSession(ctx, got.ID); err != nil {
		t.Fatalf("TouchAdminSession: %v", err)
	}

	if err := s.DeleteAdminSession(ctx, HashToken("rws_test_admin")); err != nil {
		t.Fatalf("DeleteAdminSession: %v", err)
	}
	// Logging out twice stays idempotent.
	if err := s.DeleteAdminSession(ctx, HashToken("rws_test_admin")); err != nil {
		t.Fatalf("second DeleteAdminSession: %v", err)
	}
	if _, err := s.GetAdminSession(ctx, HashToken("rws_test_admin")); err == nil {
		t.Fatal("deleted session still resolvable")
	}
}

func TestAdminSessionExpiry(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	tok := seedToken(t, s, "admin", RoleAdmin)

	seedSession(t, s, tok, -time.Minute) // already expired

	if _, err := s.GetAdminSession(ctx, HashToken("rws_test_admin")); err == nil {
		t.Fatal("expired session still resolvable")
	}
}

func TestAdminSessionDiesWithToken(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	tok := seedToken(t, s, "admin", RoleAdmin)
	seedSession(t, s, tok, time.Hour)

	if err := s.DeleteAdminToken(ctx, tok.ID, nil); err != nil {
		t.Fatalf("DeleteAdminToken: %v", err)
	}
	if _, err := s.GetAdminSession(ctx, HashToken("rws_test_admin")); err == nil {
		t.Fatal("session survived its token's revocation")
	}
}

func TestAdminSessionDiesWithReset(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	tok := seedToken(t, s, "admin", RoleAdmin)
	seedSession(t, s, tok, time.Hour)

	if _, err := s.ResetAdminTokens(ctx, nil); err != nil {
		t.Fatalf("ResetAdminTokens: %v", err)
	}
	if _, err := s.GetAdminSession(ctx, HashToken("rws_test_admin")); err == nil {
		t.Fatal("session survived the admin reset")
	}
}

func TestAdminSessionValidation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if err := s.CreateAdminSession(ctx, &AdminSession{TokenHash: "", TokenID: 1}, nil); err == nil {
		t.Error("empty hash accepted")
	}
	if err := s.CreateAdminSession(ctx, &AdminSession{TokenHash: HashToken("x"), TokenID: 0}, nil); err == nil {
		t.Error("zero token id accepted")
	}
}
