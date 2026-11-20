package fs

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/api"
	apitransport "github.com/tidbcloud/ti-cli/internal/api/transport"
	"github.com/tidbcloud/ti-cli/internal/apperr"
)

const insecureSignedURL = "http://bucket.example.com/signed/large.bin?X-Amz-Signature=secret-signature"

func TestReadFileRefusesRedirectToPlainHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, insecureSignedURL, http.StatusFound)
	}))
	defer server.Close()

	client := testBearerClient(t, server.URL)
	_, err := client.ReadFile(context.Background(), "/workspace/large.bin")
	assertInsecureRedirect(t, err)
}

func TestReadFileRefusesStorageRedirectToPlainHTTP(t *testing.T) {
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, insecureSignedURL, http.StatusTemporaryRedirect)
	}))
	defer storage.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, storage.URL+"/signed/large.bin?X-Amz-Signature=first-signature", http.StatusFound)
	}))
	defer server.Close()

	client := testBearerClient(t, server.URL)
	_, err := client.ReadFile(context.Background(), "/workspace/large.bin")
	assertInsecureRedirect(t, err)
	if strings.Contains(err.Error(), "first-signature") {
		t.Fatalf("error leaks the first signature: %v", err)
	}
}

func TestReadFileFollowsRelativeRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/fs/workspace/large.bin" {
			w.Header().Set("Location", "/signed/large.bin")
			w.WriteHeader(http.StatusFound)
			return
		}
		_, _ = w.Write([]byte("large bytes"))
	}))
	defer server.Close()

	client := testBearerClient(t, server.URL)
	data, err := client.ReadFile(context.Background(), "/workspace/large.bin")
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	if string(data) != "large bytes" {
		t.Fatalf("unexpected redirected data %q", data)
	}
}

func TestPresignedTransfersRefusePlainHTTP(t *testing.T) {
	ctx := context.Background()
	calls := map[string]func(string) error{
		"uploadPresignedPartV2": func(rawURL string) error {
			_, err := uploadPresignedPartV2(ctx, PresignedPart{URL: rawURL}, []byte("data"))
			return err
		},
		"uploadPresignedPatchPart": func(rawURL string) error {
			return uploadPresignedPatchPart(ctx, rawURL, nil, []byte("data"))
		},
		"uploadPresignedPartWithChecksum": func(rawURL string) error {
			return uploadPresignedPartWithChecksum(ctx, rawURL, nil, []byte("data"), "", true)
		},
		"readPresignedPart": func(rawURL string) error {
			_, err := readPresignedPart(ctx, rawURL, nil)
			return err
		},
	}
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, insecureSignedURL, http.StatusTemporaryRedirect)
	}))
	defer redirecting.Close()

	for name, call := range calls {
		for _, rawURL := range []string{insecureSignedURL, redirecting.URL + "/part?X-Amz-Signature=first-signature"} {
			err := call(rawURL)
			if !errors.Is(err, apitransport.ErrInsecureURL) {
				t.Errorf("%s(%s) error = %v, want ErrInsecureURL", name, rawURL, err)
				continue
			}
			if strings.Contains(err.Error(), "signature") {
				t.Errorf("%s error leaks the presigned signature: %v", name, err)
			}
		}
	}
}

func assertInsecureRedirect(t *testing.T, err error) {
	t.Helper()
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "api.insecure_redirect" {
		t.Fatalf("error = %v, want api.insecure_redirect", err)
	}
	if !errors.Is(err, apitransport.ErrInsecureURL) {
		t.Fatalf("error = %v, want ErrInsecureURL", err)
	}
	if strings.Contains(err.Error(), "secret-signature") {
		t.Fatalf("error leaks the presigned signature: %v", err)
	}
}

func TestReadFileReportsRedirectLoop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/signed/large.bin", http.StatusFound)
	}))
	defer server.Close()

	client := testBearerClient(t, server.URL)
	_, err := client.ReadFile(context.Background(), "/workspace/large.bin")
	var apiErr *api.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "api.redirect_loop" {
		t.Fatalf("error = %v, want api.redirect_loop", err)
	}
	if !errors.Is(err, apitransport.ErrTooManyRedirects) {
		t.Fatalf("error = %v, want ErrTooManyRedirects", err)
	}
}

func TestReadFileMalformedRedirectDoesNotLeak(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://bucket.example.com/large%zz.bin?X-Amz-Signature=secret-signature")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()

	client := testBearerClient(t, server.URL)
	_, err := client.ReadFile(context.Background(), "/workspace/large.bin")
	var apiErr *api.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "api.invalid_redirect" {
		t.Fatalf("error = %v, want api.invalid_redirect", err)
	}
	if strings.Contains(err.Error(), "secret-signature") || strings.Contains(err.Error(), "large%zz") {
		t.Fatalf("error leaks the redirect URL: %v", err)
	}
}

