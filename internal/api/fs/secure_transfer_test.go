package fs

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
