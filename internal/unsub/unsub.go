// Package unsub provides stateless, per-recipient unsubscribe tokens for the
// Relayward mail gateway plus the RFC 8058 List-Unsubscribe header injection
// and Bcc stripping used when relaying messages upstream.
//
// A token is an opaque, URL-safe string that encrypts one (app, email) pair
// with AES-256-GCM under a key derived from the gateway secret via
// HKDF-SHA256. Tokens reveal neither the recipient address nor the app id,
// need no server-side state, and fail closed on any tampering.
package unsub

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"

	"golang.org/x/crypto/hkdf"
)

// ErrInvalidToken is returned (wrapped) by Manager.Parse for any token that
// cannot be decoded, authenticated, or decrypted.
var ErrInvalidToken = errors.New("unsub: invalid unsubscribe token")

const (
	// tokenVersion is the leading byte of every token payload. It allows the
	// token format to evolve without ambiguity.
	tokenVersion byte = 0x01
	// hkdfInfo binds the derived key to this specific purpose so the same
	// secret can safely be used elsewhere with a different info string.
	hkdfInfo = "unsubscribe-v1"
	// nonceSize is the AES-GCM standard nonce length in bytes.
	nonceSize = 12
	// appIDSize is the fixed-width big-endian encoding of the app id.
	appIDSize = 8
	// maxEmailLen is the maximum email length accepted by Manager.Token
	// (RFC 5321 limits the forward and reverse paths to 256 and 64 octets
	// respectively; 320 covers the worst case).
	maxEmailLen = 320
	// minTokenLen is the smallest possible decoded payload: version byte,
	// nonce, app id, and the 16-byte GCM tag of an otherwise empty address.
	minTokenLen = 1 + nonceSize + appIDSize + 16
)

// Manager issues and validates stateless unsubscribe tokens. It is safe for
// concurrent use; the underlying AES-GCM instance is read-only after
// construction.
type Manager struct {
	aead cipher.AEAD
}

// NewManager derives the AES-256 key from secret via HKDF-SHA256
// (hash=sha256.New, salt=nil, info=[]byte("unsubscribe-v1")), reading 32
// bytes. Returns an error when secret is empty.
func NewManager(secret []byte) (*Manager, error) {
	if len(secret) == 0 {
		return nil, errors.New("unsub: secret must not be empty")
	}
	key := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, secret, nil, []byte(hkdfInfo)), key); err != nil {
		return nil, fmt.Errorf("unsub: derive key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("unsub: new cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("unsub: new gcm: %w", err)
	}
	return &Manager{aead: aead}, nil
}

// Token builds an opaque unsubscribe token for one (app, email) pair:
//
//	base64.RawURLEncoding( 0x01 | 12-byte crypto/rand nonce | AES-256-GCM seal )
//
// where the GCM plaintext is appID as 8 bytes big-endian followed by email.
// Returns an error when appID <= 0, email is empty, email is longer than 320
// bytes, or email contains CR, LF, NUL or any other control character
// (defensive header-injection check). The email is stored exactly as given;
// any normalization is the caller's job.
func (m *Manager) Token(appID int64, email string) (string, error) {
	if appID <= 0 {
		return "", fmt.Errorf("unsub: appID must be positive, got %d", appID)
	}
	if email == "" {
		return "", errors.New("unsub: email must not be empty")
	}
	if len(email) > maxEmailLen {
		return "", fmt.Errorf("unsub: email is %d bytes, maximum is %d", len(email), maxEmailLen)
	}
	for i := 0; i < len(email); i++ {
		if c := email[i]; c < 0x20 || c == 0x7f {
			return "", fmt.Errorf("unsub: email contains control character 0x%02x at offset %d", c, i)
		}
	}

	plain := make([]byte, appIDSize, appIDSize+len(email))
	binary.BigEndian.PutUint64(plain, uint64(appID))
	plain = append(plain, email...)

	nonce := make([]byte, nonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("unsub: read nonce: %w", err)
	}
	sealed := m.aead.Seal(nil, nonce, plain, nil)

	buf := make([]byte, 0, 1+len(nonce)+len(sealed))
	buf = append(buf, tokenVersion)
	buf = append(buf, nonce...)
	buf = append(buf, sealed...)
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// Parse decrypts a token produced by Token and returns (appID, email, nil).
// Any failure — bad base64, wrong length, unknown version byte, GCM
// authentication failure, appID out of int64 positive range — returns
// (0, "", an error wrapping ErrInvalidToken).
func (m *Manager) Parse(token string) (int64, string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return 0, "", fmt.Errorf("%w: bad base64: %w", ErrInvalidToken, err)
	}
	if len(raw) < minTokenLen {
		return 0, "", fmt.Errorf("%w: token is %d bytes, minimum is %d", ErrInvalidToken, len(raw), minTokenLen)
	}
	if raw[0] != tokenVersion {
		return 0, "", fmt.Errorf("%w: unknown version byte 0x%02x", ErrInvalidToken, raw[0])
	}

	nonce := raw[1 : 1+nonceSize]
	sealed := raw[1+nonceSize:]
	plain, err := m.aead.Open(nil, nonce, sealed, nil)
	if err != nil {
		return 0, "", fmt.Errorf("%w: %w", ErrInvalidToken, err)
	}
	if len(plain) < appIDSize {
		return 0, "", fmt.Errorf("%w: plaintext is %d bytes, minimum is %d", ErrInvalidToken, len(plain), appIDSize)
	}
	id := binary.BigEndian.Uint64(plain[:appIDSize])
	if id > math.MaxInt64 {
		return 0, "", fmt.Errorf("%w: appID %d exceeds int64 range", ErrInvalidToken, id)
	}
	return int64(id), string(plain[appIDSize:]), nil
}
