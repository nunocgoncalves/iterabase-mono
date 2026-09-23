package identity

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateSecretIsRandomAndDomainSeparated(t *testing.T) {
	t.Parallel()
	rawA, hashA, err := GenerateSecret(SecretDomainSessionToken)
	require.NoError(t, err)
	rawB, hashB, err := GenerateSecret(SecretDomainSessionToken)
	require.NoError(t, err)
	assert.NotEqual(t, rawA, rawB)
	assert.NotEqual(t, hashA, hashB)
	assert.NotContains(t, hashA, rawA)
	assert.Len(t, rawA, 43, "256-bit base64url secret keeps 43 characters")

	// The same raw value must hash differently per credential domain.
	assert.NotEqual(t, HashSecret(SecretDomainSessionToken, rawA), HashSecret(SecretDomainCSRF, rawA))
	assert.True(t, SecretMatches(hashA, SecretDomainSessionToken, rawA))
	assert.False(t, SecretMatches(hashA, SecretDomainCSRF, rawA))
	assert.False(t, SecretMatches(hashA, SecretDomainSessionToken, rawB))
	assert.False(t, SecretMatches("", SecretDomainSessionToken, rawA))
	assert.Equal(t, HashSecret(SecretDomainThrottle, "a@example.com"), HashSubject("a@example.com"))
}

func TestCanonicalEmail(t *testing.T) {
	t.Parallel()
	valid := []struct{ in, delivery, normalized string }{
		{"  Ada@Example.COM ", "Ada@example.com", "ada@example.com"},
		{"a.b+tag@sub.example.co.uk", "a.b+tag@sub.example.co.uk", "a.b+tag@sub.example.co.uk"},
		{"user@example.com\n", "user@example.com", "user@example.com"},
	}
	for _, tc := range valid {
		delivery, normalized, ok := CanonicalEmail(tc.in)
		require.True(t, ok, "expected %q to be valid", tc.in)
		assert.Equal(t, tc.delivery, delivery)
		assert.Equal(t, tc.normalized, normalized)
	}
	invalid := []string{
		"", "no-at", "@example.com", "user@",
		"user@.example.com", "user@example..com", "user@-example.com",
		"quoted\"@example.com", "user name@example.com", "user@exam ple.com",
		"user@@example.com", "user@exa\nmple.com",
	}
	for _, in := range invalid {
		_, _, ok := CanonicalEmail(in)
		assert.False(t, ok, "expected %q to be invalid", in)
	}
}

func TestParseClientLabels(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ua   string
		want ClientLabels
	}{
		{
			name: "chrome macos desktop",
			ua:   "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36",
			want: ClientLabels{Browser: BrowserChrome, OS: OSMacOS, Device: DeviceDesktop},
		},
		{
			name: "safari iphone",
			ua:   "Mozilla/5.0 (iPhone; CPU iPhone OS 17_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.0 Mobile/15E148 Safari/604.1",
			want: ClientLabels{Browser: BrowserSafari, OS: OSIOS, Device: DeviceMobile},
		},
		{
			name: "edge windows",
			ua:   "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36 Edg/120.0.0.0",
			want: ClientLabels{Browser: BrowserEdge, OS: OSWindows, Device: DeviceDesktop},
		},
		{name: "empty", ua: "", want: ClientLabels{}},
		{name: "unknown", ua: "some-agent", want: ClientLabels{Browser: ClientLabelOther, OS: ClientLabelOther, Device: DeviceDesktop}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, ParseClientLabels(tc.ua))
		})
	}

	long := make([]byte, userAgentMaxBytes+1)
	for i := range long {
		long[i] = 'a'
	}
	trimmed := ParseClientLabels(string(long))
	assert.NotEmpty(t, trimmed.Browser)
}

func TestRenderAuthEmailLocales(t *testing.T) {
	t.Parallel()
	for _, purpose := range []string{AuthLinkVerifyAccess, AuthLinkSetupPassword, AuthLinkResetPassword} {
		link := "https://app.example.com/auth/verify?token=abc"
		for _, locale := range []string{"en", "pt", "EN"} {
			subject, text, htmlBody, err := RenderAuthEmail(purpose, locale, link, "ada@example.com")
			require.NoError(t, err)
			assert.NotEmpty(t, subject)
			assert.Contains(t, text, link)
			assert.Contains(t, htmlBody, link)
			assert.Contains(t, htmlBody, "ada@example.com")
		}
	}
	_, _, _, err := RenderAuthEmail("unknown", "en", "https://app.example.com", "a@b.com")
	assert.Error(t, err)

	require.Empty(t, "")
	for _, purpose := range []string{AuthLinkVerifyAccess, AuthLinkSetupPassword, AuthLinkResetPassword} {
		link, err := AuthLinkURL("https://app.example.com/", purpose, "token")
		require.NoError(t, err)
		assert.Contains(t, link, "https://app.example.com/auth/")
		assert.NotContains(t, link, "//auth")
	}
	_, err = AuthLinkURL("https://app.example.com", "unknown", "token")
	assert.Error(t, err)
}

func TestValidateSMTPConfig(t *testing.T) {
	t.Parallel()
	valid := SMTPConfig{Host: "smtp.example.com", Port: 587, Mode: SMTPModeStartTLS, From: "auth@example.com"}
	require.NoError(t, ValidateSMTPConfig(valid))
	require.NoError(t, ValidateSMTPConfig(SMTPConfig{Host: "smtp.example.com", Port: 465, Mode: SMTPModeTLS, From: "auth@example.com", Username: "u", Password: "p"}))

	invalid := []SMTPConfig{
		{Host: "", Port: 587, Mode: SMTPModeStartTLS, From: "auth@example.com"},
		{Host: "smtp.example.com", Port: 0, Mode: SMTPModeStartTLS, From: "auth@example.com"},
		{Host: "smtp.example.com", Port: 587, Mode: "plain", From: "auth@example.com"},
		{Host: "smtp.example.com", Port: 587, Mode: SMTPModeStartTLS, From: "not-an-address"},
		{Host: "smtp.example.com", Port: 587, Mode: SMTPModeStartTLS, From: "auth@example.com", Password: "without-user"},
		{Host: "smtp.example.com", Port: 587, Mode: SMTPModeStartTLS, From: "auth@example.com\r\nBcc: x@y.z"},
	}
	for i, cfg := range invalid {
		assert.Error(t, ValidateSMTPConfig(cfg), "case %d should fail", i)
	}
}

func TestBuildMIMEContainsAlternativeParts(t *testing.T) {
	t.Parallel()
	payload := buildMIME("auth@example.com", AuthEmailMessage{
		To:      "ada@example.com",
		Subject: "Café setup — Iterabase",
		Text:    "plain body",
		HTML:    "<p>html body</p>",
	})
	body := string(payload)
	assert.Contains(t, body, "From: auth@example.com")
	assert.Contains(t, body, "To: ada@example.com")
	assert.Contains(t, body, "multipart/alternative")
	assert.Contains(t, body, "plain body")
	assert.Contains(t, body, "html body")
}
