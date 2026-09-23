package identity

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/argon2"
)

func TestValidatePasswordPolicy(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		password string
		wantErr  bool
	}{
		{"minimum length", "correct-horse", false},
		{"unicode counts as characters", strings.Repeat("é", 12), false},
		{"too short", "short-pass", true},
		{"too long", strings.Repeat("a", PasswordMaxRunes+1), true},
		{"common password", "password12345", true},
		{"common password case-insensitive", "PASSWORD12345", true},
		{"empty", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePasswordPolicy(tc.password)
			if tc.wantErr {
				require.Error(t, err)
				assert.ErrorIs(t, err, ErrPasswordPolicy)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestPasswordHashRoundTrip(t *testing.T) {
	t.Parallel()
	hash, err := HashPassword("correct-horse-battery")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=3,p=1$"), "hash %q must use the approved floor", hash)
	assert.True(t, VerifyPassword(hash, "correct-horse-battery"))
	assert.False(t, VerifyPassword(hash, "correct-horse-battery "))
	assert.False(t, VerifyPassword(hash, "wrong-password-123"))

	second, err := HashPassword("correct-horse-battery")
	require.NoError(t, err)
	assert.NotEqual(t, hash, second, "salts must be independent")
}

func TestPasswordHashRejectsPolicyViolations(t *testing.T) {
	t.Parallel()
	_, err := HashPassword("password12345")
	assert.ErrorIs(t, err, ErrPasswordPolicy)
}

func TestVerifyPasswordRejectsMalformedOrWeakHashes(t *testing.T) {
	t.Parallel()
	weak := encodeWithCost(1024, 1)
	for name, encoded := range map[string]string{
		"empty":       "",
		"garbage":     "not-a-hash",
		"wrong kind":  "$argon2i$v=19$m=65536,t=3,p=1$c2FsdA$aGFzaA",
		"weak memory": weak,
	} {
		t.Run(name, func(t *testing.T) {
			assert.False(t, VerifyPassword(encoded, "correct-horse-battery"))
		})
	}
}

// encodeWithCost emits a syntactically valid self-describing hash below the
// approved floor so verification can prove it fails closed.
func encodeWithCost(memoryKiB, iterations uint32) string {
	salt := make([]byte, argon2SaltLength)
	key := make([]byte, argon2KeyLength)
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, memoryKiB, iterations, argon2Parallelism,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(key))
}

func TestDummyPasswordVerifyDoesNotPanic(t *testing.T) {
	t.Parallel()
	DummyPasswordVerify("whatever")
}
