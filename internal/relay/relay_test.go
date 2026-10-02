package relay

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

// acceptAuth lets the fake upstreams advertise and accept AUTH PLAIN so the
// relay client can run its normal authentication step.
func acceptAuth(_ string) (sasl.Server, error) {
	return sasl.NewPlainServer(func(_, _, _ string) error {
		return nil
	}), nil
}

type recordedMessage struct {
	From  string
	Rcpts []string
	Data  []byte
}

type recordingBackend struct {
	mu       sync.Mutex
	messages []recordedMessage
}

func (b *recordingBackend) NewSession(*smtp.Conn) (smtp.Session, error) {
	return &recordingSession{backend: b}, nil
}

type recordingSession struct {
	backend *recordingBackend
	msg     recordedMessage
}

func (s *recordingSession) Reset()        { s.msg = recordedMessage{} }
func (s *recordingSession) Logout() error { return nil }

func (s *recordingSession) AuthMechanisms() []string { return []string{sasl.Plain} }
func (s *recordingSession) Auth(mech string) (sasl.Server, error) {
	return acceptAuth(mech)
}

func (s *recordingSession) Mail(from string, _ *smtp.MailOptions) error {
	s.msg.From = from
	return nil
}

func (s *recordingSession) Rcpt(to string, _ *smtp.RcptOptions) error {
	s.msg.Rcpts = append(s.msg.Rcpts, to)
	return nil
}

func (s *recordingSession) Data(r io.Reader) error {
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

// rejectingBackend fails every RCPT TO with the given SMTP error.
type rejectingBackend struct {
	rcptErr *smtp.SMTPError
}

func (b *rejectingBackend) NewSession(*smtp.Conn) (smtp.Session, error) {
	return &rejectingSession{rcptErr: b.rcptErr}, nil
}

type rejectingSession struct {
	rcptErr *smtp.SMTPError
}

func (s *rejectingSession) Reset()        {}
func (s *rejectingSession) Logout() error { return nil }

func (s *rejectingSession) AuthMechanisms() []string { return []string{sasl.Plain} }
func (s *rejectingSession) Auth(mech string) (sasl.Server, error) {
	return acceptAuth(mech)
}
func (s *rejectingSession) Mail(string, *smtp.MailOptions) error {
	return nil
}
func (s *rejectingSession) Rcpt(string, *smtp.RcptOptions) error {
	return s.rcptErr
}
func (s *rejectingSession) Data(io.Reader) error { return nil }

// startFakeUpstream starts a local plaintext SMTP server and returns its
// 127.0.0.1 address.
func startFakeUpstream(t *testing.T, backend smtp.Backend) string {
	t.Helper()
	srv := smtp.NewServer(backend)
	srv.Domain = "upstream.test"
	srv.AllowInsecureAuth = true

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }() // test upstream: failures surface via assertions
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

func newTestClient(addr string) *Client {
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ := strconv.Atoi(portStr)
	return New(Options{
		Host:        host,
		Port:        port,
		Username:    "apikey",
		Password:    "upstream-secret",
		TLSMode:     TLSNone,
		HelloDomain: "relayward.test",
		Timeout:     10 * time.Second,
	})
}

func closedPortClient() *Client {
	return New(Options{
		Host: "127.0.0.1", Port: 1, Username: "u", Password: "p",
		TLSMode: TLSNone, HelloDomain: "relayward.test", Timeout: 2 * time.Second,
	})
}

func TestSendRelaysToUpstream(t *testing.T) {
	backend := &recordingBackend{}
	addr := startFakeUpstream(t, backend)
	cl := newTestClient(addr)

	body := []byte("From: NoReply@example.com\r\nSubject: hi\r\n\r\nhello world\r\n")
	err := cl.Send(context.Background(), "noreply@example.com", []string{"a@b.com", "c@d.com"}, body)
	if err != nil {
		t.Fatalf("Send: %v", err)
	}

	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.messages) != 1 {
		t.Fatalf("upstream got %d messages, want 1", len(backend.messages))
	}
	msg := backend.messages[0]
	if msg.From != "noreply@example.com" {
		t.Errorf("from = %q", msg.From)
	}
	if len(msg.Rcpts) != 2 || msg.Rcpts[0] != "a@b.com" || msg.Rcpts[1] != "c@d.com" {
		t.Errorf("rcpts = %v", msg.Rcpts)
	}
	if string(msg.Data) != string(body) {
		t.Errorf("data = %q, want %q", msg.Data, body)
	}
}

func TestSendUpstreamRejectsRcpt(t *testing.T) {
	tests := []struct {
		name         string
		rcptErr      *smtp.SMTPError
		wantTemp     bool
		wantUpstream int
	}{
		{"permanent", &smtp.SMTPError{Code: 550, Message: "no such user"}, false, 550},
		{"temporary", &smtp.SMTPError{Code: 451, Message: "try later"}, true, 451},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr := startFakeUpstream(t, &rejectingBackend{rcptErr: tt.rcptErr})
			cl := newTestClient(addr)

			err := cl.Send(context.Background(), "noreply@example.com", []string{"a@b.com"}, []byte("x"))
			var re *Error
			if !errors.As(err, &re) {
				t.Fatalf("err = %v, want *relay.Error", err)
			}
			if re.Temporary != tt.wantTemp {
				t.Errorf("Temporary = %v, want %v (%v)", re.Temporary, tt.wantTemp, re)
			}
			if re.Code != tt.wantUpstream {
				t.Errorf("Code = %d, want %d", re.Code, tt.wantUpstream)
			}
		})
	}
}

func TestSendUpstreamUnreachable(t *testing.T) {
	cl := closedPortClient()
	err := cl.Send(context.Background(), "noreply@example.com", []string{"a@b.com"}, []byte("x"))
	var re *Error
	if !errors.As(err, &re) {
		t.Fatalf("err = %v, want *relay.Error", err)
	}
	if !re.Temporary {
		t.Errorf("connection failure should be temporary: %v", re)
	}
}

func TestProbe(t *testing.T) {
	addr := startFakeUpstream(t, &recordingBackend{})
	cl := newTestClient(addr)
	if err := cl.Probe(context.Background()); err != nil {
		t.Fatalf("Probe: %v", err)
	}

	if err := closedPortClient().Probe(context.Background()); err == nil {
		t.Fatal("Probe on closed port should fail")
	}
}
