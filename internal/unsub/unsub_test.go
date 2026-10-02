package unsub

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

const testSecret = "unit-test-secret-for-relayward-unsub"

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	m, err := NewManager([]byte(testSecret))
	if err != nil {
		t.Fatalf("NewManager(%q) failed: %v", testSecret, err)
	}
	return m
}

func TestNewManagerEmptySecret(t *testing.T) {
	for name, secret := range map[string][]byte{
		"nil":         nil,
		"empty slice": {},
	} {
		if _, err := NewManager(secret); err == nil {
			t.Errorf("NewManager(%s) = nil error, want error", name)
		}
	}
}

func TestNewManagerSameSecretInterchangeable(t *testing.T) {
	a1, err := NewManager([]byte("secret-x"))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	a2, err := NewManager([]byte("secret-x"))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	tok, err := a1.Token(9, "user@example.com")
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	gotID, gotEmail, err := a2.Parse(tok)
	if err != nil {
		t.Fatalf("Parse with same-secret manager: %v", err)
	}
	if gotID != 9 || gotEmail != "user@example.com" {
		t.Errorf("Parse = (%d, %q), want (9, %q)", gotID, gotEmail, "user@example.com")
	}
}

func TestTokenParseRoundtrip(t *testing.T) {
	m := newTestManager(t)
	tests := []struct {
		name  string
		appID int64
		email string
	}{
		{"small app id", 1, "user@example.com"},
		{"large app id", 1 << 40, "recipient@lists.example.org"},
		{"mixed case preserved", 42, "User@Example.com"},
		{"plus addressing", 7, "user+tag@example.com"},
		{"max length email", 12345, strings.Repeat("a", 308) + "@example.com"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tok, err := m.Token(tc.appID, tc.email)
			if err != nil {
				t.Fatalf("Token(%d, %q) failed: %v", tc.appID, tc.email, err)
			}
			gotID, gotEmail, err := m.Parse(tok)
			if err != nil {
				t.Fatalf("Parse failed: %v", err)
			}
			if gotID != tc.appID || gotEmail != tc.email {
				t.Errorf("Parse = (%d, %q), want (%d, %q)", gotID, gotEmail, tc.appID, tc.email)
			}
		})
	}
}

func TestTokenRandomNonce(t *testing.T) {
	m := newTestManager(t)
	t1, err := m.Token(5, "user@example.com")
	if err != nil {
		t.Fatalf("Token #1: %v", err)
	}
	t2, err := m.Token(5, "user@example.com")
	if err != nil {
		t.Fatalf("Token #2: %v", err)
	}
	if t1 == t2 {
		t.Error("two Token calls for the same pair produced identical tokens; nonce not random")
	}
	for i, tok := range []string{t1, t2} {
		id, email, err := m.Parse(tok)
		if err != nil {
			t.Fatalf("Parse token #%d: %v", i+1, err)
		}
		if id != 5 || email != "user@example.com" {
			t.Errorf("Parse token #%d = (%d, %q), want (5, %q)", i+1, id, email, "user@example.com")
		}
	}
}

func TestTokenURLSafe(t *testing.T) {
	m := newTestManager(t)
	emails := []string{"user@example.com", "a.b+c@d.example.org"}
	for _, email := range emails {
		tok, err := m.Token(3, email)
		if err != nil {
			t.Fatalf("Token(%q): %v", email, err)
		}
		if strings.ContainsAny(tok, "+/=") {
			t.Errorf("token %q is not URL-safe unpadded base64", tok)
		}
	}
}

func TestTokenErrors(t *testing.T) {
	m := newTestManager(t)
	tests := []struct {
		name  string
		appID int64
		email string
	}{
		{"zero app id", 0, "user@example.com"},
		{"negative app id", -1, "user@example.com"},
		{"empty email", 1, ""},
		{"email with LF", 1, "user@example.com\n"},
		{"email with CR", 1, "user@example.com\r"},
		{"email with NUL", 1, "user@example.com\x00"},
		{"email with other control", 1, "us\x1ber@example.com"},
		{"email with DEL", 1, "user\x7f@example.com"},
		{"email too long", 1, strings.Repeat("a", 309) + "@example.com"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			tok, err := m.Token(tc.appID, tc.email)
			if err == nil {
				t.Fatalf("Token(%d, %q) = %q, want error", tc.appID, tc.email, tok)
			}
			if tok != "" {
				t.Errorf("Token returned non-empty token %q alongside error", tok)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	m := newTestManager(t)
	other, err := NewManager([]byte("a-different-secret"))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	valid, err := m.Token(7, "user@example.com")
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(valid)
	if err != nil {
		t.Fatalf("decoding freshly minted token: %v", err)
	}

	// recode copies the raw payload, applies an in-place mutation and
	// re-encodes it.
	recode := func(mutate func(p []byte)) string {
		cp := append([]byte(nil), raw...)
		mutate(cp)
		return base64.RawURLEncoding.EncodeToString(cp)
	}
	// truncate returns the re-encoding of the first n raw bytes.
	truncate := func(n int) string {
		return base64.RawURLEncoding.EncodeToString(raw[:n])
	}

	tests := []struct {
		name  string
		token string
	}{
		{"empty string", ""},
		{"invalid base64", "!!!"},
		{"too short", truncate(minTokenLen - 1)},
		{"unknown version byte", recode(func(p []byte) { p[0] = 0x02 })},
		{"truncated by one byte", truncate(len(raw) - 1)},
		{"nonce bit flip", recode(func(p []byte) { p[1] ^= 0x01 })},
		{"ciphertext bit flip", recode(func(p []byte) { p[1+nonceSize] ^= 0x01 })},
		{"final tag byte flip", recode(func(p []byte) { p[len(p)-1] ^= 0x01 })},
		{"different secret", mustToken(t, other, 7, "user@example.com")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			id, email, err := m.Parse(tc.token)
			if !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("Parse(%q) error = %v, want ErrInvalidToken", tc.token, err)
			}
			if id != 0 || email != "" {
				t.Errorf("Parse on failure = (%d, %q), want (0, \"\")", id, email)
			}
		})
	}
}

func mustToken(t *testing.T, m *Manager, appID int64, email string) string {
	t.Helper()
	tok, err := m.Token(appID, email)
	if err != nil {
		t.Fatalf("Token(%d, %q): %v", appID, email, err)
	}
	return tok
}

func TestCrossManagerTokens(t *testing.T) {
	a, err := NewManager([]byte("secret-a"))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	b, err := NewManager([]byte("secret-b"))
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	tokA, err := a.Token(11, "cross@example.com")
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if _, _, err := b.Parse(tokA); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("manager B accepted a token from manager A: err = %v, want ErrInvalidToken", err)
	}
	if _, _, err := a.Parse(tokA); err != nil {
		t.Errorf("manager A rejected its own token: %v", err)
	}
}
