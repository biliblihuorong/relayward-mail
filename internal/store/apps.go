package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"modernc.org/sqlite"
)

// Sentinel errors returned by queries; check with errors.Is.
var (
	ErrNotFound     = errors.New("store: not found")
	ErrConflict     = errors.New("store: conflict")
	ErrInvalidInput = errors.New("store: invalid input")
)

// appNamePattern restricts app names to safe SMTP usernames.
var appNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// App is a client program that sends mail through relayward. Its name is the
// SMTP username.
type App struct {
	ID           int64
	Name         string
	PasswordHash string
	Enabled      bool
	Unsubscribe  bool
	AllowedFrom  []string
	RatePerHour  int
	DisplayName  string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// CreateApp validates and inserts a new app. PasswordHash must already be an
// argon2id hash. When a soft-deleted app with the same name exists it is
// revived in place, so its message log stays joined to a live row.
func (s *Store) CreateApp(ctx context.Context, app *App, audit *AuditEntry) error {
	if !appNamePattern.MatchString(app.Name) {
		return fmt.Errorf("create app: invalid name %q", app.Name)
	}
	if len(app.AllowedFrom) == 0 {
		return fmt.Errorf("create app: allowed_from must not be empty")
	}

	allowedFrom, err := json.Marshal(app.AllowedFrom)
	if err != nil {
		return fmt.Errorf("create app: encode allowed_from: %w", err)
	}
	now := time.Now().UTC()

	err = s.runWithAudit(ctx, audit, func(tx executor) error {
		var revivedID, revivedCreated int64
		reviveErr := tx.QueryRowContext(ctx,
			`SELECT id, created_at FROM apps WHERE name = ? AND deleted_at IS NOT NULL`,
			app.Name).Scan(&revivedID, &revivedCreated)
		switch {
		case reviveErr == nil:
			if _, err := tx.ExecContext(ctx,
				`UPDATE apps SET password_hash = ?, enabled = ?, unsubscribe = ?, allowed_from = ?,
				    rate_per_hour = ?, display_name = ?, deleted_at = NULL, updated_at = ?
				  WHERE id = ?`,
				app.PasswordHash, boolToInt(app.Enabled), boolToInt(app.Unsubscribe),
				string(allowedFrom), app.RatePerHour, app.DisplayName, now.Unix(), revivedID); err != nil {
				return fmt.Errorf("revive app %q: %w", app.Name, err)
			}
			app.ID = revivedID
			app.CreatedAt = time.Unix(revivedCreated, 0).UTC()
			app.UpdatedAt = now
			return nil
		case !errors.Is(reviveErr, sql.ErrNoRows):
			return fmt.Errorf("check deleted app %q: %w", app.Name, reviveErr)
		}

		res, err := tx.ExecContext(ctx,
			`INSERT INTO apps (name, password_hash, enabled, unsubscribe, allowed_from, rate_per_hour, display_name, created_at, updated_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			app.Name, app.PasswordHash, boolToInt(app.Enabled), boolToInt(app.Unsubscribe),
			string(allowedFrom), app.RatePerHour, app.DisplayName, now.Unix(), now.Unix())
		if err != nil {
			if isUniqueViolation(err) {
				return fmt.Errorf("create app %q: %w", app.Name, ErrConflict)
			}
			return fmt.Errorf("create app: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("create app: last insert id: %w", err)
		}
		app.ID = id
		app.CreatedAt = now
		app.UpdatedAt = now
		return nil
	})
	return err
}

func isUniqueViolation(err error) bool {
	var se *sqlite.Error
	if errors.As(err, &se) {
		return se.Code() == 2067 // SQLITE_CONSTRAINT_UNIQUE
	}
	return false
}

// AppUpdate carries the fields PATCH may change; nil pointers leave the
// column unchanged. A non-nil AllowedFrom must not be empty.
type AppUpdate struct {
	Enabled     *bool
	Unsubscribe *bool
	RatePerHour *int
	AllowedFrom []string
	DisplayName *string
}

// UpdateApp applies the given changes to an active app and returns the
// updated row.
func (s *Store) UpdateApp(ctx context.Context, name string, upd AppUpdate, audit *AuditEntry) (*App, error) {
	if upd.AllowedFrom != nil && len(upd.AllowedFrom) == 0 {
		return nil, fmt.Errorf("update app: %w: allowed_from must not be empty", ErrInvalidInput)
	}

	sets := []string{"updated_at = ?"}
	args := []any{time.Now().UTC().Unix()}
	if upd.Enabled != nil {
		sets = append(sets, "enabled = ?")
		args = append(args, boolToInt(*upd.Enabled))
	}
	if upd.Unsubscribe != nil {
		sets = append(sets, "unsubscribe = ?")
		args = append(args, boolToInt(*upd.Unsubscribe))
	}
	if upd.RatePerHour != nil {
		if *upd.RatePerHour <= 0 {
			return nil, fmt.Errorf("update app: %w: rate_per_hour must be positive", ErrInvalidInput)
		}
		sets = append(sets, "rate_per_hour = ?")
		args = append(args, *upd.RatePerHour)
	}
	if upd.AllowedFrom != nil {
		encoded, err := json.Marshal(upd.AllowedFrom)
		if err != nil {
			return nil, fmt.Errorf("update app: encode allowed_from: %w", err)
		}
		sets = append(sets, "allowed_from = ?")
		args = append(args, string(encoded))
	}
	if upd.DisplayName != nil {
		sets = append(sets, "display_name = ?")
		args = append(args, *upd.DisplayName)
	}

	var updated *App
	err := s.runWithAudit(ctx, audit, func(tx executor) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE apps SET `+strings.Join(sets, ", ")+` WHERE name = ? AND deleted_at IS NULL`,
			append(args, name)...)
		if err != nil {
			return fmt.Errorf("update app %q: %w", name, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("update app %q: rows affected: %w", name, err)
		}
		if n == 0 {
			return fmt.Errorf("update app %q: %w", name, ErrNotFound)
		}
		updated, err = scanApp(tx.QueryRowContext(ctx,
			`SELECT id, name, password_hash, enabled, unsubscribe, allowed_from, rate_per_hour, display_name, created_at, updated_at
			 FROM apps WHERE name = ?`, name))
		return err
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

// SoftDeleteApp removes an app from the active set while keeping its row so
// message logs remain joinable.
func (s *Store) SoftDeleteApp(ctx context.Context, name string, audit *AuditEntry) error {
	return s.runWithAudit(ctx, audit, func(tx executor) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE apps SET deleted_at = ?, updated_at = ? WHERE name = ? AND deleted_at IS NULL`,
			time.Now().UTC().Unix(), time.Now().UTC().Unix(), name)
		if err != nil {
			return fmt.Errorf("delete app %q: %w", name, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("delete app %q: rows affected: %w", name, err)
		}
		if n == 0 {
			return fmt.Errorf("delete app %q: %w", name, ErrNotFound)
		}
		return nil
	})
}

// RotateAppPassword replaces the stored password hash of an active app.
func (s *Store) RotateAppPassword(ctx context.Context, name, passwordHash string, audit *AuditEntry) error {
	return s.runWithAudit(ctx, audit, func(tx executor) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE apps SET password_hash = ?, updated_at = ? WHERE name = ? AND deleted_at IS NULL`,
			passwordHash, time.Now().UTC().Unix(), name)
		if err != nil {
			return fmt.Errorf("rotate app password %q: %w", name, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("rotate app password %q: rows affected: %w", name, err)
		}
		if n == 0 {
			return fmt.Errorf("rotate app password %q: %w", name, ErrNotFound)
		}
		return nil
	})
}

// GetAppByName returns the active app whose name equals the SMTP username.
func (s *Store) GetAppByName(ctx context.Context, name string) (*App, error) {
	return scanApp(s.db.QueryRowContext(ctx,
		`SELECT id, name, password_hash, enabled, unsubscribe, allowed_from, rate_per_hour, display_name, created_at, updated_at
		 FROM apps WHERE name = ? AND deleted_at IS NULL`, name))
}

// ListApps returns all active apps ordered by name.
func (s *Store) ListApps(ctx context.Context) ([]App, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, name, password_hash, enabled, unsubscribe, allowed_from, rate_per_hour, display_name, created_at, updated_at
		 FROM apps WHERE deleted_at IS NULL ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list apps: %w", err)
	}
	defer rows.Close()

	var apps []App
	for rows.Next() {
		app, err := scanApp(rows)
		if err != nil {
			return nil, err
		}
		apps = append(apps, *app)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list apps: %w", err)
	}
	return apps, nil
}

// appScanner covers *sql.Row and *sql.Rows.
type appScanner interface {
	Scan(dest ...any) error
}

func scanApp(row appScanner) (*App, error) {
	var app App
	var allowedFrom string
	var created, updated int64
	err := row.Scan(&app.ID, &app.Name, &app.PasswordHash, &app.Enabled, &app.Unsubscribe,
		&allowedFrom, &app.RatePerHour, &app.DisplayName, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("get app: %w", ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("scan app: %w", err)
	}
	if err := json.Unmarshal([]byte(allowedFrom), &app.AllowedFrom); err != nil {
		return nil, fmt.Errorf("scan app: decode allowed_from: %w", err)
	}
	app.CreatedAt = time.Unix(created, 0).UTC()
	app.UpdatedAt = time.Unix(updated, 0).UTC()
	return &app, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
