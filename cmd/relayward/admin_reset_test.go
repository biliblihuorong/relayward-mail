package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"relayward-mail/internal/store"
)

// writeTestConfig prepares a minimal config plus data dir and returns the
// config path.
func writeTestConfig(t *testing.T) (configPath, dataDir string) {
	t.Helper()
	dataDir = t.TempDir()
	configPath = filepath.Join(t.TempDir(), "config.yaml")
	content := `data_dir: ` + filepath.ToSlash(dataDir) + `
upstream:
  host: smtp.provider.test
  username: apikey
  password: test-key
public:
  base_url: https://mail.example.test
`
	if err := os.WriteFile(configPath, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath, dataDir
}

func TestAdminResetRevokesAndReissues(t *testing.T) {
	configPath, dataDir := writeTestConfig(t)

	st, err := store.Open(filepath.Join(dataDir, dbFileName))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()

	oldToken := "rw_admin_oldtokenvalue" // #nosec G101 -- test fixture, not a real credential
	if err := st.CreateAdminToken(ctx, &store.AdminToken{
		Name: "old", Role: store.RoleAdmin, TokenHash: store.HashToken(oldToken),
	}, nil); err != nil {
		t.Fatal(err)
	}

	if err := adminReset([]string{"-config", configPath}); err != nil {
		t.Fatalf("adminReset: %v", err)
	}

	// The old token no longer authenticates.
	if _, err := st.GetAdminTokenByHash(ctx, store.HashToken(oldToken)); err == nil {
		t.Error("old token still resolvable after reset")
	}

	// A fresh initial token exists and its file was rewritten.
	has, err := st.HasAdminToken(ctx)
	if err != nil || !has {
		t.Fatalf("HasAdminToken = %v, %v; want true", has, err)
	}

	newRaw, err := os.ReadFile(filepath.Join(dataDir, initialAdminTokenFile)) // #nosec G304 -- path built from the test temp dir
	if err != nil {
		t.Fatalf("read new token file: %v", err)
	}
	if string(newRaw) == oldToken+"\n" {
		t.Error("token file was not reissued")
	}
	newToken := strings.TrimSpace(string(newRaw))
	if _, err := st.GetAdminTokenByHash(ctx, store.HashToken(newToken)); err != nil {
		t.Errorf("new token file does not match a stored hash: %v", err)
	}

	// The reset left an audit trail.
	entries, _, err := st.ListAuditLogs(ctx, store.AuditFilter{})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range entries {
		if e.Action == store.ActionTokenReset {
			found = true
		}
	}
	if !found {
		t.Errorf("no token.reset audit entry: %+v", entries)
	}
}
