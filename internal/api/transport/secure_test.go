package transport

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCheckSecureURL(t *testing.T) {
	for _, raw := range []string{
		"https://bucket.s3.amazonaws.com/key?X-Amz-Signature=abc",
		"HTTPS://bucket.oss-ap-southeast-1.aliyuncs.com/key",
		"http://127.0.0.1:8080/upload",
		"http://localhost/upload",
		"http://[::1]:9000/upload",
	} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		if err := CheckSecureURL(u); err != nil {
			t.Errorf("CheckSecureURL(%q) = %v, want nil", raw, err)
		}
	}
	for _, raw := range []string{
		"http://bucket.s3.amazonaws.com/key?X-Amz-Signature=secret-signature",
		"ftp://example.com/file",
		"https://user:pass@example.com/file",
		"/relative/path",
		"mailto:someone@example.com",
	} {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatalf("parse %q: %v", raw, err)
		}
		err = CheckSecureURL(u)
		if !errors.Is(err, ErrInsecureURL) {
			t.Errorf("CheckSecureURL(%q) = %v, want ErrInsecureURL", raw, err)
			continue
		}
		for _, leaked := range []string{"secret-signature", "pass", "/key", "/file"} {
			if strings.Contains(err.Error(), leaked) {
				t.Errorf("CheckSecureURL(%q) error %q leaks %q", raw, err, leaked)
			}
		}
	}
	if err := CheckSecureURL(nil); !errors.Is(err, ErrInsecureURL) {
		t.Fatalf("CheckSecureURL(nil) = %v, want ErrInsecureURL", err)
	}
}

func TestDoSecureRefusesInsecureURLBeforeSending(t *testing.T) {
	var called atomic.Bool
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		called.Store(true)
		return nil, errors.New("must not send")
	})}
	req, err := http.NewRequest(http.MethodPut, "http://bucket.example.com/key?X-Amz-Signature=secret-signature", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = DoSecure(SecureTransferClient(client), req)
	if !errors.Is(err, ErrInsecureURL) {
		t.Fatalf("DoSecure error = %v, want ErrInsecureURL", err)
	}
	if strings.Contains(err.Error(), "secret-signature") {
		t.Fatalf("DoSecure error leaks the signature: %v", err)
	}
	if called.Load() {
		t.Fatal("DoSecure sent a request to an insecure URL")
	}
}

func TestDoSecureRefusesRedirectToHTTP(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://bucket.example.com/key?X-Amz-Signature=secret-signature", http.StatusFound)
	}))
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/start?X-Amz-Signature=first-signature", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = DoSecure(SecureTransferClient(nil), req)
	if !errors.Is(err, ErrInsecureURL) {
		t.Fatalf("DoSecure error = %v, want ErrInsecureURL", err)
	}
	for _, leaked := range []string{"secret-signature", "first-signature"} {
		if strings.Contains(err.Error(), leaked) {
			t.Fatalf("DoSecure error %q leaks %q", err, leaked)
		}
	}
}

func TestDoSecureFollowsRelativeRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			w.Header().Set("Location", "/final")
			w.WriteHeader(http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/start", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := DoSecure(SecureTransferClient(nil), req)
	if err != nil {
		t.Fatalf("DoSecure: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNoContent || res.Request.URL.Path != "/final" {
		t.Fatalf("got %d at %s, want 204 at /final", res.StatusCode, res.Request.URL.Path)
	}
}

func TestDoSecureStopsAfterMaxRedirects(t *testing.T) {
	var hops atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops.Add(1)
		http.Redirect(w, r, "/again", http.StatusFound)
	}))
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/start", nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = DoSecure(SecureTransferClient(nil), req)
	if !errors.Is(err, ErrTooManyRedirects) {
		t.Fatalf("DoSecure error = %v, want ErrTooManyRedirects", err)
	}
	// Like net/http's default policy: the original request plus nine redirects
	// are sent, and the tenth redirect is refused.
	if got := hops.Load(); got != MaxRedirects {
		t.Fatalf("server saw %d requests, want %d", got, MaxRedirects)
	}
}

func TestSecureTransferClientKeepsCallerPolicy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "/final", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	base := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	res, err := SecureTransferClient(base).Get(server.URL + "/start")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusFound {
		t.Fatalf("status = %d, want the caller policy to stop at 302", res.StatusCode)
	}
	if base.CheckRedirect == nil {
		t.Fatal("SecureTransferClient changed the base client")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}
