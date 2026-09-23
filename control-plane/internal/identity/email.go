package identity

import "strings"

// CanonicalEmail validates a submitted work email and returns the delivery
// form (domain normalized) plus the canonical normalized form used for
// uniqueness and lookup. Quoted local parts, comments, and non-simple domains
// are rejected rather than interpreted.
func CanonicalEmail(email string) (delivery, normalized string, ok bool) {
	email = strings.TrimSpace(email)
	if email == "" || len(email) > 254 {
		return "", "", false
	}
	if strings.ContainsAny(email, " \t\r\n<>\"(),:;\\[]") {
		return "", "", false
	}
	at := strings.LastIndex(email, "@")
	if at <= 0 || at == len(email)-1 || at > 64 {
		return "", "", false
	}
	local, domain := email[:at], email[at+1:]
	if strings.Contains(local, "@") {
		return "", "", false
	}
	domain = strings.ToLower(domain)
	if !validEmailDomain(domain) {
		return "", "", false
	}
	delivery = local + "@" + domain
	return delivery, strings.ToLower(delivery), true
}

// validEmailDomain accepts a simple, single- or multi-label domain. A
// single-label domain (for example `admin@local`) is syntactically valid;
// deliverability is the operator's concern, not the canonicalizer's.
func validEmailDomain(domain string) bool {
	if domain == "" || len(domain) > 253 ||
		strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") ||
		strings.Contains(domain, "..") {
		return false
	}
	for _, r := range domain {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.':
		default:
			return false
		}
	}
	for _, part := range strings.Split(domain, ".") {
		if part == "" || strings.HasPrefix(part, "-") || strings.HasSuffix(part, "-") {
			return false
		}
	}
	return true
}
