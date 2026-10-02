package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"relayward-mail/internal/store"
)

// TestEnsureInitialAdminToken covers first start and idempotence.
func TestEnsureInitialAdminToken(t *testing.T) {
	dataDir := t.TempDir()
	st, err := store.Open(filepath.Join(dataDir, "relayward.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	ctx := context.Background()

	if err := ensureInitialAdminToken(ctx, st, dataDir, logger); err != nil {
		t.Fatalf("first call: %v", err)
	}

	// Token file exists with the expected format.
	path := filepath.Join(dataDir, initialAdminTokenFile)
	rawBytes, err := os.ReadFile(path) // #nosec G104/G304 -- path built from the test temp dir
	if err != nil {
		t.Fatalf("read token file: %v", err)
	}
	raw := strings.TrimSpace(string(rawBytes))
	if !strings.HasPrefix(raw, "rw_admin_") || len(raw) != len("rw_admin_")+43 {
		t.Errorf("token = %q, unexpected format", raw)
	}

	// Hash stored in the database, plaintext never.
	has, err := st.HasAdminToken(ctx)
	if err != nil || !has {
		t.Fatalf("HasAdminToken = %v, %v; want true", has, err)
	}
	tok, err := st.GetAdminTokenByHash(ctx, store.HashToken(raw))
	if err != nil {
		t.Fatalf("token hash not stored: %v", err)
	}
	if tok.Role != store.RoleAdmin || tok.Name != "initial" {
		t.Errorf("unexpected token: %+v", tok)
	}
	if bytes.Contains(logBuf.Bytes(), []byte(store.HashToken(raw))) {
		t.Error("hash must not be logged")
	}

	// Plaintext is printed to the log exactly once.
	if n := bytes.Count(logBuf.Bytes(), []byte(raw)); n != 1 {
		t.Errorf("token appears %d times in log, want exactly 1", n)
	}

	// Second start must not create another token or rewrite the file.
	logBuf.Reset()
	if err := ensureInitialAdminToken(ctx, st, dataDir, logger); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if logBuf.Len() != 0 {
		t.Errorf("second call logged %q", logBuf.String())
	}
	after, err := os.ReadFile(path) // #nosec G304 -- path built from the test temp dir
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(rawBytes, after) {
		t.Error("token file was rewritten on second start")
	}
}
