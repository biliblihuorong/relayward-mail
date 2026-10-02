package store

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Argon2id parameters per the plan: 19 MiB memory, 2 iterations, 1 thread.
const (
	argonMemory  uint32 = 19 * 1024
	argonTime    uint32 = 2
	argonThreads uint8  = 1
	argonKeyLen  uint32 = 32
	argonSaltLen        = 16
)

var phcEncoding = base64.RawStdEncoding

// HashPassword derives an argon2id hash in PHC string format from password.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argonTime, argonMemory, argonThreads, argonKeyLen)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads,
		phcEncoding.EncodeToString(salt), phcEncoding.EncodeToString(key)), nil
}

// VerifyPassword reports whether password matches the argon2id PHC hash.
func VerifyPassword(hash, password string) (bool, error) {
	fields := strings.Split(hash, "$")
	// ["", "argon2id", "v=19", "m=19456,t=2,p=1", salt, key]
	if len(fields) != 6 || fields[1] != "argon2id" {
		return false, fmt.Errorf("unsupported password hash format")
	}

	var params argon2Params
	if err := params.parse(fields[2], fields[3]); err != nil {
		return false, err
	}
	salt, err := phcEncoding.DecodeString(fields[4])
	if err != nil {
		return false, fmt.Errorf("decode salt: %w", err)
	}
	want, err := phcEncoding.DecodeString(fields[5])
	if err != nil {
		return false, fmt.Errorf("decode hash: %w", err)
	}
	if len(want) == 0 || len(want) > 128 {
		return false, fmt.Errorf("implausible hash length %d", len(want))
	}

	got := argon2.IDKey([]byte(password), salt, params.time, params.memory, params.threads, uint32(len(want))) // #nosec G115 -- length bounds checked above
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

type argon2Params struct {
	memory  uint32
	time    uint32
	threads uint8
}

func (p *argon2Params) parse(version, params string) error {
	if version != fmt.Sprintf("v=%d", argon2.Version) {
		return fmt.Errorf("unsupported argon2 version %q", version)
	}
	for _, kv := range strings.Split(params, ",") {
		name, value, found := strings.Cut(kv, "=")
		if !found {
			return fmt.Errorf("malformed argon2 parameter %q", kv)
		}
		n, err := strconv.ParseUint(value, 10, 32)
		if err != nil {
			return fmt.Errorf("malformed argon2 parameter %q", kv)
		}
		switch name {
		case "m":
			p.memory = uint32(n) // #nosec G115 -- parsed into uint64, checked below
		case "t":
			p.time = uint32(n) // #nosec G115 -- parsed into uint64, checked below
		case "p":
			if n > 255 {
				return fmt.Errorf("argon2 thread count %d too large", n)
			}
			p.threads = uint8(n)
		default:
			return fmt.Errorf("unknown argon2 parameter %q", name)
		}
	}
	if p.memory == 0 || p.time == 0 || p.threads == 0 {
		return fmt.Errorf("incomplete argon2 parameters")
	}
	return nil
}

// RandomToken returns prefix followed by n random bytes encoded as base64url
// without padding, e.g. "rw_admin_<43 chars>".
func RandomToken(prefix string, n int) (string, error) {
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(raw), nil
}

// RandomPassword returns a random SMTP password safe to send in SMTP AUTH.
func RandomPassword() (string, error) {
	return RandomToken("", 24)
}

// HashToken is the one-way storage form of an admin token (SHA-256 hex).
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", sum)
}

// NormalizeEmail trims surrounding whitespace and lowercases an email
// address, the canonical form used everywhere in the database.
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
