package store

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"time"
)

// Audit actions recorded in audit_log. Management writes and their audit
// entries commit in the same transaction.
const (
	ActionAppCreate   = "app.create"
	ActionAppUpdate   = "app.update"
	ActionAppRotate   = "app.rotate"
	ActionAppDelete   = "app.delete"
	ActionTokenCreate = "token.create"
	ActionTokenRevoke = "token.revoke"
	ActionTokenReset  = "token.reset"
)

// AuditEntry describes one management write for the audit log.
type AuditEntry struct {
	TokenID   *int64
	TokenName string
	IP        string
	Action    string
	Target    string
	Detail    string // JSON object or ""
}

// AuditLogEntry is one row of audit_log as returned by ListAuditLogs.
type AuditLogEntry struct {
	ID        int64
	Ts        time.Time
	TokenID   *int64
	TokenName *string
	IP        *string
	Action    string
	Target    *string
	Detail    *string
}

// executor is the subset of *sql.DB / *sql.Tx used by the mutation helpers.
type executor interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// runWithAudit runs write inside a transaction together with the audit
// insert, so that the audit row commits atomically with the write it
// describes. A nil audit skips the audit insert (startup tasks).
func (s *Store) runWithAudit(ctx context.Context, audit *AuditEntry, write func(tx executor) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	if err := write(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if audit != nil {
		if err := insertAudit(ctx, tx, audit); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit tx: %w", err)
	}
	return nil
}

func insertAudit(ctx context.Context, tx executor, a *AuditEntry) error {
	var tokenID any
	if a.TokenID != nil {
		tokenID = *a.TokenID
	}
	_, err := tx.ExecContext(ctx,
		`INSERT INTO audit_log (ts, token_id, token_name, ip, action, target, detail)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		time.Now().UTC().Unix(), tokenID, nullString(a.TokenName),
		nullString(a.IP), a.Action, nullString(a.Target), nullString(a.Detail))
	if err != nil {
		return fmt.Errorf("insert audit log: %w", err)
	}
	return nil
}

// AuditFilter selects a page of audit_log.
type AuditFilter struct {
	Limit  int
	Cursor string
}

// ListAuditLogs returns one page of audit entries, newest first, plus the
// cursor for the next page ("" when exhausted).
func (s *Store) ListAuditLogs(ctx context.Context, f AuditFilter) ([]AuditLogEntry, string, error) {
	limit, err := pageLimit(f.Limit)
	if err != nil {
		return nil, "", err
	}
	where := "1=1"
	args := []any{}
	if f.Cursor != "" {
		cursor, err := parseCursor(f.Cursor)
		if err != nil {
			return nil, "", err
		}
		where = "id < ?"
		args = append(args, cursor)
	}
	args = append(args, limit+1)

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, ts, token_id, token_name, ip, action, target, detail
		 FROM audit_log WHERE `+where+` ORDER BY id DESC LIMIT ?`, args...) // #nosec G202 -- where is built from fixed fragments, values are parameterized
	if err != nil {
		return nil, "", fmt.Errorf("list audit logs: %w", err)
	}
	defer rows.Close()

	var entries []AuditLogEntry
	for rows.Next() {
		var e AuditLogEntry
		var ts sql.NullInt64
		var tokenID sql.NullInt64
		var tokenName, ip, target, detail sql.NullString
		if err := rows.Scan(&e.ID, &ts, &tokenID, &tokenName, &ip, &e.Action, &target, &detail); err != nil {
			return nil, "", fmt.Errorf("scan audit log: %w", err)
		}
		e.Ts = time.Unix(ts.Int64, 0).UTC()
		if tokenID.Valid {
			id := tokenID.Int64
			e.TokenID = &id
		}
		if tokenName.Valid {
			name := tokenName.String
			e.TokenName = &name
		}
		if ip.Valid {
			v := ip.String
			e.IP = &v
		}
		if target.Valid {
			v := target.String
			e.Target = &v
		}
		if detail.Valid {
			v := detail.String
			e.Detail = &v
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, "", fmt.Errorf("list audit logs: %w", err)
	}

	next := ""
	if len(entries) > limit {
		entries = entries[:limit]
		next = strconv.FormatInt(entries[len(entries)-1].ID, 10)
	}
	return entries, next, nil
}

// pageLimit normalizes a page size: default 50, maximum 200.
func pageLimit(limit int) (int, error) {
	if limit == 0 {
		return 50, nil
	}
	if limit < 0 || limit > 200 {
		return 0, fmt.Errorf("%w: limit must be between 1 and 200", ErrInvalidInput)
	}
	return limit, nil
}

// parseCursor decodes an opaque cursor (a row id).
func parseCursor(cursor string) (int64, error) {
	id, err := strconv.ParseInt(cursor, 10, 64)
	if err != nil || id < 0 {
		return 0, fmt.Errorf("%w: malformed cursor", ErrInvalidInput)
	}
	return id, nil
}
