// Package httpx creates bounded HTTP and verified-TLS clients for E2E stages.
package httpx

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"time"
)

// TLSOptions defines an exact trust and optional client identity.
type TLSOptions struct {
	Timeout    time.Duration
	RootCAPEM  []byte
	ServerName string
	CertPEM    []byte
	KeyPEM     []byte
}

// Client returns a bounded plaintext HTTP client.
func Client(timeout time.Duration) (*http.Client, error) {
	if timeout <= 0 {
		return nil, fmt.Errorf("http client timeout must be positive")
	}
	return &http.Client{Timeout: timeout}, nil
}

// TLSClient returns a client that verifies the supplied CA and server identity.
// There is intentionally no insecure-skip option in the shared helper.
func TLSClient(options TLSOptions) (*http.Client, error) {
	if options.Timeout <= 0 || len(options.RootCAPEM) == 0 || options.ServerName == "" {
		return nil, fmt.Errorf("tls client requires timeout, root CA, and server name")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(options.RootCAPEM) {
		return nil, fmt.Errorf("tls root CA contains no certificate")
	}
	config := &tls.Config{RootCAs: roots, ServerName: options.ServerName, MinVersion: tls.VersionTLS12}
	if len(options.CertPEM) > 0 || len(options.KeyPEM) > 0 {
		if len(options.CertPEM) == 0 || len(options.KeyPEM) == 0 {
			return nil, fmt.Errorf("tls client certificate and key must be supplied together")
		}
		certificate, err := tls.X509KeyPair(options.CertPEM, options.KeyPEM)
		if err != nil {
			return nil, fmt.Errorf("load TLS client identity: %w", err)
		}
		config.Certificates = []tls.Certificate{certificate}
	}
	return &http.Client{Timeout: options.Timeout, Transport: &http.Transport{TLSClientConfig: config}}, nil
}