func TestPresignedTransfersRefuseMalformedURLWithoutLeaking(t *testing.T) {
	ctx := context.Background()
	const malformed = "https://bucket.example.com/part%zz?X-Amz-Signature=secret-signature"
	for name, call := range map[string]func() error{
		"uploadPresignedPartV2": func() error {
			_, err := uploadPresignedPartV2(ctx, PresignedPart{URL: malformed}, []byte("data"))
			return err
		},
		"uploadPresignedPatchPart": func() error {
			return uploadPresignedPatchPart(ctx, malformed, nil, []byte("data"))
		},
		"uploadPresignedPartWithChecksum": func() error {
			return uploadPresignedPartWithChecksum(ctx, malformed, nil, []byte("data"), "", true)
		},
		"readPresignedPart": func() error {
			_, err := readPresignedPart(ctx, malformed, nil)
			return err
		},
	} {
		err := call()
		if !errors.Is(err, apitransport.ErrMalformedURL) {
			t.Errorf("%s error = %v, want ErrMalformedURL", name, err)
			continue
		}
		if strings.Contains(err.Error(), "secret-signature") {
			t.Errorf("%s error leaks the presigned signature: %v", name, err)
		}
	}
}

// TestPresignedTransfersOverHTTPS pins the happy path: an https presigned URL
// still gets the body, the server-chosen headers (never Host), the checksum
// headers, and returns the ETag or the bytes. It swaps the package transfer
// client for one that trusts the test server, so it must not run in parallel.
func TestPresignedTransfersOverHTTPS(t *testing.T) {
	type seen struct {
		method, body, sha256, crc32c, custom, host string
	}
	var mu sync.Mutex
	var got []seen
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = append(got, seen{
			method: r.Method,
			body:   string(body),
			sha256: r.Header.Get("x-amz-checksum-sha256"),
			crc32c: r.Header.Get("x-amz-checksum-crc32c"),
			custom: r.Header.Get("x-amz-meta-test"),
			host:   r.Host,
		})
		mu.Unlock()
		if r.URL.Query().Get("X-Amz-Signature") != "sig" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte("stored bytes"))
			return
		}
		w.Header().Set("ETag", `"etag-1"`)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	previous := transferClient
	transferClient = server.Client()
	t.Cleanup(func() { transferClient = previous })

	ctx := context.Background()
	rawURL := server.URL + "/bucket/part?X-Amz-Signature=sig"
	headers := map[string]string{"x-amz-meta-test": "yes", "Host": "evil.example.com"}

	etag, err := uploadPresignedPartV2(ctx, PresignedPart{URL: rawURL, Headers: headers}, []byte("part one"))
	if err != nil || etag != `"etag-1"` {
		t.Fatalf("uploadPresignedPartV2 = %q, %v", etag, err)
	}
	if err := uploadPresignedPatchPart(ctx, rawURL, map[string]string{"x-amz-checksum-sha256": "placeholder", "x-amz-meta-test": "yes"}, []byte("part two")); err != nil {
		t.Fatalf("uploadPresignedPatchPart: %v", err)
	}
	if err := uploadPresignedPartWithChecksum(ctx, rawURL, headers, []byte("part three"), "", true); err != nil {
		t.Fatalf("uploadPresignedPartWithChecksum: %v", err)
	}
	data, err := readPresignedPart(ctx, rawURL, headers)
	if err != nil || string(data) != "stored bytes" {
		t.Fatalf("readPresignedPart = %q, %v", data, err)
	}

	sum := sha256.Sum256([]byte("part two"))
	want := []seen{
		{method: http.MethodPut, body: "part one", custom: "yes"},
		{method: http.MethodPut, body: "part two", custom: "yes", sha256: base64.StdEncoding.EncodeToString(sum[:])},
		{method: http.MethodPut, body: "part three", custom: "yes", crc32c: computeCRC32C([]byte("part three"))},
		{method: http.MethodGet, custom: "yes"},
	}
	if len(got) != len(want) {
		t.Fatalf("server saw %d requests, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].host != strings.TrimPrefix(server.URL, "https://") {
			t.Errorf("request %d used Host %q, want the URL host", i, got[i].host)
		}
		got[i].host = ""
		if got[i] != want[i] {
			t.Errorf("request %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}

// A 300 response is not followed by net/http, so ReadFile parses its Location
// itself; that parse error must not carry the raw value either.
func TestReadFileMalformedMultipleChoicesLocationDoesNotLeak(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://bucket.example.com/large%zz.bin?X-Amz-Signature=secret-signature")
		w.WriteHeader(http.StatusMultipleChoices)
	}))
	defer server.Close()

	client := testBearerClient(t, server.URL)
	_, err := client.ReadFile(context.Background(), "/workspace/large.bin")
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "api.invalid_redirect" {
		t.Fatalf("error = %v, want api.invalid_redirect", err)
	}
	if strings.Contains(err.Error(), "secret-signature") || strings.Contains(err.Error(), "large%zz") {
		t.Fatalf("error leaks the redirect URL: %v", err)
	}
}

func TestReadFileMalformedStorageLocationDoesNotLeak(t *testing.T) {
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "https://bucket.example.com/large%zz.bin?X-Amz-Signature=secret-signature")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer storage.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", storage.URL+"/signed/large.bin")
		w.WriteHeader(http.StatusMultipleChoices)
	}))
	defer server.Close()

	client := testBearerClient(t, server.URL)
	_, err := client.ReadFile(context.Background(), "/workspace/large.bin")
	var appErr *apperr.Error
	if !errors.As(err, &appErr) || appErr.Code != "api.invalid_redirect" {
		t.Fatalf("error = %v, want api.invalid_redirect", err)
	}
	if strings.Contains(err.Error(), "secret-signature") {
		t.Fatalf("error leaks the presigned signature: %v", err)
	}
}
