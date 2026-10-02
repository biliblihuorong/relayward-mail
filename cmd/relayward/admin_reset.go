package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"relayward-mail/internal/config"
	"relayward-mail/internal/store"
)

// adminReset implements `relayward admin reset`: revoke every admin token
// and generate a fresh initial one. Local only, never exposed via the API,
// per the plan's recovery path for lost tokens.
func adminReset(args []string) error {
	fs := flag.NewFlagSet("admin reset", flag.ExitOnError)
	configPath := fs.String("config", "config.yaml", "path to the configuration file")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(cfg.DataDir, dbFileName))
	if err != nil {
		return err
	}
	defer st.Close()

	ctx := context.Background()
	revoked, err := st.ResetAdminTokens(ctx, &store.AuditEntry{
		TokenName: "local-cli",
		IP:        "local",
		Action:    store.ActionTokenReset,
		Target:    "all",
	})
	if err != nil {
		return err
	}

	if err := os.Remove(filepath.Join(cfg.DataDir, initialAdminTokenFile)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove old initial token file: %w", err)
	}

	if err := ensureInitialAdminToken(ctx, st, cfg.DataDir, newLogger()); err != nil {
		return err
	}
	fmt.Printf("revoked %d token(s); a new initial admin token was generated and printed above\n", revoked)
	return nil
}
