package redact

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestRedactorRemovesRegisteredStructuredAndPEMSecrets(t *testing.T) {
	t.Parallel()
	redactor := New("literal-secret")
	input := `{"token":"json-secret","safe":"literal-secret","dsn":"postgres://user:pass@example/db"}
Authorization: Bearer abc.def.ghi
password: yaml-secret
-----BEGIN PRIVATE KEY-----
private-material
-----END PRIVATE KEY-----`
	output := redactor.String(input)
	for _, secret := range []string{"literal-secret", "json-secret", "pass@example", "abc.def.ghi", "yaml-secret", "private-material"} {
		if strings.Contains(output, secret) {
			t.Fatalf("redacted output retains %q:\n%s", secret, output)
		}
	}
	firstLine, _, _ := strings.Cut(output, "\n")
	var decoded map[string]string
	if err := json.Unmarshal([]byte(firstLine), &decoded); err != nil {
		t.Fatalf("redaction broke JSON: %v\n%s", err, firstLine)
	}
	if decoded["token"] != replacement || decoded["safe"] != replacement {
		t.Fatalf("JSON values not redacted: %v", decoded)
	}
}
