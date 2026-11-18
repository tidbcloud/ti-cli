package fsregion

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/apperr"
)

func testCredentials() Credentials {
	return Credentials{ProfileName: "default", PublicKey: "public", PrivateKey: "private"}
}

func regionJSON(regionID, provider, address, state string) string {
	return fmt.Sprintf(`{"name":"regions/%s","regionId":%q,"cloudProvider":%q,"displayName":"%s","serviceAddress":%q,"state":%q}`, regionID, regionID, provider, regionID, address, state)
}

func catalogServer(t *testing.T, pages ...string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var requests atomic.Int32
	var index int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != RegionsPath {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		position := int(atomic.AddInt32(&index, 1)) - 1
		if position >= len(pages) {
			position = len(pages) - 1
		}
		_, _ = w.Write([]byte(pages[position]))
	}))
	t.Cleanup(server.Close)
	return server, &requests
}

func TestFetchReadsEveryPage(t *testing.T) {
	first := `{"regions":[` + regionJSON("aws-us-west-2", "aws", "https://aws-us-west-2.drive9.ai", "ACTIVE") + `],"totalSize":2,"nextPageToken":"page-2"}`
	second := `{"regions":[` + regionJSON("aws-us-east-1", "aws", "https://aws-us-east-1.drive9.ai", "ACTIVE") + `],"totalSize":2,"nextPageToken":""}`
	server, requests := catalogServer(t, first, second)

	catalog, err := Client{BaseURL: server.URL, Credentials: testCredentials()}.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch failed: %v", err)
	}
	if got := requests.Load(); got != 2 {
		t.Fatalf("requests = %d, want 2", got)
	}
	if len(catalog.Entries) != 2 {
		t.Fatalf("entries = %#v", catalog.Entries)
	}
	if catalog.Entries[0].RegionCode != "aws-us-east-1" || catalog.Entries[1].RegionCode != "aws-us-west-2" {
		t.Fatalf("entries are not sorted by region code: %#v", catalog.Entries)
	}
	if catalog.Entries[0].Provider != "aws" {
		t.Fatalf("entry provider = %q", catalog.Entries[0].Provider)
	}
}

func TestFetchFiltersNonActiveRegionsAtLookup(t *testing.T) {
	server, _ := catalogServer(t, `{"regions":[`+
		regionJSON("aws-us-east-1", "aws", "https://aws-us-east-1.drive9.ai", "ACTIVE")+`,`+
		regionJSON("aws-us-west-2", "aws", "https://aws-us-west-2.drive9.ai", "PENDING")+
		`],"totalSize":2,"nextPageToken":""}`)
	catalog, err := Client{BaseURL: server.URL, Credentials: testCredentials()}.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := catalog.Lookup("aws-us-west-2"); !ok {
		t.Fatal("expected the inactive region to stay in the catalog")
	}
	if codes := catalog.RoutableRegionCodes(); len(codes) != 1 || codes[0] != "aws-us-east-1" {
		t.Fatalf("routable regions = %#v", codes)
	}
}

