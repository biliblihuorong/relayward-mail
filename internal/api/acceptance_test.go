package api

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/textproto"
	"testing"

	"github.com/emersion/go-sasl"
	smtp "github.com/emersion/go-smtp"

	"relayward-mail/internal/relay"
	"relayward-mail/internal/smtpd"
	"relayward-mail/internal/store"
)

// startGatewaySMTP boots the ingress SMTP server backed by the same store as
// the management API, relaying into the given upstream client.
func startGatewaySMTP(t *testing.T, st *store.Store, rl *relay.Client) string {
	t.Helper()
	be := smtpd.NewBackend(st, rl, nil, nil, nil) // no limiter/lockout here
	srv := smtp.NewServer(be)
	srv.Domain = "mail.example.com"
	srv.AllowInsecureAuth = true

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

// sendViaGateway performs one full SMTP transaction. The error, if any, is
// returned so callers can assert rejection codes.
func sendViaGateway(t *testing.T, addr, username, password string) error {
	t.Helper()
	cl, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer cl.Close()

	if err := cl.Hello("client.test"); err != nil {
		return fmt.Errorf("hello: %w", err)
	}
	if err := cl.Auth(sasl.NewPlainClient("", username, password)); err != nil {
		return fmt.Errorf("auth: %w", err)
	}
	if err := cl.Mail("NoReply@example.com", nil); err != nil {
		return fmt.Errorf("mail from: %w", err)
	}
	if err := cl.Rcpt("user@example.com", nil); err != nil {
		return fmt.Errorf("rcpt to: %w", err)
	}
	w, err := cl.Data()
	if err != nil {
		return fmt.Errorf("data: %w", err)
	}
	if _, err := w.Write([]byte("Subject: m2 acceptance\r\n\r\nhello from the acceptance test\r\n")); err != nil {
		return fmt.Errorf("write data: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("end of data: %w", err)
	}
	return nil
}

// smtpErrorCode extracts the SMTP reply code from a client error.
func smtpErrorCode(t *testing.T, err error) int {
	t.Helper()
	var te *textproto.Error
	var se *smtp.SMTPError
	switch {
	case errors.As(err, &te):
		return te.Code
	case errors.As(err, &se):
		return se.Code
	default:
		t.Fatalf("error %v (%T) carries no SMTP reply code", err, err)
		return 0
	}
}

// TestM2Acceptance walks the whole M2 story through the public surfaces only:
// create an app via the management API, relay mail through the gateway with
// the returned one-time password, rotate the password via the API and prove
// the old password stopped working while the new one works.
func TestM2Acceptance(t *testing.T) {
	st, ts := startAPI(t, nil)
	admin := seedToken(t, st, "admin", store.RoleAdmin)
	gw := startGatewaySMTP(t, st, newRelay(t, startFakeUpstream(t)))

	// Step 1: create the app through the management API.
	status, body, _ := apiCall(t, ts, http.MethodPost, "/api/apps", admin, map[string]any{
		"name":         "gitea",
		"allowed_from": []string{"NoReply@example.com"},
		"display_name": "Gitea",
		"unsubscribe":  true,
	})
	if status != http.StatusCreated {
		t.Fatalf("create app: status = %d, want 201 (body %v)", status, body)
	}
	password := str(body, "smtp_password")
	if password == "" {
		t.Fatal("create app: smtp_password empty")
	}

	// Step 2: the API-issued password relays mail through the gateway.
	if err := sendViaGateway(t, gw, "gitea", password); err != nil {
		t.Fatalf("send with api-issued password: %v", err)
	}

	// Step 3: rotate the password via the API.
	status, body, _ = apiCall(t, ts, http.MethodPost, "/api/apps/gitea/rotate", admin, nil)
	if status != http.StatusOK {
		t.Fatalf("rotate: status = %d, want 200 (body %v)", status, body)
	}
	newPassword := str(body, "smtp_password")
	if newPassword == "" {
		t.Fatal("rotate: smtp_password empty")
	}
	if newPassword == password {
		t.Fatal("rotate returned the same password")
	}

	// Step 4: the old password is rejected with 535.
	err := sendViaGateway(t, gw, "gitea", password)
	if err == nil {
		t.Fatal("old password accepted after rotate")
	}
	if code := smtpErrorCode(t, err); code != 535 {
		t.Fatalf("old password rejected with code %d (%v), want 535", code, err)
	}

	// Step 5: the new password works.
	if err := sendViaGateway(t, gw, "gitea", newPassword); err != nil {
		t.Fatalf("send with rotated password: %v", err)
	}

	// Both successful sends are in the message log.
	viewer := seedToken(t, st, "view", store.RoleViewer)
	status, body, _ = apiCall(t, ts, http.MethodGet, "/api/messages?app=gitea", viewer, nil)
	if status != http.StatusOK {
		t.Fatalf("list messages: status = %d, want 200 (body %v)", status, body)
	}
	msgs := bodySlice(t, body, "messages")
	if len(msgs) != 2 {
		t.Fatalf("message log has %d rows, want 2: %v", len(msgs), body)
	}
	for _, m := range msgs {
		if got := str(m, "app"); got != "gitea" {
			t.Errorf("message app = %q, want gitea", got)
		}
		if got := str(m, "status"); got != store.StatusSent {
			t.Errorf("message status = %q, want sent", got)
		}
	}
}

// TestViewerCannotWrite checks the read-only guarantee for viewer tokens
// across every write endpoint.
func TestViewerCannotWrite(t *testing.T) {
	st, ts := startAPI(t, nil)
	viewer := seedToken(t, st, "view", store.RoleViewer)

	cases := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"create app", http.MethodPost, "/api/apps", map[string]any{"name": "x", "allowed_from": []string{"a@example.com"}}},
		{"patch app", http.MethodPatch, "/api/apps/x", map[string]any{"display_name": "X"}},
		{"rotate app", http.MethodPost, "/api/apps/x/rotate", nil},
		{"delete app", http.MethodDelete, "/api/apps/x", nil},
		{"create token", http.MethodPost, "/api/tokens", map[string]any{"name": "x", "role": "viewer"}},
		{"revoke token", http.MethodDelete, "/api/tokens/1", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body, _ := apiCall(t, ts, tc.method, tc.path, viewer, tc.body)
			if status != http.StatusForbidden {
				t.Fatalf("status = %d, want 403 (body %v)", status, body)
			}
			errObj, ok := body["error"].(map[string]any)
			if !ok {
				t.Fatalf("body %v carries no error object", body)
			}
			if got := str(errObj, "code"); got != "forbidden" {
				t.Errorf("error.code = %q, want forbidden", got)
			}
		})
	}
}
