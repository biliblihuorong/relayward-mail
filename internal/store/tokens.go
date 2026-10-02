package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Admin token roles, ordered from most to least privileged.
const (
	RoleAdmin    = "admin"
	RoleOperator = "operator"
	RoleViewer   = "viewer"
)

// AdminToken is a bearer credential for the management API.
type AdminToken struct {
	ID        int64
	Name      string
	TokenHash string // SHA-256 hex
	Role      string
	ExpiresAt *time.Time
	LastUsed  *time.Time
	CreatedBy *int64
	CreatedAt time.Time
}

// CreateAdminToken inserts a token; TokenHash must be the SHA-256 hex of the
// plaintext token, which is never stored. The audit entry, when given,
// commits in the same transaction.
func (s *Store) CreateAdminToken(ctx context.Context, tok *AdminToken, audit *AuditEntry) error {
	switch tok.Role {
	case RoleAdmin, RoleOperator, RoleViewer:
	default:
		return fmt.Errorf("create admin token: invalid role %q", tok.Role)
	}
	if tok.Name == "" {
		return fmt.Errorf("create admin token: name is required")
	}

	var expires, createdBy any
	if tok.ExpiresAt != nil {
		expires = tok.ExpiresAt.Unix()
	}
	if tok.CreatedBy != nil {
		createdBy = *tok.CreatedBy
	}
	now := time.Now().UTC()

	err := s.runWithAudit(ctx, audit, func(tx executor) error {
		res, err := tx.ExecContext(ctx,
			`INSERT INTO admin_tokens (name, token_hash, role, expires_at, created_by, created_at)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			tok.Name, tok.TokenHash, tok.Role, expires, createdBy, now.Unix())
		if err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("create admin token %q: %w", tok.Name, ErrConflict)
			}
			return fmt.Errorf("create admin token: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("create admin token: last insert id: %w", err)
		}
		tok.ID = id
		tok.CreatedAt = now
		return nil
	})
	return err
}

// ListAdminTokens returns every token, oldest first. Hashes are included for
// internal use; API responses must not expose them.
func (s *Store) ListAdminTokens(ctx context.Context) ([]AdminToken, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, token_hash, role, expires_at, last_used, created_by, created_at
		 FROM admin_tokens ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list admin tokens: %w", err)
	}
	defer rows.Close()

	var tokens []AdminToken
	for rows.Next() {
		tok, err := scanToken(rows)
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, *tok)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list admin tokens: %w", err)
	}
	return tokens, nil
}

// GetAdminTokenByID returns one token, or ErrNotFound.
func (s *Store) GetAdminTokenByID(ctx context.Context, id int64) (*AdminToken, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, token_hash, role, expires_at, last_used, created_by, created_at
		 FROM admin_tokens WHERE id = ?`, id)
	return scanToken(row)
}

// DeleteAdminToken revokes a token by removing its row. The audit entry, when
// given, commits in the same transaction.
func (s *Store) DeleteAdminToken(ctx context.Context, id int64, audit *AuditEntry) error {
	return s.runWithAudit(ctx, audit, func(tx executor) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM admin_tokens WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("delete admin token %d: %w", id, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("delete admin token %d: rows affected: %w", id, err)
		}
		if n == 0 {
			return fmt.Errorf("delete admin token %d: %w", id, ErrNotFound)
		}
		return nil
	})
}

// ResetAdminTokens revokes every token and returns how many were removed.
// Used by the local `admin reset` command when all tokens are lost.
func (s *Store) ResetAdminTokens(ctx context.Context, audit *AuditEntry) (int64, error) {
	var count int64
	err := s.runWithAudit(ctx, audit, func(tx executor) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM admin_tokens`)
		if err != nil {
			return fmt.Errorf("reset admin tokens: %w", err)
		}
		count, err = res.RowsAffected()
		if err != nil {
			return fmt.Errorf("reset admin tokens: rows affected: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return count, nil
}

// HasAdminToken reports whether at least one admin token exists.
func (s *Store) HasAdminToken(ctx context.Context) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM admin_tokens`).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("count admin tokens: %w", err)
	}
	return count > 0, nil
}

// GetAdminTokenByHash returns the token with the given SHA-256 hex hash, or
// ErrNotFound. Expired tokens are still returned; callers enforce expiry.
func (s *Store) GetAdminTokenByHash(ctx context.Context, tokenHash string) (*AdminToken, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, token_hash, role, expires_at, last_used, created_by, created_at
		 FROM admin_tokens WHERE token_hash = ?`, tokenHash)
	return scanToken(row)
}

// tokenScanner covers *sql.Row and *sql.Rows.
type tokenScanner interface {
	Scan(dest ...any) error
}

func scanToken(row tokenScanner) (*AdminToken, error) {
	var tok AdminToken
	var expires, lastUsed, createdBy sql.NullInt64
	var created int64
	err := row.Scan(&tok.ID, &tok.Name, &tok.TokenHash, &tok.Role, &expires, &lastUsed, &createdBy, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("get admin token: %w", ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("scan admin token: %w", err)
	}

	if expires.Valid {
		ts := time.Unix(expires.Int64, 0).UTC()
		tok.ExpiresAt = &ts
	}
	if lastUsed.Valid {
		ts := time.Unix(lastUsed.Int64, 0).UTC()
		tok.LastUsed = &ts
	}
	if createdBy.Valid {
		id := createdBy.Int64
		tok.CreatedBy = &id
	}
	tok.CreatedAt = time.Unix(created, 0).UTC()
	return &tok, nil
}

// TouchAdminToken records that the token was just used.
func (s *Store) TouchAdminToken(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE admin_tokens SET last_used = ? WHERE id = ?`, time.Now().UTC().Unix(), id)
	if err != nil {
		return fmt.Errorf("touch admin token %d: %w", id, err)
	}
	return nil
}
