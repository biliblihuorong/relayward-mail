package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
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

// MessageView is a log row joined with the app name it belongs to. Soft
// deleted apps keep their rows, so the name always resolves.
type MessageView struct {
	Message
	AppName string
}

// InsertMessage records one delivered (or rejected) recipient. Logging is
// fire-and-forget from the relay path and never carries an audit entry.
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

// MessageFilter selects a page of the message log. Zero-value fields are
// ignored; Since/Until bound ts.
type MessageFilter struct {
	App    string // app name
	To     string // recipient, normalized before the query
	Status string
	Since  *time.Time
	Until  *time.Time
	Limit  int
	Cursor string
}

// ListMessages returns one page of log rows, newest first, plus the cursor
// for the next page ("" when exhausted).
func (s *Store) ListMessages(ctx context.Context, f MessageFilter) ([]MessageView, string, error) {
	limit, err := pageLimit(f.Limit)
	if err != nil {
		return nil, "", err
	}

	where := []string{"1=1"}
	args := []any{}
	if f.App != "" {
		where = append(where, "a.name = ?")
		args = append(args, f.App)
	}
	if f.To != "" {
		where = append(where, "m.rcpt_to = ?")
		args = append(args, NormalizeEmail(f.To))
	}
	if f.Status != "" {
		switch f.Status {
		case StatusSent, StatusSuppressed, StatusRateLimited, StatusFailed:
		default:
			return nil, "", fmt.Errorf("%w: unknown status %q", ErrInvalidInput, f.Status)
		}
		where = append(where, "m.status = ?")
		args = append(args, f.Status)
	}
	if f.Since != nil {
		where = append(where, "m.ts >= ?")
		args = append(args, f.Since.UTC().Unix())
	}
	if f.Until != nil {
		where = append(where, "m.ts <= ?")
		args = append(args, f.Until.UTC().Unix())
	}
	if f.Cursor != "" {
		cursor, err := parseCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		where = append(where, "m.id < ?")
		args = append(args, cursor)
	}
	args = append(args, limit+1)

	rows, err := s.db.QueryContext(ctx,
		`SELECT m.id, m.ts, m.mail_from, m.rcpt_to, m.subject, m.size, m.message_id, m.status, m.upstream_resp, m.client_ip, a.name
		 FROM messages m JOIN apps a ON a.id = m.app_id
		 WHERE `+joinAnd(where)+` ORDER BY m.id DESC LIMIT ?`, args...) // #nosec G202 -- where is built from fixed fragments, values are parameterized
	if err != nil {
		return nil, "", fmt.Errorf("list messages: %w", err)
	}
	defer rows.Close()

	var views []MessageView
	for rows.Next() {
		var v MessageView
		var ts sql.NullInt64
		var subject, messageID, upstreamResp, clientIP sql.NullString
		var size sql.NullInt64
		if err := rows.Scan(&v.ID, &ts, &v.MailFrom, &v.RcptTo, &subject, &size, &messageID,
			&v.Status, &upstreamResp, &clientIP, &v.AppName); err != nil {
			return nil, "", fmt.Errorf("scan message: %w", err)
		}
		v.Ts = time.Unix(ts.Int64, 0).UTC()
		if subject.Valid {
			v.Subject = subject.String
		}
		if size.Valid {
			v.Size = size.Int64
		}
		if messageID.Valid {
			v.MessageID = messageID.String
		}
		if upstreamResp.Valid {
			v.UpstreamResp = upstreamResp.String
		}
		if clientIP.Valid {
			v.ClientIP = clientIP.String
		}
		views = append(views, v)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("list messages: %w", err)
	}

	next := ""
	if len(views) > limit {
		views = views[:limit]
		next = strconv.FormatInt(views[len(views)-1].ID, 10)
	}
	return views, next, nil
}

// AppStats aggregates the message log for one app.
type AppStats struct {
	AppID       int64  `json:"app_id"`
	App         string `json:"app"`
	Total       int64  `json:"total"`
	Sent        int64  `json:"sent"`
	Failed      int64  `json:"failed"`
	RateLimited int64  `json:"rate_limited"`
	Suppressed  int64  `json:"suppressed"`
}

// AppStats returns per-app totals for every active app, ordered by name.
func (s *Store) AppStats(ctx context.Context) ([]AppStats, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT a.id, a.name, COUNT(m.id),
		        COALESCE(SUM(m.status = ?), 0), COALESCE(SUM(m.status = ?), 0),
		        COALESCE(SUM(m.status = ?), 0), COALESCE(SUM(m.status = ?), 0)
		 FROM apps a LEFT JOIN messages m ON m.app_id = a.id
		 WHERE a.deleted_at IS NULL
		 GROUP BY a.id ORDER BY a.name`,
		StatusSent, StatusFailed, StatusRateLimited, StatusSuppressed)
	if err != nil {
		return nil, fmt.Errorf("app stats: %w", err)
	}
	defer rows.Close()

	var stats []AppStats
	for rows.Next() {
		var st AppStats
		if err := rows.Scan(&st.AppID, &st.App, &st.Total, &st.Sent, &st.Failed, &st.RateLimited, &st.Suppressed); err != nil {
			return nil, fmt.Errorf("scan app stats: %w", err)
		}
		stats = append(stats, st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("app stats: %w", err)
	}
	return stats, nil
}

// AppStatsByName returns the stats of one active app, or ErrNotFound.
func (s *Store) AppStatsByName(ctx context.Context, name string) (*AppStats, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT a.id, a.name, COUNT(m.id),
		        COALESCE(SUM(m.status = ?), 0), COALESCE(SUM(m.status = ?), 0),
		        COALESCE(SUM(m.status = ?), 0), COALESCE(SUM(m.status = ?), 0)
		 FROM apps a LEFT JOIN messages m ON m.app_id = a.id
		 WHERE a.deleted_at IS NULL AND a.name = ?
		 GROUP BY a.id`,
		StatusSent, StatusFailed, StatusRateLimited, StatusSuppressed, name)

	var st AppStats
	err := row.Scan(&st.AppID, &st.App, &st.Total, &st.Sent, &st.Failed, &st.RateLimited, &st.Suppressed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("app stats %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("app stats %q: %w", name, err)
	}
	return &st, nil
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

func joinAnd(parts []string) string {
	out := parts[0]
	for _, p := range parts[1:] {
		out += " AND " + p
	}
	return out
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
