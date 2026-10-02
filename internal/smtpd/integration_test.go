package smtpd

import (
	"io"
	"net"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-sasl"
	smtp "github.com/emersion/go-smtp"

	"relayward-mail/internal/relay"
	"relayward-mail/internal/store"
)

// openStore opens a throwaway store for tests.
func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "relayward.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

// seedApp inserts an app with password "pw-<name>". The unsubscribe switch
// is off, so these tests exercise the plain as-is relay path; the split path
// has its own tests with explicitly enabled apps.
func seedApp(t *testing.T, st *store.Store, name string, enabled bool) *store.App {
	t.Helper()
	hash, err := store.HashPassword("pw-" + name)
	if err != nil {
		t.Fatal(err)
	}
	app := &store.App{
		Name:         name,
		PasswordHash: hash,
		Enabled:      enabled,
		Unsubscribe:  false,
		AllowedFrom:  []string{"NoReply@example.com"},
		RatePerHour:  500,
	}
	if err := st.CreateApp(t.Context(), app, nil); err != nil {
		t.Fatalf("seed app %s: %v", name, err)
	}
	return app
}

// fakeUpstream records what the gateway relays.
type fakeUpstream struct {
	mu       sync.Mutex
	messages []relayed
}

type relayed struct {
	From  string
	Rcpts []string
	Data  []byte
}

func (f *fakeUpstream) NewSession(*smtp.Conn) (smtp.Session, error) {
	return &fakeSession{backend: f}, nil
}

type fakeSession struct {
	backend *fakeUpstream
	msg     relayed
}

func (s *fakeSession) Reset()        { s.msg = relayed{} }
func (s *fakeSession) Logout() error { return nil }

func (s *fakeSession) AuthMechanisms() []string { return []string{sasl.Plain} }
func (s *fakeSession) Auth(_ string) (sasl.Server, error) {
	return sasl.NewPlainServer(func(_, _, _ string) error { return nil }), nil
}

func (s *fakeSession) Mail(from string, _ *smtp.MailOptions) error {
	s.msg.From = from
	return nil
}

func (s *fakeSession) Rcpt(to string, _ *smtp.RcptOptions) error {
	s.msg.Rcpts = append(s.msg.Rcpts, to)
	return nil
}

func (s *fakeSession) Data(r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.msg.Data = data
	s.backend.mu.Lock()
	s.backend.messages = append(s.backend.messages, s.msg)
	s.backend.mu.Unlock()
	return nil
}

// rejectingUpstream fails every RCPT TO with a permanent error.
type rejectingUpstream struct{}

func (rejectingUpstream) NewSession(*smtp.Conn) (smtp.Session, error) {
	return rejectingSession{}, nil
}

type rejectingSession struct{}

func (rejectingSession) Reset()        {}
func (rejectingSession) Logout() error { return nil }
func (rejectingSession) AuthMechanisms() []string {
	return []string{sasl.Plain}
}
func (rejectingSession) Auth(_ string) (sasl.Server, error) {
	return sasl.NewPlainServer(func(_, _, _ string) error { return nil }), nil
}
func (rejectingSession) Mail(string, *smtp.MailOptions) error { return nil }
func (rejectingSession) Rcpt(string, *smtp.RcptOptions) error {
	return &smtp.SMTPError{Code: 550, Message: "no such user"}
}
func (rejectingSession) Data(io.Reader) error { return nil }

// startSMTP starts the gateway SMTP server on 127.0.0.1:0.
func startSMTP(t *testing.T, be smtp.Backend) string {
	t.Helper()
	srv := smtp.NewServer(be)
	srv.Domain = "mail.example.com"
	srv.AllowInsecureAuth = true
	srv.MaxMessageBytes = 1024 * 1024
	srv.MaxRecipients = 100

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

func dialSMTP(t *testing.T, addr string) *smtp.Client {
	t.Helper()
	cl, err := smtp.Dial(addr)
	if err != nil {
		t.Fatalf("dial gateway: %v", err)
	}
	t.Cleanup(func() { _ = cl.Close() })
	if err := cl.Hello("client.example.com"); err != nil {
		t.Fatalf("EHLO: %v", err)
	}
	return cl
}

func authAndSend(t *testing.T, cl *smtp.Client, user, pass, from, to, body string) error {
	t.Helper()
	if err := cl.Auth(sasl.NewPlainClient("", user, pass)); err != nil {
		return err
	}
	if err := cl.Mail(from, nil); err != nil {
		return err
	}
	if err := cl.Rcpt(to, nil); err != nil {
		return err
	}
	w, err := cl.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(body)); err != nil {
		return err
	}
	return w.Close()
}

