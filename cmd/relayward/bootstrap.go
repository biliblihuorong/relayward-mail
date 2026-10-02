package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"relayward-mail/internal/store"
)

// initialAdminTokenFile holds the plaintext initial admin token inside the
// data directory.
const initialAdminTokenFile = "initial_admin_token"

// ensureInitialAdminToken generates the first super admin token when the
// database contains none. The plaintext is printed to the startup log once
// (the one log line sanctioned by the plan) and written to
// data_dir/initial_admin_token with mode 0600; only its SHA-256 hash is
// stored in the database.
func ensureInitialAdminToken(ctx context.Context, st *store.Store, dataDir string, logger *slog.Logger) error {
	has, err := st.HasAdminToken(ctx)
	if err != nil {
		return err
	}
	if has {
		return nil
	}

	raw, err := store.RandomToken("rw_admin_", 32)
	if err != nil {
		return err
	}
	tok := &store.AdminToken{
		Name:      "initial",
		Role:      store.RoleAdmin,
		TokenHash: store.HashToken(raw),
	}
	if err := st.CreateAdminToken(ctx, tok); err != nil {
		return err
	}

	path := filepath.Join(dataDir, initialAdminTokenFile)
	if err := os.WriteFile(path, []byte(raw+"\n"), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}

	logger.Info("generated initial admin token (shown once; revoke after use)",
		slog.String("token", raw),
		slog.String("file", path))
	return nil
}
