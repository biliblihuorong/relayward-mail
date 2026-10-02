// Package relay forwards accepted messages to the upstream SMTP provider.
package relay

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"
)

// TLS modes for the upstream connection.
const (
	TLSStarttls = "starttls"
	TLSNone     = "none" // plaintext; only sensible for local testing
)

// DefaultTimeout bounds a single relay attempt.
const DefaultTimeout = 5 * time.Minute

// Error is an upstream failure. Temporary marks 4xx replies and connection
// problems, so the caller can answer the client program with 451.
type Error struct {
	Code      int // upstream SMTP reply code; 0 for connection-level failures
	Message   string
	Temporary bool
}

// Error implements error.
func (e *Error) Error() string {
	if e.Code == 0 {
		return e.Message
	}
	return fmt.Sprintf("upstream replied %d: %s", e.Code, e.Message)
}

// Client relays messages to one upstream provider account.
type Client struct {
	host     string
	port     int
	username string
	password string
	tlsMode  string
	hello    string
	timeout  time.Duration
}

// Options is the configuration for a Client.
type Options struct {
	Host        string
	Port        int
	Username    string
	Password    string
	TLSMode     string // TLSStarttls or TLSNone
	HelloDomain string
	Timeout     time.Duration // zero means DefaultTimeout
}

// New builds a Client from options.
func New(opts Options) *Client {
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultTimeout
	}
	return &Client{
		host:     opts.Host,
		port:     opts.Port,
		username: opts.Username,
		password: opts.Password,
		tlsMode:  opts.TLSMode,
		hello:    opts.HelloDomain,
		timeout:  opts.Timeout,
	}
}

func (c *Client) addr() string {
	return net.JoinHostPort(c.host, fmt.Sprint(c.port))
}

// Send submits the message to the upstream provider with a single envelope
// containing every recipient. The error, if any, is a *Error.
func (c *Client) Send(ctx context.Context, from string, rcpts []string, data []byte) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", c.addr())
	if err != nil {
		return &Error{Message: fmt.Sprintf("dial upstream: %v", err), Temporary: true}
	}
	// net/smtp has no context support; bound the whole session with the
	// context deadline instead.
	if deadline, ok := ctx.Deadline(); ok {
		if err := conn.SetDeadline(deadline); err != nil {
			_ = conn.Close()
			return &Error{Message: fmt.Sprintf("set upstream deadline: %v", err), Temporary: true}
		}
	}

	cl, err := smtp.NewClient(conn, c.host)
	if err != nil {
		_ = conn.Close()
		return &Error{Message: fmt.Sprintf("upstream greeting: %v", err), Temporary: true}
	}
	defer cl.Close()

	if err := cl.Hello(c.hello); err != nil {
		return tempOrPerm("EHLO", err)
	}
	if c.tlsMode == TLSStarttls {
		ok, _ := cl.Extension("STARTTLS")
		if !ok {
			return &Error{Message: "upstream does not offer STARTTLS"}
		}
		if err := cl.StartTLS(&tls.Config{ServerName: c.host, MinVersion: tls.VersionTLS12}); err != nil {
			return tempOrPerm("STARTTLS", err)
		}
	}

	if err := cl.Auth(smtp.PlainAuth("", c.username, c.password, c.host)); err != nil {
		return tempOrPerm("AUTH", err)
	}
	if err := cl.Mail(from); err != nil {
		return tempOrPerm("MAIL FROM", err)
	}
	for _, rcpt := range rcpts {
		if err := cl.Rcpt(rcpt); err != nil {
			return tempOrPerm("RCPT TO "+rcpt, err)
		}
	}

	w, err := cl.Data()
	if err != nil {
		return tempOrPerm("DATA", err)
	}
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		return &Error{Message: fmt.Sprintf("write message body: %v", err), Temporary: true}
	}
	if err := w.Close(); err != nil {
		return tempOrPerm("end of DATA", err)
	}

	// The message has been accepted; a failed QUIT cannot change the outcome.
	_ = cl.Quit()
	return nil
}

// Probe checks upstream connectivity by establishing a session up to EHLO,
// without authenticating or sending anything.
func (c *Client) Probe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", c.addr())
	if err != nil {
		return fmt.Errorf("dial upstream: %w", err)
	}
	defer conn.Close()

	cl, err := smtp.NewClient(conn, c.host)
	if err != nil {
		return fmt.Errorf("upstream greeting: %w", err)
	}
	if err := cl.Hello(c.hello); err != nil {
		return fmt.Errorf("EHLO upstream: %w", err)
	}
	if err := cl.Quit(); err != nil {
		return fmt.Errorf("quit upstream: %w", err)
	}
	return nil
}

// tempOrPerm classifies an upstream reply: SMTP 4xx replies and any
// connection-level failure are temporary; 5xx replies are permanent.
func tempOrPerm(stage string, err error) error {
	te := &Error{}
	var textErr *textproto.Error
	switch {
	case errors.As(err, &textErr):
		te.Code = textErr.Code
		te.Message = strings.TrimSpace(textErr.Msg)
		te.Temporary = textErr.Code >= 400 && textErr.Code < 500
	default:
		te.Message = fmt.Sprintf("%s: %v", stage, err)
		te.Temporary = true
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		te.Temporary = true
	}
	return te
}
