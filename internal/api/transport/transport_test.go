package transport

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
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

func TestSameOriginThenKeepsCallerRestrictionsWithoutWidening(t *testing.T) {
	mustURL := func(value string) *url.URL {
		parsed, err := url.Parse(value)
		if err != nil {
			t.Fatalf("parse %q: %v", value, err)
		}
		return parsed
	}
	same := &http.Request{URL: mustURL("https://fs.tidbapi.com/v1beta2/regions")}
	cross := &http.Request{URL: mustURL("https://evil.example/v1beta2/regions")}
	via := []*http.Request{{URL: mustURL("https://fs.tidbapi.com/v1beta2/regions")}}

	if SameOriginThen(nil) == nil {
		t.Fatal("nil caller returned no policy")
	}
	permissive := SameOriginThen(func(*http.Request, []*http.Request) error { return nil })
	if err := permissive(cross, via); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("a permissive caller re-enabled a cross-origin redirect: %v", err)
	}
	if err := permissive(same, via); err != nil {
		t.Fatalf("a permissive caller should not be checked twice for a same-origin hop: %v", err)
	}
	strict := SameOriginThen(func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse })
	if err := strict(same, via); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("a stricter caller must still narrow same-origin redirects: %v", err)
	}
}

func TestSameOriginRedirectKeepsCredentialsOnTheSameOrigin(t *testing.T) {
	mustURL := func(value string) *url.URL {
		parsed, err := url.Parse(value)
		if err != nil {
			t.Fatalf("parse %q: %v", value, err)
		}
		return parsed
	}
	via := []*http.Request{{URL: mustURL("https://fs.tidbapi.com/v1beta2/regions")}}
	for _, tc := range []struct {
		name    string
		target  string
		wantErr error
	}{
		{"same-origin path", "https://fs.tidbapi.com/v1beta2/regions?pageToken=next", nil},
		{"case-folded host", "https://FS.TiDBAPI.com/other", nil},
		{"other host", "https://evil.example/v1beta2/regions", http.ErrUseLastResponse},
		{"other scheme", "http://fs.tidbapi.com/v1beta2/regions", http.ErrUseLastResponse},
		{"other port", "https://fs.tidbapi.com:8443/v1beta2/regions", http.ErrUseLastResponse},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := SameOriginRedirect(&http.Request{URL: mustURL(tc.target)}, via)
			if !errors.Is(err, tc.wantErr) && err != tc.wantErr {
				t.Fatalf("SameOriginRedirect(%q) = %v, want %v", tc.target, err, tc.wantErr)
			}
		})
	}

	chain := make([]*http.Request, MaxRedirects)
	for i := range chain {
		chain[i] = &http.Request{URL: mustURL("https://fs.tidbapi.com/v1beta2/regions")}
	}
	if err := SameOriginRedirect(chain[0], chain); !errors.Is(err, ErrTooManyRedirects) {
		t.Fatalf("redirect limit error = %v, want ErrTooManyRedirects", err)
	}
}

func TestSameOriginRedirectFailsClosedOnMissingInput(t *testing.T) {
	if err := SameOriginRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("nil request error = %v, want http.ErrUseLastResponse", err)
	}
	if err := SameOriginRedirect(&http.Request{}, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("URL-less request error = %v, want http.ErrUseLastResponse", err)
	}
	if SameOrigin(nil, nil) || SameOrigin(&url.URL{Scheme: "https"}, &url.URL{Scheme: "https"}) {
		t.Fatal("SameOrigin must fail closed for URLs without an origin")
	}
}

func TestCrossOriginRedirectClassification(t *testing.T) {
	mustURL := func(value string) *url.URL {
		parsed, err := url.Parse(value)
		if err != nil {
			t.Fatalf("parse %q: %v", value, err)
		}
		return parsed
	}
	origin := mustURL("https://fs.tidbapi.com/v1beta2/regions")
	redirect := func(status int, location string, req *http.Request) *http.Response {
		res := &http.Response{StatusCode: status, Header: http.Header{}, Request: req}
		if location != "" {
			res.Header.Set("Location", location)
		}
		return res
	}

	if _, _, ok := CrossOriginRedirect(redirect(http.StatusFound, "https://evil.example/v1beta2/regions", nil), &http.Request{URL: origin}); !ok {
		t.Fatal("a cross-origin redirect was not classified")
	}
	from, to, ok := CrossOriginRedirect(redirect(http.StatusFound, "https://fs.tidbapi.com/other", nil), &http.Request{URL: origin})
	if ok {
		t.Fatalf("a same-origin redirect was classified as cross-origin: %s -> %s", from, to)
	}
	from, to, ok = CrossOriginRedirect(redirect(http.StatusFound, "https://user:pass@evil.example/?token=x#frag", nil), &http.Request{URL: origin})
	if !ok || from != "https://fs.tidbapi.com" || to != "https://evil.example" {
		t.Fatalf("origins were not reduced to scheme://host: from=%q to=%q ok=%v", from, to, ok)
	}
	for _, res := range []*http.Response{
		nil,
		redirect(http.StatusOK, "", nil),
		redirect(http.StatusNotModified, "", nil),
		redirect(http.StatusFound, "", nil),
	} {
		if _, _, ok := CrossOriginRedirect(res, &http.Request{URL: origin}); ok {
			t.Fatalf("non-cross-origin response classified as cross-origin: %#v", res)
		}
	}
	if Origin(nil) != "" || Origin(mustURL("/relative")) != "" {
		t.Fatal("Origin must be empty without a scheme and host")
	}
	if Origin(mustURL("about:blank")) != "" || Origin(mustURL("https://:8443/x")) != "" {
		t.Fatal("Origin must be empty for opaque and port-only URLs")
	}

	// Responses without their own request (custom RoundTrippers, direct callers) must
	// resolve a relative or scheme-relative Location against the fallback request.
	for _, tc := range []struct {
		name     string
		location string
		wantOK   bool
		wantTo   string
	}{
		{"relative same-origin", "/other", false, ""},
		{"scheme-relative other host", "//evil.example/other", true, "https://evil.example"},
		{"opaque target", "about:blank", false, ""},
		{"empty host target", "https:///moved", false, ""},
		{"port-only target", "https://:8443/moved", false, ""},
		{"no location", "", false, ""},
	} {
		res := redirect(http.StatusFound, tc.location, nil)
		from, to, ok := CrossOriginRedirect(res, &http.Request{URL: origin})
		if ok != tc.wantOK {
			t.Fatalf("%s: ok = %v, want %v (from=%q to=%q)", tc.name, ok, tc.wantOK, from, to)
		}
		if tc.wantOK && to != tc.wantTo {
			t.Fatalf("%s: toOrigin = %q, want %q", tc.name, to, tc.wantTo)
		}
	}
	if _, _, ok := CrossOriginRedirect(redirect(http.StatusFound, "https://evil.example/x", nil), nil); ok {
		t.Fatal("a response without any request must not be classified")
	}
}
