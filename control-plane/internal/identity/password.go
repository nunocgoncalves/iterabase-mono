package identity

import (
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Password policy floor (DES-HOR-451-06). Configuration may increase the hash
// cost or shorten time bounds, never lower this floor.
const (
	PasswordMinRunes = 12
	PasswordMaxRunes = 128

	argon2MemoryKiB   = 64 * 1024
	argon2Iterations  = 3
	argon2Parallelism = 1
	argon2SaltLength  = 16
	argon2KeyLength   = 32

	// argon2MaxMemoryKiB and the iteration/parallelism ceilings bound a
	// verification against a hostile stored hash.
	argon2MaxMemoryKiB   = 1024 * 1024
	argon2MaxIterations  = 10
	argon2MaxParallelism = 4
)

// ErrPasswordPolicy is returned when a password violates the approved policy.
var ErrPasswordPolicy = errors.New("identity: password does not meet policy")

// commonPasswordsFile is the bundled common-password blocklist. The file keeps
// its source attribution in comment lines.
//
//go:embed common_passwords.txt
var commonPasswordsFile string

var commonPasswords = parseCommonPasswords(commonPasswordsFile)

func parseCommonPasswords(raw string) map[string]struct{} {
	out := make(map[string]struct{}, 10240)
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out[strings.ToLower(line)] = struct{}{}
	}
	return out
}

// ValidatePasswordPolicy enforces the approved length bounds and the bundled
// common-password rejection. It deliberately imposes no composition rule.
func ValidatePasswordPolicy(password string) error {
	n := utf8.RuneCountInString(password)
	if n < PasswordMinRunes || n > PasswordMaxRunes {
		return fmt.Errorf("%w: password must be between %d and %d characters",
			ErrPasswordPolicy, PasswordMinRunes, PasswordMaxRunes)
	}
	if _, ok := commonPasswords[strings.ToLower(password)]; ok {
		return fmt.Errorf("%w: password is too common", ErrPasswordPolicy)
	}
	return nil
}

// HashPassword validates policy and returns a self-describing Argon2id hash at
// the approved floor.
func HashPassword(password string) (string, error) {
	if err := ValidatePasswordPolicy(password); err != nil {
		return "", err
	}
	salt := make([]byte, argon2SaltLength)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generating password salt: %w", err)
	}
	key := argon2.IDKey([]byte(password), salt, argon2Iterations, argon2MemoryKiB, argon2Parallelism, argon2KeyLength)
	return encodeArgon2(salt, key), nil
}

// VerifyPassword reports whether password matches the encoded Argon2id hash. A
// malformed or weaker-than-floor hash fails closed.
func VerifyPassword(encoded, password string) bool {
	salt, key, memoryKiB, iterations, parallelism, err := decodeArgon2(encoded)
	if err != nil {
		return false
	}
	computed := argon2.IDKey([]byte(password), salt, iterations, memoryKiB, uint8(parallelism), uint32(len(key))) //nolint:gosec // decoded key length is bounded by decodeArgon2
	return subtle.ConstantTimeCompare(computed, key) == 1
}

// DummyPasswordVerify burns the equivalent Argon2id work for an unknown or
// ineligible account so sign-in timing does not disclose account existence.
func DummyPasswordVerify(password string) {
	salt := []byte("iterabase-dummy-salt")
	_ = argon2.IDKey([]byte(password), salt, argon2Iterations, argon2MemoryKiB, argon2Parallelism, argon2KeyLength)
}

func encodeArgon2(salt, key []byte) string {
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argon2MemoryKiB, argon2Iterations, argon2Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key))
}

//nolint:gocyclo // explicit field validation of a hostile stored hash.
func decodeArgon2(encoded string) (salt, key []byte, memoryKiB, iterations, parallelism uint32, err error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != "argon2id" {
		return nil, nil, 0, 0, 0, ErrPasswordPolicy
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return nil, nil, 0, 0, 0, ErrPasswordPolicy
	}
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &memoryKiB, &iterations, &parallelism); err != nil {
		return nil, nil, 0, 0, 0, ErrPasswordPolicy
	}
	if memoryKiB < argon2MemoryKiB || memoryKiB > argon2MaxMemoryKiB ||
		iterations < argon2Iterations || iterations > argon2MaxIterations ||
		parallelism < argon2Parallelism || parallelism > argon2MaxParallelism {
		return nil, nil, 0, 0, 0, ErrPasswordPolicy
	}
	salt, err = base64.RawStdEncoding.DecodeString(parts[4])
	if err != nil || len(salt) < argon2SaltLength {
		return nil, nil, 0, 0, 0, ErrPasswordPolicy
	}
	key, err = base64.RawStdEncoding.DecodeString(parts[5])
	if err != nil || len(key) != argon2KeyLength {
		return nil, nil, 0, 0, 0, ErrPasswordPolicy
	}
	return salt, key, memoryKiB, iterations, parallelism, nil
}
