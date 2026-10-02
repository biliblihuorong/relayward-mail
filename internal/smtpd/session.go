package smtpd

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/mail"
	"strings"
	"time"

	"github.com/emersion/go-sasl"
	smtp "github.com/emersion/go-smtp"

	"relayward-mail/internal/relay"
	"relayward-mail/internal/store"
	"relayward-mail/internal/unsub"
)

// dbTimeout bounds a single store operation on the relay path.
const dbTimeout = 10 * time.Second

// Error responses shared by all sessions. They are *smtp.SMTPError so that
// go-smtp uses the exact status codes required by the plan.
var (
	errAuthFailed  = &smtp.SMTPError{Code: 535, EnhancedCode: smtp.EnhancedCode{5, 7, 8}, Message: "authentication failed"}
	errMustAuth    = &smtp.SMTPError{Code: 530, EnhancedCode: smtp.EnhancedCode{5, 7, 0}, Message: "authentication required"}
	errBadSender   = &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 7, 1}, Message: "sender address not allowed"}
	errTempRelay   = &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: "temporarily unable to relay, try again later"}
	errPermRelay   = &smtp.SMTPError{Code: 554, EnhancedCode: smtp.EnhancedCode{5, 3, 0}, Message: "message not accepted by upstream provider"}
	errRateLimited = &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 1}, Message: "rate limit exceeded, try again later"}
)

// session is one SMTP connection for one client program.
type session struct {
	backend  *Backend
	remoteIP string

	app      *store.App
	mailFrom string
	rcpts    []string
}

var (
	_ smtp.Session     = (*session)(nil)
	_ smtp.AuthSession = (*session)(nil)
)

// AuthMechanisms implements smtp.AuthSession; the plan allows PLAIN and LOGIN.
func (s *session) AuthMechanisms() []string {
	return []string{sasl.Plain, sasl.Login}
}

// Auth implements smtp.AuthSession.
func (s *session) Auth(mech string) (sasl.Server, error) {
	switch mech {
	case sasl.Plain:
		return sasl.NewPlainServer(func(_, username, password string) error {
			return s.authenticate(username, password)
		}), nil
	case sasl.Login:
		return &loginServer{auth: s.authenticate}, nil
	default:
		return nil, smtp.ErrAuthUnknownMechanism
	}
}

// authenticate checks the credentials and marks the session as authenticated.
// Every failure maps to the same 535 response so that the reply does not
// reveal whether the account exists, is disabled, or the IP is banned.
func (s *session) authenticate(username, password string) error {
	if s.backend.lockout != nil && s.backend.lockout.Banned(s.remoteIP) {
		return errAuthFailed
	}

	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	app, err := s.backend.store.GetAppByName(ctx, username)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			// Log storage problems, but keep the same outward response.
			s.backend.logger.Error("authenticate: load app", slog.String("app", username), slog.String("err", err.Error()))
		}
		s.recordAuthFailure()
		return errAuthFailed
	}
	if !app.Enabled {
		s.recordAuthFailure()
		return errAuthFailed
	}
	ok, err := store.VerifyPassword(app.PasswordHash, password)
	if err != nil || !ok {
		s.recordAuthFailure()
		return errAuthFailed
	}
	if s.backend.lockout != nil {
		s.backend.lockout.Reset(s.remoteIP)
	}

	s.app = app
	s.backend.logger.Info("smtp authenticated",
		slog.String("app", app.Name), slog.String("remote_ip", s.remoteIP))
	return nil
}

func (s *session) recordAuthFailure() {
	if s.backend.lockout != nil {
		s.backend.lockout.RecordFailure(s.remoteIP)
	}
}

// Mail implements smtp.Session.
func (s *session) Mail(from string, _ *smtp.MailOptions) error {
	if s.app == nil {
		return errMustAuth
	}

	addr, err := mail.ParseAddress(strings.Trim(from, "<>"))
	if err != nil {
		return &smtp.SMTPError{Code: 501, EnhancedCode: smtp.EnhancedCode{5, 1, 7}, Message: "invalid sender address"}
	}
	if !senderAllowed(s.app.AllowedFrom, addr.Address) {
		s.backend.logger.Warn("smtp sender rejected",
			slog.String("app", s.app.Name),
			slog.String("from", addr.Address),
			slog.String("remote_ip", s.remoteIP))
		return errBadSender
	}

	s.mailFrom = addr.Address
	s.rcpts = nil
	return nil
}

