package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"
)

// Message statuses recorded in the messages table.
const (
	StatusSent        = "sent"
	StatusSuppressed  = "suppressed"
	StatusRateLimited = "rate_limited"
	StatusFailed      = "failed"
)

// Message is one log row per accepted recipient. Only metadata is stored,
// never the message body.
type Message struct {
	ID           int64
	AppID        int64
	Ts           time.Time
	MailFrom     string
	RcptTo       string
	Subject      string
	Size         int64
	MessageID    string
	Status       string
	UpstreamResp string
	ClientIP     string
}

// InsertMessage records one delivered (or rejected) recipient.
func (s *Store) InsertMessage(ctx context.Context, msg *Message) error {
	ts := msg.Ts
	if ts.IsZero() {
		ts = time.Now().UTC()
	}
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO messages (app_id, ts, mail_from, rcpt_to, subject, size, message_id, status, upstream_resp, client_ip)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		msg.AppID, ts.Unix(), msg.MailFrom, NormalizeEmail(msg.RcptTo),
		nullString(msg.Subject), nullInt(msg.Size), nullString(msg.MessageID),
		msg.Status, nullString(msg.UpstreamResp), nullString(msg.ClientIP))
	if err != nil {
		return fmt.Errorf("insert message: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("insert message: last insert id: %w", err)
	}
	msg.ID = id
	msg.Ts = ts
	return nil
}

// LastSuccessfulRelayTime returns the timestamp of the most recent message
// with status sent, or nil when nothing has been relayed yet.
func (s *Store) LastSuccessfulRelayTime(ctx context.Context) (*time.Time, error) {
	var unix sql.NullInt64
	err := s.db.QueryRowContext(ctx,
		`SELECT MAX(ts) FROM messages WHERE status = ?`, StatusSent).Scan(&unix)
	if err != nil {
		return nil, fmt.Errorf("last successful relay time: %w", err)
	}
	if !unix.Valid || unix.Int64 == 0 {
		return nil, nil
	}
	ts := time.Unix(unix.Int64, 0).UTC()
	return &ts, nil
}

// PruneMessages deletes message log rows older than cutoff and returns the
// number of deleted rows.
func (s *Store) PruneMessages(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM messages WHERE ts < ?`, cutoff.Unix())
	if err != nil {
		return 0, fmt.Errorf("prune messages: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("prune messages: rows affected: %w", err)
	}
	return n, nil
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}
