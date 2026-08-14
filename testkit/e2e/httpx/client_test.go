package httpx

import (
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestTLSClientVerifiesExplicitCAAndServerName(t *testing.T) {
	t.Parallel()
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(response, "ok")
	}))
	defer server.Close()
	certificate := server.Certificate()
	roots := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate.Raw})
	client, err := TLSClient(TLSOptions{Timeout: time.Second, RootCAPEM: roots, ServerName: certificate.DNSNames[0]})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("TLS status = %d", response.StatusCode)
	}
}

func TestTLSClientRejectsIncompleteTrust(t *testing.T) {
	t.Parallel()
	if _, err := TLSClient(TLSOptions{Timeout: time.Second}); err == nil {
		t.Fatal("TLS client accepted missing CA and server name")
	}
}
