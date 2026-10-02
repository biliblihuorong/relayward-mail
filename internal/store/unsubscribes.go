package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// How an unsubscribe record came into existence.
const (
	SourceLink     = "link"      // recipient clicked the link in the mail footer
	SourceOneClick = "one_click" // RFC 8058 one-click POST from the mail client
	SourceAPI      = "api"       // manual entry through the management API
)

// Audit actions for unsubscribe writes.
const (
	ActionUnsubscribeCreate = "unsubscribe.create"
	ActionUnsubscribeDelete = "unsubscribe.delete"
)

// Unsubscribe is one suppressed (app, email) pair.
type Unsubscribe struct {
	ID     int64
	AppID  int64
	Email  string
	Ts     time.Time
	Source string
}

// UnsubscribeView is an unsubscribe row joined with the app name it belongs
// to. Rows of soft-deleted apps stay visible so history remains explainable.
type UnsubscribeView struct {
	Unsubscribe
	AppName string
}

// validUnsubscribeSources is the closed set accepted for the source column.
var validUnsubscribeSources = map[string]bool{
	SourceLink:     true,
	SourceOneClick: true,
	SourceAPI:      true,
}

// CreateUnsubscribe records (appID, email), which must already be normalized.
// When the pair is already recorded the existing row is returned unchanged
// with created=false, so the operation is idempotent. audit may be nil for
// non-management writes (the public unsubscribe page passes one anyway).
func (s *Store) CreateUnsubscribe(ctx context.Context, appID int64, email, source string, audit *AuditEntry) (*Unsubscribe, bool, error) {
	if appID <= 0 {
		return nil, false, fmt.Errorf("create unsubscribe: %w: app id must be positive", ErrInvalidInput)
	}
	if email == "" {
		return nil, false, fmt.Errorf("create unsubscribe: %w: email must not be empty", ErrInvalidInput)
	}
	if !validUnsubscribeSources[source] {
		return nil, false, fmt.Errorf("create unsubscribe: %w: unknown source %q", ErrInvalidInput, source)
	}

	var row *Unsubscribe
	created := false
	err := s.runWithAudit(ctx, audit, func(tx executor) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO unsubscribes (app_id, email, ts, source) VALUES (?, ?, ?, ?)
			 ON CONFLICT(app_id, email) DO NOTHING`,
			appID, email, time.Now().UTC().Unix(), source)
		if err != nil {
			return fmt.Errorf("create unsubscribe: %w", err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("create unsubscribe: rows affected: %w", err)
		}
		created = n > 0

		row, err = scanUnsubscribe(tx.QueryRowContext(ctx,
			`SELECT id, app_id, email, ts, source FROM unsubscribes WHERE app_id = ? AND email = ?`,
			appID, email))
		return err
	})
	if err != nil {
		return nil, false, err
	}
	return row, created, nil
}

// GetUnsubscribe returns the unsubscribe row for one (app, email) pair, or
// ErrNotFound.
func (s *Store) GetUnsubscribe(ctx context.Context, appID int64, email string) (*Unsubscribe, error) {
	return scanUnsubscribe(s.db.QueryRowContext(ctx,
		`SELECT id, app_id, email, ts, source FROM unsubscribes WHERE app_id = ? AND email = ?`,
		appID, email))
}

// IsUnsubscribed reports whether mail to (appID, email) must be suppressed.
func (s *Store) IsUnsubscribed(ctx context.Context, appID int64, email string) (bool, error) {
	_, err := s.GetUnsubscribe(ctx, appID, email)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, ErrNotFound):
		return false, nil
	default:
		return false, err
	}
}

// UnsubscribeFilter selects a page of the unsubscribe table. Zero-value
// fields are ignored.
type UnsubscribeFilter struct {
	App    string // app name
	Email  string // normalized before the query
	Limit  int
	Cursor string
}

// ListUnsubscribes returns one page, newest first, plus the cursor for the
// next page ("" when exhausted).
func (s *Store) ListUnsubscribes(ctx context.Context, f UnsubscribeFilter) ([]UnsubscribeView, string, error) {
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
	if f.Email != "" {
		where = append(where, "u.email = ?")
		args = append(args, NormalizeEmail(f.Email))
	}
	if f.Cursor != "" {
		cursor, err := parseCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		where = append(where, "u.id < ?")
		args = append(args, cursor)
	}
	args = append(args, limit+1)

	rows, err := s.db.QueryContext(ctx,
		`SELECT u.id, u.app_id, u.email, u.ts, u.source, a.name
		 FROM unsubscribes u JOIN apps a ON a.id = u.app_id
		 WHERE `+joinAnd(where)+` ORDER BY u.id DESC LIMIT ?`, args...) // #nosec G202 -- where is built from fixed fragments, values are parameterized
	if err != nil {
		return nil, "", fmt.Errorf("list unsubscribes: %w", err)
	}
	defer rows.Close()

	var views []UnsubscribeView
	for rows.Next() {
		var v UnsubscribeView
		var ts int64
		if err := rows.Scan(&v.ID, &v.AppID, &v.Email, &ts, &v.Source, &v.AppName); err != nil {
			return nil, "", fmt.Errorf("scan unsubscribe: %w", err)
		}
		v.Ts = time.Unix(ts, 0).UTC()
		views = append(views, v)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("list unsubscribes: %w", err)
	}

	next := ""
	if len(views) > limit {
		views = views[:limit]
		next = strconv.FormatInt(views[len(views)-1].ID, 10)
	}
	return views, next, nil
}

// DeleteUnsubscribe removes the unsubscribe row with the given id (a
// resubscription), or returns ErrNotFound.
func (s *Store) DeleteUnsubscribe(ctx context.Context, id int64, audit *AuditEntry) error {
	return s.runWithAudit(ctx, audit, func(tx executor) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM unsubscribes WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("delete unsubscribe %d: %w", id, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("delete unsubscribe %d: rows affected: %w", id, err)
		}
		if n == 0 {
			return fmt.Errorf("delete unsubscribe %d: %w", id, ErrNotFound)
		}
		return nil
	})
}

func scanUnsubscribe(row *sql.Row) (*Unsubscribe, error) {
	var u Unsubscribe
	var ts int64
	err := row.Scan(&u.ID, &u.AppID, &u.Email, &ts, &u.Source)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("get unsubscribe: %w", ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("scan unsubscribe: %w", err)
	}
	u.Ts = time.Unix(ts, 0).UTC()
	return &u, nil
}
