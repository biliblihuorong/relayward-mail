package smtpd

import (
	"testing"
	"time"

	"github.com/emersion/go-sasl"

	"relayward-mail/internal/ratelimit"
	"relayward-mail/internal/store"
)

func TestRateLimitedReturns451AndLogs(t *testing.T) {
	st := openStore(t)
	seedApp(t, st, "chatty", true)
	rate := 2
	if _, err := st.UpdateApp(t.Context(), "chatty", store.AppUpdate{RatePerHour: &rate}, nil); err != nil {
		t.Fatalf("set rate: %v", err)
	}

	be := NewBackend(st, newTestRelay(t, startFakeSMTPUpstream(t, &fakeUpstream{})), ratelimit.NewLimiter(), nil, nil)
	addr := startSMTP(t, be)

	cl := dialSMTP(t, addr)
	if err := cl.Auth(sasl.NewPlainClient("", "chatty", "pw-chatty")); err != nil {
		t.Fatalf("auth: %v", err)
	}

	// First two messages pass, the third hits the hourly limit.
	for i := 0; i < 2; i++ {
		body := "Subject: batch\r\n\r\nhi\r\n"
		if err := authAndSendAfterAuth(t, cl, "NoReply@example.com", "user@example.com", body); err != nil {
			t.Fatalf("message %d rejected: %v", i+1, err)
		}
	}
	err := authAndSendAfterAuth(t, cl, "NoReply@example.com", "user@example.com", "Subject: over\r\n\r\nhi\r\n")
	if code := smtpErrorCode(t, err); code != 451 {
		t.Fatalf("third message err = %v, want 451", err)
	}

	stats, err := st.AppStatsByName(t.Context(), "chatty")
	if err != nil {
		t.Fatal(err)
	}
	if stats.Sent != 2 || stats.RateLimited != 1 {
		t.Errorf("stats = %+v, want sent 2 rate_limited 1", stats)
	}
}

func TestAuthLockoutBansIP(t *testing.T) {
	st := openStore(t)
	seedApp(t, st, "gitea", true)

	// Three failures inside the window ban the source IP.
	lock := ratelimit.NewLockout(3, time.Minute, time.Hour)
	be := NewBackend(st, nil, nil, lock, nil)
	s := &session{backend: be, remoteIP: "10.9.9.9"}

	for i := 0; i < 3; i++ {
		err := s.authenticate("gitea", "wrong")
		if smtpErrorCode(t, err) != 535 {
			t.Fatalf("failure %d: err = %v, want 535", i+1, err)
		}
	}
	// Even the correct password is refused while banned, with the same 535.
	err := s.authenticate("gitea", "pw-gitea")
	if code := smtpErrorCode(t, err); code != 535 {
		t.Fatalf("banned auth err = %v, want 535", err)
	}

	// Other IPs are unaffected.
	other := &session{backend: be, remoteIP: "10.9.9.8"}
	if err := other.authenticate("gitea", "pw-gitea"); err != nil {
		t.Fatalf("other IP auth: %v", err)
	}

	// Reset (e.g. after ban expiry or explicit unblock) restores access.
	lock.Reset("10.9.9.9")
	if err := (&session{backend: be, remoteIP: "10.9.9.9"}).authenticate("gitea", "pw-gitea"); err != nil {
		t.Fatalf("auth after reset: %v", err)
	}
}

func TestSuccessfulAuthResetsFailures(t *testing.T) {
	st := openStore(t)
	seedApp(t, st, "gitea", true)

	lock := ratelimit.NewLockout(3, time.Minute, time.Hour)
	be := NewBackend(st, nil, nil, lock, nil)
	s := &session{backend: be, remoteIP: "10.0.0.5"}

	if err := s.authenticate("gitea", "wrong"); err == nil {
		t.Fatal("expected failure")
	}
	if err := s.authenticate("gitea", "pw-gitea"); err != nil {
		t.Fatalf("correct auth: %v", err)
	}
	// The success restarted the counter, so two more failures must not ban.
	if err := s.authenticate("gitea", "wrong"); err == nil {
		t.Fatal("expected failure")
	}
	if err := s.authenticate("gitea", "pw-gitea"); err != nil {
		t.Fatalf("auth should still work, not banned: %v", err)
	}
}
