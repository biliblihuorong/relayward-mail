package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"modernc.org/sqlite"
)

// Sentinel errors returned by queries; check with errors.Is.
var (
	ErrNotFound = errors.New("store: not found")
	ErrConflict = errors.New("store: conflict")
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
// argon2id hash.
func (s *Store) CreateApp(ctx context.Context, app *App) error {
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
	res, err := s.db.ExecContext(ctx,
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
}

func isUniqueViolation(err error) bool {
	var se *sqlite.Error
	if errors.As(err, &se) {
		return se.Code() == 2067 // SQLITE_CONSTRAINT_UNIQUE
	}
	return false
}

// GetAppByName returns the app whose name equals the SMTP username.
func (s *Store) GetAppByName(ctx context.Context, name string) (*App, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, name, password_hash, enabled, unsubscribe, allowed_from, rate_per_hour, display_name, created_at, updated_at
		 FROM apps WHERE name = ?`, name)

	var app App
	var allowedFrom string
	var created, updated int64
	err := row.Scan(&app.ID, &app.Name, &app.PasswordHash, &app.Enabled, &app.Unsubscribe,
		&allowedFrom, &app.RatePerHour, &app.DisplayName, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("get app %q: %w", name, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get app %q: %w", name, err)
	}
	if err := json.Unmarshal([]byte(allowedFrom), &app.AllowedFrom); err != nil {
		return nil, fmt.Errorf("get app %q: decode allowed_from: %w", name, err)
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