// newGateway wires store + relay + backend together for integration tests.
func newGateway(t *testing.T) (*store.Store, *fakeUpstream, string) {
	t.Helper()
	st := openStore(t)
	up := &fakeUpstream{}
	addr := startFakeSMTPUpstream(t, up)
	cl := newTestRelay(t, addr)
	be := NewBackend(st, cl, nil, nil, nil, nil, "", "")
	return st, up, startSMTP(t, be)
}

func startFakeSMTPUpstream(t *testing.T, be smtp.Backend) string {
	t.Helper()
	srv := smtp.NewServer(be)
	srv.Domain = "upstream.test"
	srv.AllowInsecureAuth = true
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("upstream listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

func newTestRelay(t *testing.T, addr string) *relay.Client {
	t.Helper()
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)
	return relay.New(relay.Options{
		Host: host, Port: port,
		Username: "apikey", Password: "upstream-secret",
		TLSMode: relay.TLSNone, HelloDomain: "relayward.test",
		Timeout: 10 * time.Second,
	})
}

func TestEndToEndRelay(t *testing.T) {
	st, up, addr := newGateway(t)
	seedApp(t, st, "gitea", true)

	cl := dialSMTP(t, addr)
	body := "From: NoReply@example.com\r\nTo: user@example.com\r\nSubject: Hello\r\nMessage-Id: <m1@gitea>\r\n\r\nworld\r\n"
	if err := authAndSend(t, cl, "gitea", "pw-gitea", "NoReply@example.com", "user@example.com", body); err != nil {
		t.Fatalf("auth and send: %v", err)
	}

	up.mu.Lock()
	defer up.mu.Unlock()
	if len(up.messages) != 1 {
		t.Fatalf("upstream got %d messages, want 1", len(up.messages))
	}
	msg := up.messages[0]
	if msg.From != "NoReply@example.com" || len(msg.Rcpts) != 1 || msg.Rcpts[0] != "user@example.com" {
		t.Errorf("unexpected envelope: %+v", msg)
	}
	if string(msg.Data) != body {
		t.Errorf("relayed body mismatch:\n got %q\nwant %q", msg.Data, body)
	}

	last, err := st.LastSuccessfulRelayTime(t.Context())
	if err != nil || last == nil {
		t.Fatalf("message log row missing: %v, %v", last, err)
	}
}

func TestEndToEndWrongPassword(t *testing.T) {
	st, up, addr := newGateway(t)
	seedApp(t, st, "gitea", true)

	cl := dialSMTP(t, addr)
	err := authAndSend(t, cl, "gitea", "wrong", "NoReply@example.com", "user@example.com", "body")
	if smtpErrorCode(t, err) != 535 {
		t.Fatalf("err = %v, want 535", err)
	}

	up.mu.Lock()
	defer up.mu.Unlock()
	if len(up.messages) != 0 {
		t.Errorf("nothing should reach upstream on auth failure")
	}
	if last, _ := st.LastSuccessfulRelayTime(t.Context()); last != nil {
		t.Errorf("no log row expected, got %v", last)
	}
}

func TestEndToEndDisabledApp(t *testing.T) {
	st, _, addr := newGateway(t)
	seedApp(t, st, "oldapp", false)

	cl := dialSMTP(t, addr)
	err := authAndSend(t, cl, "oldapp", "pw-oldapp", "NoReply@example.com", "user@example.com", "body")
	if smtpErrorCode(t, err) != 535 {
		t.Fatalf("err = %v, want 535 for disabled app", err)
	}
}

func TestEndToEndSenderNotAllowed(t *testing.T) {
	st, up, addr := newGateway(t)
	seedApp(t, st, "gitea", true)

	cl := dialSMTP(t, addr)
	if err := cl.Auth(sasl.NewPlainClient("", "gitea", "pw-gitea")); err != nil {
		t.Fatalf("auth: %v", err)
	}
	err := cl.Mail("spoof@evil.com", nil)
	if smtpErrorCode(t, err) != 550 {
		t.Fatalf("Mail err = %v, want 550", err)
	}

	up.mu.Lock()
	defer up.mu.Unlock()
	if len(up.messages) != 0 {
		t.Errorf("rejected mail must not be forwarded")
	}
}

func TestEndToEndMultipleRecipientsLogPerRcpt(t *testing.T) {
	st, up, addr := newGateway(t)
	seedApp(t, st, "gitea", true)

	cl := dialSMTP(t, addr)
	body := "Subject: batch\r\n\r\nhello\r\n"
	if err := cl.Auth(sasl.NewPlainClient("", "gitea", "pw-gitea")); err != nil {
		t.Fatalf("auth: %v", err)
	}
	if err := cl.Mail("NoReply@example.com", nil); err != nil {
		t.Fatal(err)
	}
	for _, to := range []string{"a@example.com", "B@Example.com"} {
		if err := cl.Rcpt(to, nil); err != nil {
			t.Fatal(err)
		}
	}
	w, err := cl.Data()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("DATA close: %v", err)
	}

	up.mu.Lock()
	defer up.mu.Unlock()
	if len(up.messages) != 1 || len(up.messages[0].Rcpts) != 2 {
		t.Fatalf("upstream envelope = %+v, want one message with two recipients", up.messages)
	}
	if up.messages[0].Rcpts[1] != "B@Example.com" {
		t.Errorf("recipient case altered: %v", up.messages[0].Rcpts)
	}

	// Two log rows: verify indirectly that the last successful relay is set
	// and pruning can see rows (row-level assertions live in store tests).
	if last, _ := st.LastSuccessfulRelayTime(t.Context()); last == nil {
		t.Fatal("expected log rows for both recipients")
	}
}