// senderAllowed reports whether addr matches one of the app's allowed sender
// addresses, compared in normalized form.
func senderAllowed(allowed []string, addr string) bool {
	normalized := store.NormalizeEmail(addr)
	for _, a := range allowed {
		if store.NormalizeEmail(a) == normalized {
			return true
		}
	}
	return false
}

// Rcpt implements smtp.Session.
func (s *session) Rcpt(to string, _ *smtp.RcptOptions) error {
	if s.app == nil {
		return errMustAuth
	}
	addr, err := mail.ParseAddress(strings.Trim(to, "<>"))
	if err != nil {
		return &smtp.SMTPError{Code: 501, EnhancedCode: smtp.EnhancedCode{5, 1, 3}, Message: "invalid recipient address"}
	}
	s.rcpts = append(s.rcpts, addr.Address)
	return nil
}

// Data implements smtp.Session: forward the message to the upstream provider
// and log one row per recipient. Apps with the unsubscribe switch on are
// split per recipient, so every copy carries its own List-Unsubscribe link.
func (s *session) Data(r io.Reader) error {
	if s.app == nil {
		return errMustAuth
	}
	if len(s.rcpts) == 0 {
		return &smtp.SMTPError{Code: 503, EnhancedCode: smtp.EnhancedCode{5, 5, 1}, Message: "no valid recipients"}
	}
	if s.backend.relay == nil {
		return errTempRelay
	}
	if err := s.backend.beginTransfer(); err != nil {
		return err
	}
	defer s.backend.endTransfer()

	start := time.Now()
	data, err := io.ReadAll(r)
	if err != nil {
		return errTempRelay
	}

	meta := parseMessageMeta(data)
	size := int64(len(data))

	// Per-app hourly limit: reject the whole transaction with 451 so the
	// client program retries later, and log rate_limited rows.
	if s.backend.limiter != nil && !s.backend.limiter.Allow(s.app.ID, s.app.RatePerHour) {
		s.logAndStore(store.StatusRateLimited, "", meta, size, time.Since(start))
		s.backend.logger.Warn("rate limited",
			slog.String("app", s.app.Name),
			slog.Int("rate_per_hour", s.app.RatePerHour),
			slog.String("remote_ip", s.remoteIP))
		return errRateLimited
	}

	if s.app.Unsubscribe {
		if s.backend.unsub == nil || s.backend.baseURL == "" {
			s.backend.logger.Error("unsubscribe enabled but gateway misconfigured",
				slog.String("app", s.app.Name))
			return errTempRelay
		}
		return s.relaySplit(data, meta, size, start)
	}
	return s.relayAsIs(data, meta, size, start)
}

// relayAsIs forwards one copy to all recipients untouched (apps with the
// unsubscribe switch off).
func (s *session) relayAsIs(data []byte, meta messageMeta, size int64, start time.Time) error {
	relayErr := s.backend.relay.Send(context.Background(), s.mailFrom, s.rcpts, data)

	status, resp := store.StatusSent, ""
	if relayErr != nil {
		status, resp = store.StatusFailed, relayErr.Error()
	}
	s.logAndStore(status, resp, meta, size, time.Since(start))
	return relayOutcome(relayErr)
}