func TestFetchRejectsContractViolations(t *testing.T) {
	for _, test := range []struct {
		name string
		page string
	}{
		{"duplicate region", `{"regions":[` + regionJSON("aws-us-east-1", "aws", "https://aws-us-east-1.drive9.ai", "ACTIVE") + `,` + regionJSON("aws-us-east-1", "aws", "https://aws-us-east-1.drive9.ai", "ACTIVE") + `],"nextPageToken":""}`},
		{"malformed region id", `{"regions":[{"regionId":"us-east-1","cloudProvider":"aws","serviceAddress":"https://fs.drive9.ai","state":"ACTIVE"}],"nextPageToken":""}`},
		{"unknown region id shape", `{"regions":[{"regionId":"aws-not a region","cloudProvider":"aws","serviceAddress":"https://fs.drive9.ai","state":"ACTIVE"}],"nextPageToken":""}`},
		{"provider mismatch", `{"regions":[` + regionJSON("aws-us-east-1", "gcp", "https://aws-us-east-1.drive9.ai", "ACTIVE") + `],"nextPageToken":""}`},
		{"missing provider", `{"regions":[{"regionId":"aws-us-east-1","serviceAddress":"https://aws-us-east-1.drive9.ai","state":"ACTIVE"}],"nextPageToken":""}`},
		{"missing state", `{"regions":[{"regionId":"aws-us-east-1","cloudProvider":"aws","serviceAddress":"https://aws-us-east-1.drive9.ai"}],"nextPageToken":""}`},
		{"plain http address", `{"regions":[` + regionJSON("aws-us-east-1", "aws", "http://aws-us-east-1.drive9.ai", "ACTIVE") + `],"nextPageToken":""}`},
		{"address with query", `{"regions":[` + regionJSON("aws-us-east-1", "aws", "https://aws-us-east-1.drive9.ai/?x=1", "ACTIVE") + `],"nextPageToken":""}`},
		{"empty catalog", `{"regions":[],"nextPageToken":""}`},
		{"repeated page token", `{"regions":[` + regionJSON("aws-us-east-1", "aws", "https://aws-us-east-1.drive9.ai", "ACTIVE") + `],"nextPageToken":"same"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server, _ := catalogServer(t, test.page)
			_, err := Client{BaseURL: server.URL, Credentials: testCredentials()}.Fetch(context.Background())
			if err == nil {
				t.Fatal("expected the catalog to be rejected")
			}
			if code := apperr.CodeFor(err); code != "api.fs_regions_invalid" {
				t.Fatalf("unexpected code %q for %v", code, err)
			}
		})
	}
}

func TestFetchKeepsTheDocumentedExitCodeContract(t *testing.T) {
	for _, test := range []struct {
		status int
		code   string
		exit   int
		want   string
	}{
		{status: http.StatusUnauthorized, code: "auth.invalid_credentials", exit: 3, want: "Check ~/.ti/credentials"},
		{status: http.StatusForbidden, code: "authz.permission_denied", exit: 4, want: `profile "default" is not allowed to read the Filesystem region catalog in the selected scope`},
		{status: http.StatusNotFound, code: "api.not_found", exit: 5, want: "remote resource not found"},
	} {
		t.Run(http.StatusText(test.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
			}))
			defer server.Close()
			_, err := Client{BaseURL: server.URL, Credentials: testCredentials()}.Fetch(context.Background())
			if err == nil {
				t.Fatal("expected the catalog request to fail")
			}
			if code := apperr.CodeFor(err); code != test.code {
				t.Fatalf("code = %q, want %q", code, test.code)
			}
			if exit := apperr.ExitCodeFor(err); exit != test.exit {
				t.Fatalf("exit = %d, want %d", exit, test.exit)
			}
			if message := apperr.MessageFor(err); !strings.Contains(message, test.want) {
				t.Fatalf("message %q is missing %q", message, test.want)
			}
		})
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	if _, err := (Client{BaseURL: server.URL, Credentials: testCredentials()}).Fetch(context.Background()); apperr.CodeFor(err) != "api.fs_regions_unavailable" || apperr.ExitCodeFor(err) != 1 {
		t.Fatalf("expected a generic api.fs_regions_unavailable exit 1 for HTTP 500, got %v", err)
	}
}

func TestFetchReportsTransportFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	_, err := Client{BaseURL: server.URL, Credentials: testCredentials()}.Fetch(context.Background())
	if err == nil || apperr.CodeFor(err) != "api.fs_regions_unavailable" {
		t.Fatalf("expected api.fs_regions_unavailable, got %v", err)
	}

	_, err = Client{BaseURL: "http://127.0.0.1:1", Credentials: testCredentials()}.Fetch(context.Background())
	if err == nil || apperr.CodeFor(err) != "api.fs_regions_unavailable" {
		t.Fatalf("expected api.fs_regions_unavailable for a transport error, got %v", err)
	}

	_, err = Client{BaseURL: server.URL}.Fetch(context.Background())
	if err == nil || apperr.CodeFor(err) != "fs.region_catalog_unavailable" {
		t.Fatalf("expected fs.region_catalog_unavailable without credentials, got %v", err)
	}
}

func TestFetchKeepsUnknownCloudProviders(t *testing.T) {
	server, _ := catalogServer(t, `{"regions":[`+
		regionJSON("aws-us-east-1", "aws", "https://aws-us-east-1.drive9.ai", "ACTIVE")+`,`+
		`{"regionId":"aws-us-west-2","cloudProvider":"tencent","serviceAddress":"https://aws-us-west-2.drive9.ai","state":"ACTIVE"}`+
		`],"nextPageToken":""}`)
	catalog, err := Client{BaseURL: server.URL, Credentials: testCredentials()}.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	unmapped, ok := catalog.Lookup("aws-us-west-2")
	if !ok {
		t.Fatal("expected the unmapped provider entry to stay listed")
	}
	if unmapped.CloudProvider != "tencent" || unmapped.Provider != "" {
		t.Fatalf("unmapped entry = %#v", unmapped)
	}
	if unmapped.Routable() {
		t.Fatal("an unmapped cloud provider must not be routable")
	}
	if codes := catalog.RoutableRegionCodes(); len(codes) != 1 || codes[0] != "aws-us-east-1" {
		t.Fatalf("routable regions = %#v", codes)
	}
	unsupported := UnsupportedProviderError(unmapped)
	if code := apperr.CodeFor(unsupported); code != "api.fs_endpoint_unsupported" {
		t.Fatalf("unexpected code %q", code)
	}
	for _, want := range []string{"aws-us-west-2", "tencent", "does not support"} {
		if message := apperr.MessageFor(unsupported); !strings.Contains(message, want) {
			t.Fatalf("error message %q is missing %q", message, want)
		}
	}
}

func TestNormalizeServiceAddress(t *testing.T) {
	for _, test := range []struct {
		value string
		want  string
		ok    bool
	}{
		{value: "https://aws-us-east-1.drive9.ai", want: "https://aws-us-east-1.drive9.ai", ok: true},
		{value: "https://aws-us-east-1.drive9.ai/", want: "https://aws-us-east-1.drive9.ai", ok: true},
		{value: " http://127.0.0.1:8080 ", want: "http://127.0.0.1:8080", ok: true},
		{value: "http://drive9.example", ok: false},
		{value: "https://user:pass@drive9.example", ok: false},
		{value: "https://drive9.example/#fragment", ok: false},
		{value: "https://drive9.example?", ok: false},
		{value: "https://:8443", ok: false},
		{value: "https://", ok: false},
		{value: "", ok: false},
	} {
		got, err := NormalizeServiceAddress(test.value)
		if test.ok != (err == nil) {
			t.Fatalf("%q: error = %v, want ok=%t", test.value, err, test.ok)
		}
		if test.ok && got != test.want {
			t.Fatalf("%q normalized to %q, want %q", test.value, got, test.want)
		}
	}
}

func TestErrorsAreActionable(t *testing.T) {
	catalog, err := Normalize([]Entry{{RegionCode: "aws-us-east-1", CloudProvider: "aws", ServiceAddress: "https://aws-us-east-1.drive9.ai", State: "ACTIVE"}})
	if err != nil {
		t.Fatal(err)
	}
	unpublished := UnpublishedRegionError("aws-us-west-2", catalog)
	if code := apperr.CodeFor(unpublished); code != "api.fs_endpoint_unsupported" {
		t.Fatalf("unexpected code %q", code)
	}
	if message := apperr.MessageFor(unpublished); !strings.Contains(message, "aws-us-east-1") {
		t.Fatalf("expected the published regions in %q", message)
	}

	inactive := UnpublishedRegionError("aws-us-west-2", Catalog{Entries: []Entry{{RegionCode: "aws-us-west-2", State: "DISABLED"}}})
	if message := apperr.MessageFor(inactive); !strings.Contains(message, "disabled") {
		t.Fatalf("expected the inactive state in %q", message)
	}

}

func TestDeriveServiceAddress(t *testing.T) {
	for _, test := range []struct {
		regionCode string
		want       string
	}{
		{"aws-us-east-1", "https://aws-us-east-1.drive9.ai"},
		{"aws-ap-southeast-1", "https://aws-ap-southeast-1.drive9.ai"},
		{"gcp-us-east1", "https://gcp-us-east1.drive9.ai"},
		{"azure-centralus", "https://azure-centralus.drive9.ai"},
		{"ali-ap-southeast-1", "https://alicloud-ap-southeast-1.drive9.ai"},
	} {
		t.Run(test.regionCode, func(t *testing.T) {
			got, err := DeriveServiceAddress(test.regionCode)
			if err != nil {
				t.Fatalf("DeriveServiceAddress failed: %v", err)
			}
			if got != test.want {
				t.Fatalf("derived address = %q, want %q", got, test.want)
			}
		})
	}
	for _, bad := range []string{"", "aws", "aws-us_east_1", "tencent-ap-beijing"} {
		if _, err := DeriveServiceAddress(bad); err == nil {
			t.Fatalf("expected %q to be rejected", bad)
		}
	}
}

// TestFetchRefusesCrossOriginRedirect covers the reported leak: the catalog
// request is Digest-authenticated with the organization API key pair, so a
// redirect to another origin must never be followed, and the second origin must
// not even receive the request.
func TestFetchRefusesCrossOriginRedirect(t *testing.T) {
	var attackerHits atomic.Int32
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attackerHits.Add(1)
		w.Header().Set("WWW-Authenticate", `Digest realm="attacker", nonce="nonce", qop="auth"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(attacker.Close)

	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, attacker.URL+RegionsPath, http.StatusFound)
	}))
	t.Cleanup(catalog.Close)

	_, err := Client{BaseURL: catalog.URL, Credentials: testCredentials()}.Fetch(context.Background())
	if apperr.CodeFor(err) != "api.fs_regions_redirect_refused" {
		t.Fatalf("error code = %q, want api.fs_regions_redirect_refused: %v", apperr.CodeFor(err), err)
	}
	message := apperr.MessageFor(err)
	if !strings.Contains(message, "refused a Filesystem region catalog redirect") ||
		!strings.Contains(message, "to "+attacker.URL) {
		t.Fatalf("refusal message is not actionable: %q", message)
	}
	if got := attackerHits.Load(); got != 0 {
		t.Fatalf("the redirect target received %d request(s); credentials could have leaked", got)
	}
}