func TestEndToEndUpstreamUnreachable(t *testing.T) {
	st := openStore(t)
	seedApp(t, st, "gitea", true)

	be := NewBackend(st, relay.New(relay.Options{Host: "127.0.0.1", Port: 1, Username: "u", Password: "p", TLSMode: relay.TLSNone, HelloDomain: "relayward.test", Timeout: 5 * time.Second}), nil, nil, nil, nil, "", "")
	addr := startSMTP(t, be)

	cl := dialSMTP(t, addr)
	err := authAndSend(t, cl, "gitea", "pw-gitea", "NoReply@example.com", "user@example.com", "body")
	if smtpErrorCode(t, err) != 451 {
		t.Fatalf("err = %v, want 451 on upstream failure", err)
	}
	if last, _ := st.LastSuccessfulRelayTime(t.Context()); last != nil {
		t.Errorf("failed relay must not set last success")
	}
}

func TestEndToEndUpstreamPermReject(t *testing.T) {
	st := openStore(t)
	seedApp(t, st, "gitea", true)

	be := NewBackend(st, newTestRelay(t, startFakeSMTPUpstream(t, rejectingUpstream{})), nil, nil, nil, nil, "", "")
	addr := startSMTP(t, be)

	cl := dialSMTP(t, addr)
	err := authAndSend(t, cl, "gitea", "pw-gitea", "NoReply@example.com", "user@example.com", "body")
	if smtpErrorCode(t, err) != 554 {
		t.Fatalf("err = %v, want 554 on permanent upstream rejection", err)
	}
}

func TestEndToEndLoginMechanism(t *testing.T) {
	st, up, addr := newGateway(t)
	seedApp(t, st, "gitea", true)

	cl := dialSMTP(t, addr)
	body := "Subject: via login\r\n\r\nhi\r\n"
	if err := cl.Auth(sasl.NewLoginClient("gitea", "pw-gitea")); err != nil {
		t.Fatalf("LOGIN auth: %v", err)
	}
	if err := authAndSendAfterAuth(t, cl, "NoReply@example.com", "user@example.com", body); err != nil {
		t.Fatalf("send: %v", err)
	}

	up.mu.Lock()
	defer up.mu.Unlock()
	if len(up.messages) != 1 {
		t.Fatalf("upstream got %d messages, want 1", len(up.messages))
	}
}

// authAndSendAfterAuth sends a message on an already-authenticated client.
func authAndSendAfterAuth(t *testing.T, cl *smtp.Client, from, to, body string) error {
	t.Helper()
	if err := cl.Mail(from, nil); err != nil {
		return err
	}
	if err := cl.Rcpt(to, nil); err != nil {
		return err
	}
	w, err := cl.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(body)); err != nil {
		return err
	}
	return w.Close()
}
