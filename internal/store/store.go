// Package store provides SQLite access for relayward: opening the database,
// applying migrations and all queries. No other package talks SQL.
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strconv"
	"strings"

	// Pure-Go SQLite driver; registered for its side effects.
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store wraps the SQLite database handle. The pool is limited to a single
// connection, which serializes writes and avoids SQLITE_BUSY surprises.
type Store struct {
	db *sql.DB
}

// Open opens (creating if needed) the SQLite database at path, enables WAL
// mode and applies all pending migrations.
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	s := &Store{db: db}
	if err := s.applyPragmas(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) applyPragmas() error {
	var mode string
	if err := s.db.QueryRow("PRAGMA journal_mode=WAL").Scan(&mode); err != nil {
		return fmt.Errorf("enable WAL mode: %w", err)
	}
	if !strings.EqualFold(mode, "wal") {
		return fmt.Errorf("enable WAL mode: got mode %q", mode)
	}
	if _, err := s.db.Exec("PRAGMA busy_timeout=5000"); err != nil {
		return fmt.Errorf("set busy_timeout: %w", err)
	}
	if _, err := s.db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		return fmt.Errorf("enable foreign_keys: %w", err)
	}
	return nil
}

// migrate applies every embedded migration whose version is newer than
// PRAGMA user_version, in filename order, each inside its own transaction.
func (s *Store) migrate() error {
	entries, err := fs.Glob(migrationsFS, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	sort.Strings(entries)

	current, err := s.userVersion()
	if err != nil {
		return err
	}

	for _, name := range entries {
		version, err := migrationVersion(name)
		if err != nil {
			return err
		}
		if version <= current {
			continue
		}
		script, err := migrationsFS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}

		tx, err := s.db.Begin()
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", name, err)
		}
		if _, err := tx.Exec(string(script)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
		if _, err := tx.Exec("PRAGMA user_version=" + strconv.Itoa(version)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("set user_version to %d: %w", version, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
		current = version
	}
	return nil
}

func (s *Store) userVersion() (int, error) {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return 0, fmt.Errorf("read user_version: %w", err)
	}
	return version, nil
}

func migrationVersion(name string) (int, error) {
	base := strings.TrimSuffix(strings.TrimPrefix(name, "migrations/"), ".sql")
	dash := strings.Index(base, "_")
	if dash <= 0 {
		return 0, fmt.Errorf("migration %s: file name must look like 0001_init.sql", name)
	}
	version, err := strconv.Atoi(base[:dash])
	if err != nil {
		return 0, fmt.Errorf("migration %s: bad version prefix", name)
	}
	return version, nil
}

// Close closes the database.
func (s *Store) Close() error {
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close sqlite: %w", err)
	}
	return nil
}

// Ping verifies database connectivity.
func (s *Store) Ping(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping sqlite: %w", err)
	}
	return nil
}
