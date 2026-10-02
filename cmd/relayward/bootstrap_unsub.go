package main

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"relayward-mail/internal/config"
)

// unsubscribeSecretFile is the data_dir file holding the auto-generated
// unsubscribe token secret.
const unsubscribeSecretFile = "unsubscribe_secret" // #nosec G101 -- this is a file name, not a credential

// ensureUnsubscribeSecret resolves the AES secret for unsubscribe tokens:
// the configuration wins, then an existing data_dir file, otherwise a fresh
// secret is generated and stored with 0600 (the plan's bootstrap rule). The
// secret value itself is never logged.
func ensureUnsubscribeSecret(cfg *config.Config, logger *slog.Logger) ([]byte, error) {
	if cfg.Unsubscribe.Secret != "" {
		return []byte(cfg.Unsubscribe.Secret), nil
	}

	path := filepath.Join(cfg.DataDir, unsubscribeSecretFile)
	raw, err := os.ReadFile(path) // #nosec G304 -- path is data_dir from the operator's configuration
	if err == nil {
		if secret := strings.TrimSpace(string(raw)); secret != "" {
			return []byte(secret), nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("read unsubscribe secret: %w", err)
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return nil, fmt.Errorf("generate unsubscribe secret: %w", err)
	}
	secret := hex.EncodeToString(buf)
	if err := os.WriteFile(path, []byte(secret+"\n"), 0o600); err != nil {
		return nil, fmt.Errorf("store unsubscribe secret: %w", err)
	}
	logger.Info("unsubscribe secret generated", slog.String("file", unsubscribeSecretFile))
	return []byte(secret), nil
}
