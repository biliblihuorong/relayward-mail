// Package smtpd implements the ingress SMTP server: per-program
// authentication, MAIL FROM validation and message logging. Forwarding to
// the upstream provider lives in the relay package.
package smtpd

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"sync"

	"github.com/emersion/go-smtp"

	"relayward-mail/internal/ratelimit"
	"relayward-mail/internal/relay"
	"relayward-mail/internal/store"
	"relayward-mail/internal/unsub"
)

// Backend is the go-smtp backend shared by all SMTP sessions. A nil limiter
// or lockout (tests) disables the corresponding throttle. unsub and baseURL
// power the per-recipient split: with unsubscribe enabled apps, a nil manager
// is a misconfiguration and message transfer fails with a temporary error.
type Backend struct {
	store   *store.Store
	relay   *relay.Client
	logger  *slog.Logger
	limiter *ratelimit.Limiter
	lockout *ratelimit.Lockout
	unsub   *unsub.Manager
	baseURL string

	mu       sync.Mutex
	stopping bool
	inFlight sync.WaitGroup
}

// NewBackend builds a Backend. unsubMgr may be nil only when no app uses the
// unsubscribe feature; publicBaseURL is the scheme+host the unsubscribe links
// point at (from public.base_url).
func NewBackend(st *store.Store, rl *relay.Client, limiter *ratelimit.Limiter, lockout *ratelimit.Lockout, logger *slog.Logger, unsubMgr *unsub.Manager, publicBaseURL string) *Backend {
	if logger == nil {
		logger = slog.Default()
	}
	return &Backend{
		store:   st,
		relay:   rl,
		logger:  logger,
		limiter: limiter,
		lockout: lockout,
		unsub:   unsubMgr,
		baseURL: strings.TrimSuffix(publicBaseURL, "/"),
	}
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
