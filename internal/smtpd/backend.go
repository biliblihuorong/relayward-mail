// Package smtpd implements the ingress SMTP server: per-program
// authentication, MAIL FROM validation and message logging. Forwarding to
// the upstream provider lives in the relay package.
package smtpd

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"github.com/emersion/go-smtp"

	"relayward-mail/internal/relay"
	"relayward-mail/internal/store"
)

// Backend is the go-smtp backend shared by all SMTP sessions.
type Backend struct {
	store  *store.Store
	relay  *relay.Client
	logger *slog.Logger

	mu       sync.Mutex
	stopping bool
	inFlight sync.WaitGroup
}

// NewBackend builds a Backend. relay may be nil in tests that never call
// Data.
func NewBackend(st *store.Store, rl *relay.Client, logger *slog.Logger) *Backend {
	if logger == nil {
		logger = slog.Default()
	}
	return &Backend{store: st, relay: rl, logger: logger}
}

// NewSession implements smtp.Backend.
func (b *Backend) NewSession(conn *smtp.Conn) (smtp.Session, error) {
	remoteIP, _ := splitHostPort(conn.Conn().RemoteAddr())
	return &session{backend: b, remoteIP: remoteIP}, nil
}

// WaitIdle waits until no message transfer is in progress, or until ctx is
// done. Call it after closing the listener during shutdown.
func (b *Backend) WaitIdle(ctx context.Context) {
	done := make(chan struct{})
	go func() {
		b.inFlight.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		b.logger.Warn("shutdown grace period elapsed with transfers in flight")
	case <-done:
	}
}

// beginTransfer marks a DATA transfer as in flight, or reports an error if
// the server is shutting down.
func (b *Backend) beginTransfer() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopping {
		return &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 2}, Message: "shutting down, try again later"}
	}
	b.inFlight.Add(1)
	return nil
}

func (b *Backend) endTransfer() { b.inFlight.Done() }

// Stop accepting new transfers; used by main during shutdown after the
// listener is closed.
func (b *Backend) Stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stopping = true
}

func splitHostPort(addr net.Addr) (string, error) {
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "", fmt.Errorf("split remote address: %w", err)
	}
	return host, nil
}
