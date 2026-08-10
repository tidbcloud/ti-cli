package transport

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDigestTransportUsesDigestNotBasic(t *testing.T) {
	const publicKey = "public-key"
	const privateKey = "private-key"

	seenAuthorized := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth == "" {
			w.Header().Set("WWW-Authenticate", `Digest realm="ti", nonce="nonce", qop="auth"`)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		seenAuthorized = true
		if strings.HasPrefix(auth, "Basic ") {
			t.Fatalf("control-plane transport must not use Basic auth: %s", auth)
		}
		if !strings.HasPrefix(auth, "Digest ") {
			t.Fatalf("expected Digest auth, got %q", auth)
		}
		if !strings.Contains(auth, fmt.Sprintf(`username="%s"`, publicKey)) {
			t.Fatalf("digest auth did not include public key username: %s", auth)
		}
		if strings.Contains(auth, privateKey) {
			t.Fatalf("digest auth leaked private key: %s", auth)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := &http.Client{Transport: NewDigest(publicKey, privateKey, http.DefaultTransport)}
	res, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", res.StatusCode)
	}
	if !seenAuthorized {
		t.Fatal("server did not receive authorized retry")
	}
}

func TestBearerTransport(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer fs-key" {
			t.Fatalf("unexpected Authorization header %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := &http.Client{Transport: NewBearer("fs-key", http.DefaultTransport)}
	res, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("expected 204, got %d", res.StatusCode)
	}
}

func TestDebugRoundTripperRedactsSecrets(t *testing.T) {
	var log bytes.Buffer
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := &http.Client{
		Transport: NewDebugRoundTripper(
			http.DefaultTransport,
			&log,
			Redactor{Secrets: []string{"private-key"}},
		),
	}
	res, err := client.Get(server.URL + "/path?token=private-key")
	if err != nil {
		t.Fatalf("GET failed: %v", err)
	}
	_ = res.Body.Close()
	if strings.Contains(log.String(), "private-key") {
		t.Fatalf("debug output leaked secret:\n%s", log.String())
	}
	if !strings.Contains(log.String(), "[REDACTED]") {
		t.Fatalf("debug output did not redact secret:\n%s", log.String())
	}
}
