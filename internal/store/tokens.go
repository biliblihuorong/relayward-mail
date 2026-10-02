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
// plaintext token, which is never stored.
func (s *Store) CreateAdminToken(ctx context.Context, tok *AdminToken) error {
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
	res, err := s.db.ExecContext(ctx,
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

	var tok AdminToken
	var expires, lastUsed, createdBy sql.NullInt64
	var created int64
	err := row.Scan(&tok.ID, &tok.Name, &tok.TokenHash, &tok.Role, &expires, &lastUsed, &createdBy, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("get admin token: %w", ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get admin token: %w", err)
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
