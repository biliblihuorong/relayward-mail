package smtpd

import (
	"errors"
	"net/textproto"
	"testing"

	smtp "github.com/emersion/go-smtp"

	"relayward-mail/internal/store"
)

func TestSenderAllowed(t *testing.T) {
	allowed := []string{"NoReply@example.com", "bounce@example.com"}
	tests := []struct {
		addr string
		want bool
	}{
		{"noreply@example.com", true},
		{"  NoReply@example.com  ", true},
		{"NOREPLY@EXAMPLE.COM", true},
		{"other@example.com", false},
		{"noreply@evil.com", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := senderAllowed(allowed, tt.addr); got != tt.want {
			t.Errorf("senderAllowed(%q) = %v, want %v", tt.addr, got, tt.want)
		}
	}
}

func TestLoginServerFlow(t *testing.T) {
	var gotUser, gotPass string
	l := &loginServer{auth: func(u, p string) error {
		gotUser, gotPass = u, p
		return nil
	}}

	// Without initial response.
	challenge, done, err := l.Next(nil)
	if err != nil || done || string(challenge) != "Username:" {
		t.Fatalf("step 1: challenge=%q done=%v err=%v", challenge, done, err)
	}
	challenge, done, err = l.Next([]byte("gitea"))
	if err != nil || done || string(challenge) != "Password:" {
		t.Fatalf("step 2: challenge=%q done=%v err=%v", challenge, done, err)
	}
	challenge, done, err = l.Next([]byte("secret"))
	if err != nil || !done || challenge != nil {
		t.Fatalf("step 3: challenge=%q done=%v err=%v", challenge, done, err)
	}
	if gotUser != "gitea" || gotPass != "secret" {
		t.Errorf("auth got %q / %q", gotUser, gotPass)
	}

	// With initial response carrying the username.
	l2 := &loginServer{auth: func(_, _ string) error { return nil }}
	challenge, done, err = l2.Next([]byte("gitea"))
	if err != nil || done || string(challenge) != "Password:" {
		t.Fatalf("initial response step: challenge=%q done=%v err=%v", challenge, done, err)
	}
}

func TestLoginServerAuthError(t *testing.T) {
	l := &loginServer{auth: func(_, _ string) error { return errAuthFailed }}
	_, _, _ = l.Next([]byte("gitea"))
	_, done, err := l.Next([]byte("wrong"))
	if !done || !errors.Is(err, errAuthFailed) {
		t.Fatalf("done=%v err=%v, want done with errAuthFailed", done, err)
	}
}

func TestParseMessageMeta(t *testing.T) {
	data := []byte("Message-Id: <abc@example.com>\r\nSubject: Welcome aboard\r\nFrom: a@b.com\r\n\r\nbody\r\n")
	meta := parseMessageMeta(data)
	if meta.MessageID != "<abc@example.com>" {
		t.Errorf("MessageID = %q", meta.MessageID)
	}
	if meta.Subject != "Welcome aboard" {
		t.Errorf("Subject = %q", meta.Subject)
	}

	empty := parseMessageMeta([]byte("not a message"))
	if empty.MessageID != "" || empty.Subject != "" {
		t.Errorf("garbage input should give empty meta: %+v", empty)
	}
}

// smtpErrorCode extracts the numeric SMTP status from a client-side error.
func smtpErrorCode(t *testing.T, err error) int {
	t.Helper()
	var smtpErr *smtp.SMTPError
	var textErr *textproto.Error
	switch {
	case errors.As(err, &smtpErr):
		return smtpErr.Code
	case errors.As(err, &textErr):
		return textErr.Code
	default:
		t.Fatalf("err %v (%T) carries no SMTP status", err, err)
		return 0
	}
}

func TestBeginTransferWhileStopping(t *testing.T) {
	be := NewBackend(nil, nil, nil, nil, nil, nil, "", "")
	if err := be.beginTransfer(); err != nil {
		t.Fatalf("beginTransfer before stop: %v", err)
	}
	be.endTransfer()

	be.Stop()
	err := be.beginTransfer()
	var smtpErr *smtp.SMTPError
	if !errors.As(err, &smtpErr) || smtpErr.Code != 451 {
		t.Fatalf("beginTransfer while stopping = %v, want 451 SMTPError", err)
	}
}

func TestAuthenticate(t *testing.T) {
	st := openStore(t)
	seedApp(t, st, "gitea", true)
	seedApp(t, st, "offapp", false)

	be := NewBackend(st, nil, nil, nil, nil, nil, "", "")

	tests := []struct {
		name     string
		user     string
		password string
		wantCode int
	}{
		{"ok", "gitea", "pw-gitea", 0},
		{"wrong password", "gitea", "nope", 535},
		{"unknown app", "ghost", "pw", 535},
		{"disabled app", "offapp", "pw-offapp", 535},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &session{backend: be, remoteIP: "10.0.0.9"}
			err := s.authenticate(tt.user, tt.password)
			if tt.wantCode == 0 {
				if err != nil {
					t.Fatalf("authenticate: %v", err)
				}
				if s.app == nil || s.app.Name != tt.user {
					t.Fatalf("session app not set")
				}
				return
			}
			if smtpErrorCode(t, err) != tt.wantCode {
				t.Fatalf("code = %d, want %d", smtpErrorCode(t, err), tt.wantCode)
			}
			if s.app != nil {
				t.Fatalf("app should stay unset on failure")
			}
		})
	}
}

func TestSessionGuards(t *testing.T) {
	st := openStore(t)
	be := NewBackend(st, nil, nil, nil, nil, nil, "", "")
	s := &session{backend: be}

	if err := s.Mail("noreply@example.com", nil); smtpErrorCode(t, err) != 530 {
		t.Errorf("unauthenticated Mail = %v, want 530", err)
	}
	if err := s.Rcpt("a@b.com", nil); smtpErrorCode(t, err) != 530 {
		t.Errorf("unauthenticated Rcpt = %v, want 530", err)
	}
	if err := s.Data(nil); smtpErrorCode(t, err) != 530 {
		t.Errorf("unauthenticated Data = %v, want 530", err)
	}

	s.app = &store.App{ID: 1, Name: "gitea", AllowedFrom: []string{"noreply@example.com"}}
	if err := s.Data(nil); smtpErrorCode(t, err) != 503 {
		t.Errorf("Data without recipients = %v, want 503", err)
	}
	if err := s.Mail("hacker@evil.com", nil); smtpErrorCode(t, err) != 550 {
		t.Errorf("disallowed sender = %v, want 550", err)
	}
	if err := s.Mail("not-an-address", nil); smtpErrorCode(t, err) != 501 {
		t.Errorf("invalid sender = %v, want 501", err)
	}
	if err := s.Mail("NoReply@example.com", nil); err != nil {
		t.Errorf("allowed sender rejected: %v", err)
	}
	if err := s.Rcpt("bad address", nil); smtpErrorCode(t, err) != 501 {
		t.Errorf("invalid rcpt = %v, want 501", err)
	}

	// Reset clears the transaction but keeps the authenticated app.
	s.Reset()
	if s.mailFrom != "" || len(s.rcpts) != 0 {
		t.Errorf("Reset did not clear transaction state")
	}
	if s.app == nil {
		t.Errorf("Reset cleared the authenticated app")
	}
	if err := s.Logout(); err != nil {
		t.Errorf("Logout = %v", err)
	}
}