// relaySplit forwards one copy per recipient: recipients with an unsubscribe
// record are suppressed (logged as such, still a 250 to the client so it does
// not retry), the others each receive their own copy with their own
// unsubscribe link. Any upstream failure maps like the single-copy path so
// the client retries; already delivered copies may then arrive twice, which
// the first version accepts (see DECISIONS.md).
func (s *session) relaySplit(data []byte, meta messageMeta, size int64, start time.Time) error {
	stripped := unsub.RemoveBcc(data)

	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	var firstErr error
	for _, rcpt := range s.rcpts {
		normalized := store.NormalizeEmail(rcpt)

		suppressed, err := s.backend.store.IsUnsubscribed(ctx, s.app.ID, normalized)
		if err != nil {
			s.backend.logger.Error("unsubscribe lookup",
				slog.String("app", s.app.Name), slog.String("rcpt", normalized), slog.String("err", err.Error()))
			return errTempRelay
		}
		if suppressed {
			s.backend.logger.Info("recipient suppressed",
				slog.String("app", s.app.Name), slog.String("rcpt", normalized))
			s.logOne(store.StatusSuppressed, "", meta, size, rcpt, time.Since(start))
			continue
		}

		token, err := s.backend.unsub.Token(s.app.ID, normalized)
		if err != nil {
			s.backend.logger.Error("build unsubscribe token",
				slog.String("app", s.app.Name), slog.String("rcpt", normalized), slog.String("err", err.Error()))
			return errTempRelay
		}
		msgData, err := unsub.AddUnsubscribeHeaders(stripped, s.backend.baseURL+"/u/"+token)
		if err != nil {
			s.backend.logger.Error("inject unsubscribe headers",
				slog.String("app", s.app.Name), slog.String("rcpt", normalized), slog.String("err", err.Error()))
			return errTempRelay
		}

		sendErr := s.backend.relay.Send(context.Background(), s.mailFrom, []string{rcpt}, msgData)
		if sendErr != nil {
			s.logOne(store.StatusFailed, sendErr.Error(), meta, size, rcpt, time.Since(start))
			if firstErr == nil {
				firstErr = sendErr
			}
			continue
		}
		s.logOne(store.StatusSent, "", meta, size, rcpt, time.Since(start))
	}
	return relayOutcome(firstErr)
}

// relayOutcome maps an upstream relay error to the SMTP reply: permanent
// upstream failures answer 554, everything else 451 so the client retries.
func relayOutcome(relayErr error) error {
	if relayErr == nil {
		return nil
	}
	if re := (*relay.Error)(nil); errors.As(relayErr, &re) && !re.Temporary {
		return errPermRelay
	}
	return errTempRelay
}

// logAndStore records the outcome for every recipient of the transaction.
func (s *session) logAndStore(status, resp string, meta messageMeta, size int64, duration time.Duration) {
	for _, rcpt := range s.rcpts {
		s.logOne(status, resp, meta, size, rcpt, duration)
	}
}

// logOne records one recipient outcome: a messages row plus a structured
// log line.
func (s *session) logOne(status, resp string, meta messageMeta, size int64, rcpt string, duration time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), dbTimeout)
	defer cancel()

	logger := s.backend.logger.With(
		slog.String("app", s.app.Name),
		slog.String("rcpt", rcpt),
		slog.String("status", status),
		slog.Int64("size", size),
		slog.String("msg_id", meta.MessageID),
		slog.String("remote_ip", s.remoteIP),
		slog.Int64("duration_ms", duration.Milliseconds()),
	)

	msg := &store.Message{
		AppID:        s.app.ID,
		MailFrom:     s.mailFrom,
		RcptTo:       rcpt,
		Subject:      meta.Subject,
		Size:         size,
		MessageID:    meta.MessageID,
		Status:       status,
		UpstreamResp: resp,
		ClientIP:     s.remoteIP,
	}
	if err := s.backend.store.InsertMessage(ctx, msg); err != nil {
		logger.Error("store message log", slog.String("err", err.Error()))
	}
	logger.Info("message processed")
}

// Reset implements smtp.Session: discard the current transaction but keep
// the authenticated app.
func (s *session) Reset() {
	s.mailFrom = ""
	s.rcpts = nil
}

// Logout implements smtp.Session.
func (s *session) Logout() error { return nil }

// messageMeta carries the header fields kept in the message log.
type messageMeta struct {
	Subject   string
	MessageID string
}

// parseMessageMeta extracts Subject and Message-Id; unparsable messages are
// still relayed, they just log without metadata.
func parseMessageMeta(data []byte) messageMeta {
	msg, err := mail.ReadMessage(strings.NewReader(string(data)))
	if err != nil {
		return messageMeta{}
	}
	return messageMeta{
		Subject:   strings.TrimSpace(msg.Header.Get("Subject")),
		MessageID: strings.TrimSpace(msg.Header.Get("Message-Id")),
	}
}
