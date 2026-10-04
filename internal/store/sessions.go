package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// AdminSessionTTL is how long one management-page login stays valid. Fixed
// from creation, not sliding: operators re-login daily, scripts use bearer
// tokens.
const AdminSessionTTL = 24 * time.Hour

// AdminSession is one browser login on the management API. The cookie value
// is a random secret; only its SHA-256 hash (TokenHash) is stored.
type AdminSession struct {
	ID        int64
	TokenHash string // SHA-256 hex of the cookie value
	TokenID   int64
	CreatedAt time.Time
	ExpiresAt time.Time
	LastSeen  time.Time
}

// AdminSessionView is a session joined with the admin token it was created
// from. The join failing (token revoked) invalidates the session.
type AdminSessionView struct {
	AdminSession
	TokenName string
	Role      string
}

// ActionSessionCreate is the audit action for one successful page login.
const ActionSessionCreate = "session.create"

// CreateAdminSession inserts a login session; the audit entry, when given,
// commits in the same transaction. Expired rows are pruned opportunistically
// so the table stays small without a dedicated job.
func (s *Store) CreateAdminSession(ctx context.Context, sess *AdminSession, audit *AuditEntry) error {
	if sess.TokenHash == "" {
		return fmt.Errorf("create admin session: token hash is required")
	}
	if sess.TokenID <= 0 {
		return fmt.Errorf("create admin session: token id must be positive")
	}
	now := time.Now().UTC()
	if sess.ExpiresAt.IsZero() {
		sess.ExpiresAt = now.Add(AdminSessionTTL)
	}

	err := s.runWithAudit(ctx, audit, func(tx executor) error {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM admin_sessions WHERE expires_at < ?`, now.Unix()); err != nil {
			return fmt.Errorf("prune expired sessions: %w", err)
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO admin_sessions (token_hash, token_id, created_at, expires_at, last_seen)
			 VALUES (?, ?, ?, ?, ?)`,
			sess.TokenHash, sess.TokenID, now.Unix(), sess.ExpiresAt.Unix(), now.Unix())
		if err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("create admin session: %w", ErrConflict)
			}
			return fmt.Errorf("create admin session: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("create admin session: last insert id: %w", err)
		}
		sess.ID = id
		sess.CreatedAt = now
		sess.LastSeen = now
		return nil
	})
	return err
}

// GetAdminSession returns the live session with the given hash, joined with
// its admin token. Expired sessions and sessions whose token has been
// revoked both resolve to ErrNotFound; an expired row is deleted lazily.
func (s *Store) GetAdminSession(ctx context.Context, tokenHash string) (*AdminSessionView, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT s.id, s.token_hash, s.token_id, s.created_at, s.expires_at, s.last_seen, t.name, t.role
		 FROM admin_sessions s JOIN admin_tokens t ON t.id = s.token_id
		 WHERE s.token_hash = ?`, tokenHash)

	var v AdminSessionView
	var created, expires, lastSeen int64
	err := row.Scan(&v.ID, &v.TokenHash, &v.TokenID, &created, &expires, &lastSeen, &v.TokenName, &v.Role)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("get admin session: %w", ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get admin session: %w", err)
	}
	v.CreatedAt = time.Unix(created, 0).UTC()
	v.ExpiresAt = time.Unix(expires, 0).UTC()
	v.LastSeen = time.Unix(lastSeen, 0).UTC()

	if time.Now().After(v.ExpiresAt) {
		_, _ = s.db.ExecContext(ctx, `DELETE FROM admin_sessions WHERE id = ?`, v.ID)
		return nil, fmt.Errorf("get admin session: %w", ErrNotFound)
	}
	return &v, nil
}

// TouchAdminSession refreshes last_seen; callers throttle the writes.
func (s *Store) TouchAdminSession(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE admin_sessions SET last_seen = ? WHERE id = ?`, time.Now().UTC().Unix(), id)
	if err != nil {
		return fmt.Errorf("touch admin session %d: %w", id, err)
	}
	return nil
}

// DeleteAdminSession removes one session by hash (logout). A missing row is
// not an error: logging out twice must stay idempotent.
func (s *Store) DeleteAdminSession(ctx context.Context, tokenHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM admin_sessions WHERE token_hash = ?`, tokenHash)
	if err != nil {
		return fmt.Errorf("delete admin session: %w", err)
	}
	return nil
}
