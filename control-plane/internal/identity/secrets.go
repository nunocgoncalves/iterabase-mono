package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
)

// Secret domains separate otherwise-identical random material by credential
// kind (architecture 5.1: setup/reset/session/API-key material never reuses
// another credential's domain).
const (
	SecretDomainSessionToken = "iterabase/browser-session/v1"  //nolint:gosec // domain-separation label, never a credential
	SecretDomainCSRF         = "iterabase/csrf/v1"             //nolint:gosec // domain-separation label, never a credential
	SecretDomainAuthLink     = "iterabase/auth-link/v1"        //nolint:gosec // domain-separation label, never a credential
	SecretDomainThrottle     = "iterabase/throttle-subject/v1" //nolint:gosec // domain-separation label, never a credential
)

const secretEntropyBytes = 32 // 256 bits

// GenerateSecret returns a new 256-bit CSPRNG secret and its domain-separated
// hash. The raw value is never persisted.
func GenerateSecret(domain string) (raw, hash string, err error) {
	buf := make([]byte, secretEntropyBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("generating secret: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(buf)
	return raw, HashSecret(domain, raw), nil
}

// HashSecret derives the persisted, domain-separated SHA-256 hash of a secret.
func HashSecret(domain, raw string) string {
	sum := sha256.Sum256([]byte(domain + "\x00" + raw))
	return hex.EncodeToString(sum[:])
}

// SecretMatches compares a persisted hash with the hash of a presented secret
// in constant time.
func SecretMatches(storedHash, domain, raw string) bool {
	return subtle.ConstantTimeCompare([]byte(storedHash), []byte(HashSecret(domain, raw))) == 1
}

// HashSubject derives the bounded, non-reversible throttle subject key so raw
// emails/IP addresses never land in the counter table.
func HashSubject(subject string) string {
	return HashSecret(SecretDomainThrottle, subject)
}
