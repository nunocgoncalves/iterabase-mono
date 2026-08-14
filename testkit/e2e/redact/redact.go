// Package redact removes registered and structurally recognizable secrets from evidence.
package redact

import (
	"regexp"
	"sort"
	"strings"
	"sync"
)

const replacement = "<redacted>"

var structuralPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?is)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`),
	regexp.MustCompile(`(?i)(bearer\s+)[A-Za-z0-9._~+/=-]+`),
	regexp.MustCompile(`(?i)(["']?(?:authorization|password|passwd|token|api[_-]?key|client[_-]?secret|cookie|private[_-]?key)["']?\s*[:=]\s*)(?:["'][^"']*["']|[^\s,}\]]+)`),
	regexp.MustCompile(`(?i)(://[^:/@\s]+:)[^@/\s]+(@)`),
}

// Redactor applies structural rules plus exact secret literals. Register exact
// values as soon as a fixture resolves them and before collecting output.
type Redactor struct {
	mu       sync.RWMutex
	literals []string
}

// New returns a redactor initialized with non-empty secret literals.
func New(secrets ...string) *Redactor {
	redactor := &Redactor{}
	redactor.Add(secrets...)
	return redactor
}

// Add registers exact values, longest first so overlapping tokens redact safely.
func (redactor *Redactor) Add(secrets ...string) {
	redactor.mu.Lock()
	defer redactor.mu.Unlock()
	seen := make(map[string]struct{}, len(redactor.literals)+len(secrets))
	for _, secret := range redactor.literals {
		seen[secret] = struct{}{}
	}
	for _, secret := range secrets {
		if secret == "" {
			continue
		}
		if _, exists := seen[secret]; exists {
			continue
		}
		redactor.literals = append(redactor.literals, secret)
		seen[secret] = struct{}{}
	}
	sort.Slice(redactor.literals, func(i, j int) bool {
		return len(redactor.literals[i]) > len(redactor.literals[j])
	})
}

// String returns redacted text.
func (redactor *Redactor) String(value string) string {
	var literals []string
	if redactor != nil {
		redactor.mu.RLock()
		literals = append(literals, redactor.literals...)
		redactor.mu.RUnlock()
	}
	for _, secret := range literals {
		value = strings.ReplaceAll(value, secret, replacement)
	}
	value = structuralPatterns[0].ReplaceAllString(value, replacement)
	value = structuralPatterns[1].ReplaceAllString(value, `${1}`+replacement)
	value = structuralPatterns[2].ReplaceAllString(value, `${1}"`+replacement+`"`)
	value = structuralPatterns[3].ReplaceAllString(value, `${1}`+replacement+`${2}`)
	return value
}

// Bytes returns a redacted copy.
func (redactor *Redactor) Bytes(value []byte) []byte {
	return []byte(redactor.String(string(value)))
}