// TestFetchAppliesRedirectPolicyToCallerClient pins that the policy survives the
// caller-supplied client seam the resolver uses for tests.
func TestFetchAppliesRedirectPolicyToCallerClient(t *testing.T) {
	var attackerHits atomic.Int32
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attackerHits.Add(1)
	}))
	t.Cleanup(attacker.Close)
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, attacker.URL+RegionsPath, http.StatusTemporaryRedirect)
	}))
	t.Cleanup(catalog.Close)

	_, err := Client{
		BaseURL:     catalog.URL,
		HTTPClient:  &http.Client{},
		Credentials: testCredentials(),
	}.Fetch(context.Background())
	if apperr.CodeFor(err) != "api.fs_regions_redirect_refused" {
		t.Fatalf("error code = %q: %v", apperr.CodeFor(err), err)
	}
	if got := attackerHits.Load(); got != 0 {
		t.Fatalf("the redirect target received %d request(s)", got)
	}
}

// TestFetchFollowsSameOriginRedirect keeps ordinary same-origin redirects working.
func TestFetchFollowsSameOriginRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/moved"+RegionsPath {
			http.Redirect(w, r, "/moved"+RegionsPath, http.StatusFound)
			return
		}
		_, _ = w.Write([]byte(`{"regions":[` + regionJSON("aws-us-east-1", "aws", "https://aws-us-east-1.drive9.ai", "ACTIVE") + `],"totalSize":1,"nextPageToken":""}`))
	}))
	t.Cleanup(server.Close)

	catalog, err := Client{BaseURL: server.URL, Credentials: testCredentials()}.Fetch(context.Background())
	if err != nil {
		t.Fatalf("same-origin redirect failed: %v", err)
	}
	if len(catalog.Entries) != 1 || catalog.Entries[0].RegionCode != "aws-us-east-1" {
		t.Fatalf("catalog = %#v", catalog.Entries)
	}
}

// TestFetchEnforcesSameOriginUnderCallerRedirectPolicy pins that the mandatory
// same-origin check cannot be relaxed by a caller-supplied client: it runs before
// the caller's policy, so a permissive caller cannot re-enable cross-origin
// redirects while a stricter caller can still narrow them further.
func TestFetchEnforcesSameOriginUnderCallerRedirectPolicy(t *testing.T) {
	var attackerHits atomic.Int32
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attackerHits.Add(1)
		w.Header().Set("WWW-Authenticate", `Digest realm="attacker", nonce="nonce", qop="auth"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(attacker.Close)
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, attacker.URL+RegionsPath, http.StatusFound)
	}))
	t.Cleanup(catalog.Close)

	permissive := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return nil }}
	_, err := Client{
		BaseURL:     catalog.URL,
		HTTPClient:  permissive,
		Credentials: testCredentials(),
	}.Fetch(context.Background())
	if apperr.CodeFor(err) != "api.fs_regions_redirect_refused" {
		t.Fatalf("permissive caller policy error = %v, want api.fs_regions_redirect_refused", err)
	}
	if got := attackerHits.Load(); got != 0 {
		t.Fatalf("the redirect target received %d request(s)", got)
	}
}

// TestFetchHonorsStricterCallerRedirectPolicy keeps the composition honest: a
// caller that refuses all redirects still wins over the same-origin policy.
func TestFetchHonorsStricterCallerRedirectPolicy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/moved"+RegionsPath, http.StatusFound)
	}))
	t.Cleanup(server.Close)

	noRedirects := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	_, err := Client{
		BaseURL:     server.URL,
		HTTPClient:  noRedirects,
		Credentials: testCredentials(),
	}.Fetch(context.Background())
	if apperr.CodeFor(err) != "api.fs_regions_unavailable" {
		t.Fatalf("stricter caller policy error = %v, want a 3xx catalog failure", err)
	}
}

// TestFetchRejectsInsecureCatalogEndpoint pins the base-URL rule: the endpoint
// spends the organization API key pair, so it must be https (or a loopback test
// server) before any request is made. Each rejection names the actual problem
// instead of blaming the wrong part of the URL.
func TestFetchRejectsInsecureCatalogEndpoint(t *testing.T) {
	for _, tc := range []struct {
		baseURL string
		want    string
	}{
		{"http://fs.tidbapi.com", "scheme must be https"},
		{"ftp://fs.tidbapi.com", "scheme must be https"},
		{"https://user:pass@fs.tidbapi.com", "credentials, query, and fragment are not allowed"},
		{"https://fs.tidbapi.com?region=aws-us-east-1", "credentials, query, and fragment are not allowed"},
		{"https://fs.tidbapi.com?", "credentials, query, and fragment are not allowed"},
		{"https://fs.tidbapi.com#frag", "credentials, query, and fragment are not allowed"},
		{"https://:8443", "it needs a scheme and a host name"},
		{"//fs.tidbapi.com", "it needs a scheme and a host name"},
		{"fs.tidbapi.com", "it needs a scheme and a host name"},
	} {
		_, err := Client{BaseURL: tc.baseURL, Credentials: testCredentials()}.Fetch(context.Background())
		if apperr.CodeFor(err) != "api.fs_regions_request" {
			t.Fatalf("endpoint %q error = %v, want api.fs_regions_request", tc.baseURL, err)
		}
		if exit := apperr.ExitCodeFor(err); exit != 2 {
			t.Fatalf("endpoint %q exit code = %d, want 2", tc.baseURL, exit)
		}
		message := apperr.MessageFor(err)
		if !strings.Contains(message, tc.want) {
			t.Fatalf("endpoint %q message = %q, want it to contain %q", tc.baseURL, message, tc.want)
		}
	}
	if message := apperr.MessageFor(func() error {
		_, err := Client{BaseURL: "fs.tidbapi.com", Credentials: testCredentials()}.Fetch(context.Background())
		return err
	}()); strings.Contains(message, "credentials, query") {
		t.Fatalf("a missing scheme must not be reported as a credentials/query problem: %q", message)
	}
}

// TestRejectedOriginsNeverEchoSecrets pins the redaction rule: a rejected
// endpoint or service address must not print the embedded password or query
// token to stderr.
func TestRejectedOriginsNeverEchoSecrets(t *testing.T) {
	const password = "sup3r-s3cret"
	const queryToken = "token-value"
	for _, raw := range []string{
		"https://user:" + password + "@fs.tidbapi.com",
		"https://fs.tidbapi.com?pageToken=" + queryToken,
		"https://user:" + password + "@fs.tidbapi.com?pageToken=" + queryToken + "#frag",
		// Inputs that do not parse as a hierarchical URL must not be echoed either:
		// Opaque carries "user:secret@host" and Path can carry a pasted token.
		"https:user:" + password + "@fs.tidbapi.com",
		password + "@fs.tidbapi.com",
		"ti_fs_" + queryToken,
	} {
		_, catalogErr := Client{BaseURL: raw, Credentials: testCredentials()}.Fetch(context.Background())
		if catalogErr == nil {
			t.Fatalf("endpoint %q was accepted", raw)
		}
		if message := apperr.MessageFor(catalogErr); strings.Contains(message, password) || strings.Contains(message, queryToken) {
			t.Fatalf("catalog endpoint error echoed a secret: %q", message)
		}
		if _, serviceErr := NormalizeServiceAddress(raw); serviceErr == nil {
			t.Fatalf("service address %q was accepted", raw)
		} else if message := apperr.MessageFor(serviceErr); strings.Contains(message, password) || strings.Contains(message, queryToken) {
			t.Fatalf("service address error echoed a secret: %q", message)
		}
	}

	if _, err := (Client{BaseURL: "http://127.0.0.1:1", Credentials: testCredentials()}).Fetch(context.Background()); apperr.CodeFor(err) != "api.fs_regions_unavailable" {
		t.Fatalf("loopback http endpoint error = %v, want a transport failure", err)
	}
}
