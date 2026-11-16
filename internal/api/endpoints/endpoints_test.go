package endpoints

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/config"
	"github.com/tidbcloud/ti-cli/internal/config/region"
)

func TestResolveStarterEndpoint(t *testing.T) {
	resolver := NewResolver()
	endpoint, err := resolver.ResolveStarter(region.ProviderAWS, "us-east-1")
	if err != nil {
		t.Fatalf("ResolveStarter failed: %v", err)
	}
	if endpoint.BaseURL != DefaultStarterBaseURL {
		t.Fatalf("unexpected base URL %q", endpoint.BaseURL)
	}
	if endpoint.RegionName != "regions/aws-us-east-1" {
		t.Fatalf("unexpected region name %q", endpoint.RegionName)
	}
}

func TestResolveStarterAlibabaRegion(t *testing.T) {
	endpoint, err := NewResolver().ResolveStarter(region.ProviderAlibabaCloud, "ap-southeast-1")
	if err != nil {
		t.Fatalf("ResolveStarter failed: %v", err)
	}
	if endpoint.APIProvider != "alicloud" {
		t.Fatalf("unexpected API provider %q", endpoint.APIProvider)
	}
	if endpoint.RegionName != "regions/alicloud-ap-southeast-1" {
		t.Fatalf("unexpected region name %q", endpoint.RegionName)
	}
}

func TestResolveStarterTestOverrideRequiresOptIn(t *testing.T) {
	setLocalBuild(t, true)
	t.Setenv("TI_TEST_STARTER_BASE_URL", "https://starter.test")
	t.Setenv("TI_ALLOW_TEST_ENDPOINTS", "")
	endpoint, err := NewResolver().ResolveStarter(region.ProviderAWS, "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.BaseURL != DefaultStarterBaseURL {
		t.Fatalf("test override used without opt-in: %q", endpoint.BaseURL)
	}

	t.Setenv("TI_ALLOW_TEST_ENDPOINTS", "1")
	endpoint, err = NewResolver().ResolveStarter(region.ProviderAWS, "us-east-1")
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.BaseURL != "https://starter.test" {
		t.Fatalf("test override not applied: %q", endpoint.BaseURL)
	}
}

func TestResolveIAMEndpoint(t *testing.T) {
	endpoint, err := NewResolver().ResolveIAM()
	if err != nil {
		t.Fatalf("ResolveIAM failed: %v", err)
	}
	if endpoint.BaseURL != DefaultIAMBaseURL {
		t.Fatalf("unexpected IAM base URL %q", endpoint.BaseURL)
	}
}

func TestResolveIAMTestOverrideRequiresOptIn(t *testing.T) {
	setLocalBuild(t, true)
	t.Setenv("TI_TEST_IAM_BASE_URL", "https://iam.test")
	t.Setenv("TI_ALLOW_TEST_ENDPOINTS", "")
	endpoint, err := NewResolver().ResolveIAM()
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.BaseURL != DefaultIAMBaseURL {
		t.Fatalf("test override used without opt-in: %q", endpoint.BaseURL)
	}

	t.Setenv("TI_ALLOW_TEST_ENDPOINTS", "1")
	endpoint, err = NewResolver().ResolveIAM()
	if err != nil {
		t.Fatal(err)
	}
	if endpoint.BaseURL != "https://iam.test" {
		t.Fatalf("test override not applied: %q", endpoint.BaseURL)
	}
}

// testCatalogServer serves one Filesystem region catalog page and counts hits.
func testCatalogServer(t *testing.T, regions string) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		_, _ = w.Write([]byte(`{"regions":[` + regions + `],"totalSize":1,"nextPageToken":""}`))
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

const activeUSPrivateEast1 = `{"regionId":"aws-us-east-1","cloudProvider":"aws","displayName":"N. Virginia (us-east-1)","serviceAddress":"https://aws-us-east-1.drive9.ai","state":"ACTIVE"}`

func testProfileWithKeys(home string) *config.Profile {
	return &config.Profile{
		Name:                config.DefaultProfile,
		HomeDir:             home,
		TiDBCloudPublicKey:  "public",
		TiDBCloudPrivateKey: "private",
	}
}

func TestResolveFSProfileFromCatalog(t *testing.T) {
	server, hits := testCatalogServer(t, activeUSPrivateEast1)
	profile := testProfileWithKeys(t.TempDir())
	profile.FSPlacementRegionCode = "aws-us-east-1"
	endpoint, err := (Resolver{FSRegionsBaseURL: server.URL}).ResolveFSProfile(context.Background(), profile)
	if err != nil {
		t.Fatalf("ResolveFSProfile failed: %v", err)
	}
	if endpoint.BaseURL != "https://aws-us-east-1.drive9.ai" || endpoint.RegionCode != "aws-us-east-1" {
		t.Fatalf("unexpected endpoint: %#v", endpoint)
	}
	if endpoint.Provider != region.ProviderAWS {
		t.Fatalf("unexpected provider: %#v", endpoint)
	}
	if got := atomic.LoadInt32(hits); got != 1 {
		t.Fatalf("catalog requests = %d, want 1", got)
	}
}

func TestResolveFSRegionRejectsUnpublishedRegion(t *testing.T) {
	server, _ := testCatalogServer(t, activeUSPrivateEast1)
	profile := testProfileWithKeys(t.TempDir())
	_, err := (Resolver{FSRegionsBaseURL: server.URL}).ResolveFSRegion(context.Background(), profile, "aws-us-west-2")
	if err == nil {
		t.Fatal("expected unsupported fs region to fail")
	}
	if got := apperr.ExitCodeFor(err); got != 2 {
		t.Fatalf("expected exit 2, got %d", got)
	}
	if code := apperr.CodeFor(err); code != "api.fs_endpoint_unsupported" {
		t.Fatalf("unexpected code %q", code)
	}
	message := apperr.MessageFor(err)
	if !strings.Contains(message, "ti fs is not available") || !strings.Contains(message, "aws-us-east-1") {
		t.Fatalf("unexpected message: %q", message)
	}
}

func TestResolveFSRegionRejectsUnmappedCloudProvider(t *testing.T) {
	server, _ := testCatalogServer(t, `{"regionId":"aws-us-east-1","cloudProvider":"tencent","displayName":"N. Virginia (us-east-1)","serviceAddress":"https://aws-us-east-1.drive9.ai","state":"ACTIVE"}`)
	profile := testProfileWithKeys(t.TempDir())
	_, err := (Resolver{FSRegionsBaseURL: server.URL}).ResolveFSRegion(context.Background(), profile, "aws-us-east-1")
	if err == nil {
		t.Fatal("expected an unmapped cloud provider to be unroutable")
	}
	if code := apperr.CodeFor(err); code != "api.fs_endpoint_unsupported" {
		t.Fatalf("unexpected code %q", code)
	}
	if exit := apperr.ExitCodeFor(err); exit != 2 {
		t.Fatalf("exit = %d, want 2", exit)
	}
	for _, want := range []string{"tencent", "does not support"} {
		if message := apperr.MessageFor(err); !strings.Contains(message, want) {
			t.Fatalf("error message %q is missing %q", message, want)
		}
	}
}

func TestResolveFSRegionRejectsInactiveRegion(t *testing.T) {
	server, _ := testCatalogServer(t, `{"regionId":"aws-us-west-2","cloudProvider":"aws","displayName":"Oregon (us-west-2)","serviceAddress":"https://aws-us-west-2.drive9.ai","state":"PENDING"}`)
	profile := testProfileWithKeys(t.TempDir())
	_, err := (Resolver{FSRegionsBaseURL: server.URL}).ResolveFSRegion(context.Background(), profile, "aws-us-west-2")
	if err == nil {
		t.Fatal("expected a non-ACTIVE region to fail")
	}
	if message := apperr.MessageFor(err); !strings.Contains(message, "pending") {
		t.Fatalf("expected the inactive state in the message, got %q", message)
	}
}

func TestResolveFSWithoutCredentialsFails(t *testing.T) {
	_, err := NewResolver().ResolveFSProfile(context.Background(), &config.Profile{Name: config.DefaultProfile, FSPlacementRegionCode: "aws-us-east-1"})
	if err == nil {
		t.Fatal("expected resolution without credentials to fail")
	}
	if code := apperr.CodeFor(err); code != "fs.region_catalog_unavailable" {
		t.Fatalf("unexpected code %q", code)
	}
	if got := apperr.ExitCodeFor(err); got != 2 {
		t.Fatalf("expected exit 2, got %d", got)
	}
}

func TestResolveFSCredentialEndpointUsesStoredAddress(t *testing.T) {
	profile := &config.Profile{
		Name:                  config.DefaultProfile,
		FSTenantID:            "tenant-1",
		FSPlacementRegionCode: "aws-us-east-1",
		FSCloudProvider:       region.ProviderAWS,
		FSServiceAddress:      "https://aws-us-east-1.drive9.ai/",
	}
	endpoint, err := NewResolver().ResolveFSCredentialEndpoint(profile)
	if err != nil {
		t.Fatalf("ResolveFSCredentialEndpoint failed: %v", err)
	}
	if endpoint.BaseURL != "https://aws-us-east-1.drive9.ai" || endpoint.RegionCode != "aws-us-east-1" {
		t.Fatalf("unexpected endpoint: %#v", endpoint)
	}
}

func TestResolveFSCredentialEndpointDerivesTheAddressWithoutARecordedOne(t *testing.T) {
	profile := &config.Profile{Name: config.DefaultProfile, FSTenantID: "tenant-1", FSPlacementRegionCode: "aws-us-east-1"}
	endpoint, err := NewResolver().ResolveFSCredentialEndpoint(profile)
	if err != nil {
		t.Fatalf("ResolveFSCredentialEndpoint failed: %v", err)
	}
	if endpoint.BaseURL != "https://aws-us-east-1.drive9.ai" || endpoint.RegionCode != "aws-us-east-1" {
		t.Fatalf("unexpected endpoint: %#v", endpoint)
	}
}

func TestResolveFSCredentialEndpointDerivesForUnpublishedRegions(t *testing.T) {
	// Derivation is the offline fallback: it must work for any structurally
	// valid canonical code without a catalog request.
	profile := &config.Profile{Name: config.DefaultProfile, FSPlacementRegionCode: "aws-us-east-3"}
	endpoint, err := NewResolver().ResolveFSCredentialEndpoint(profile)
	if err != nil {
		t.Fatalf("ResolveFSCredentialEndpoint failed: %v", err)
	}
	if endpoint.BaseURL != "https://aws-us-east-3.drive9.ai" {
		t.Fatalf("unexpected endpoint: %#v", endpoint)
	}
}

func TestResolveFSCredentialEndpointIgnoresAnUnusableRecordedAddress(t *testing.T) {
	profile := &config.Profile{
		Name:                  config.DefaultProfile,
		FSTenantID:            "tenant-1",
		FSPlacementRegionCode: "aws-us-east-1",
		FSServiceAddress:      "http://aws-us-east-1.drive9.ai",
	}
	endpoint, err := NewResolver().ResolveFSCredentialEndpoint(profile)
	if err != nil {
		t.Fatalf("ResolveFSCredentialEndpoint failed: %v", err)
	}
	if endpoint.BaseURL != "https://aws-us-east-1.drive9.ai" {
		t.Fatalf("expected the derived address, got %#v", endpoint)
	}
}

func TestFetchFSRegionsReadsThePublishedCatalog(t *testing.T) {
	server, hits := testCatalogServer(t, activeUSPrivateEast1)
	profile := testProfileWithKeys(t.TempDir())
	catalog, err := (Resolver{FSRegionsBaseURL: server.URL}).FetchFSRegions(context.Background(), profile)
	if err != nil {
		t.Fatalf("FetchFSRegions failed: %v", err)
	}
	if len(catalog.Entries) != 1 || catalog.Entries[0].ServiceAddress != "https://aws-us-east-1.drive9.ai" {
		t.Fatalf("unexpected catalog: %#v", catalog)
	}
	if got := atomic.LoadInt32(hits); got != 1 {
		t.Fatalf("catalog requests = %d, want 1", got)
	}
}

func TestFetchFSRegionsNeverFallsBackToLocalState(t *testing.T) {
	profile := testProfileWithKeys(t.TempDir())
	_, err := (Resolver{FSRegionsBaseURL: "http://127.0.0.1:1"}).FetchFSRegions(context.Background(), profile)
	if err == nil || apperr.CodeFor(err) != "api.fs_regions_unavailable" {
		t.Fatalf("expected a catalog fetch failure, got %v", err)
	}
}

func TestResolveFSRejectsPlainHTTPCatalogAddress(t *testing.T) {
	server, _ := testCatalogServer(t, `{"regionId":"aws-us-east-1","cloudProvider":"aws","displayName":"N. Virginia (us-east-1)","serviceAddress":"http://aws-us-east-1.drive9.ai","state":"ACTIVE"}`)
	profile := testProfileWithKeys(t.TempDir())
	_, err := (Resolver{FSRegionsBaseURL: server.URL}).ResolveFSRegion(context.Background(), profile, "aws-us-east-1")
	if err == nil || apperr.CodeFor(err) != "api.fs_regions_invalid" {
		t.Fatalf("expected the catalog to reject a plain http service address, got %v", err)
	}
}

func TestResolveStarterRejectsFilesystemOnlyPlacement(t *testing.T) {
	resolver := NewResolver()
	for _, test := range []struct {
		provider  string
		region    string
		canonical string
	}{
		{provider: region.ProviderGCP, region: "us-east1", canonical: "gcp-us-east1"},
		{provider: region.ProviderAzure, region: "centralus", canonical: "azure-centralus"},
	} {
		t.Run(test.canonical, func(t *testing.T) {
			if _, err := resolver.ResolveStarter(test.provider, test.region); err == nil {
				t.Fatal("expected Starter resolution to reject the Filesystem-only placement")
			} else {
				if got := apperr.ExitCodeFor(err); got != 2 {
					t.Fatalf("expected exit code 2, got %d", got)
				}
				if message := apperr.MessageFor(err); !strings.Contains(message, test.canonical) || !strings.Contains(message, "Filesystem only") {
					t.Fatalf("unexpected Starter error message %q", message)
				}
			}
		})
	}
}

func TestResolveUnsupportedRegionShowsValidRegions(t *testing.T) {
	_, err := NewResolver().ResolveStarter(region.ProviderAlibabaCloud, "us-east-1")
	if err == nil {
		t.Fatal("expected invalid region to fail")
	}
	if got := apperr.ExitCodeFor(err); got != 2 {
		t.Fatalf("expected exit 2, got %d", got)
	}
	message := apperr.MessageFor(err)
	if !strings.Contains(message, "ap-southeast-1") {
		t.Fatalf("expected valid region list in message, got %q", message)
	}
}

func TestValidateBaseURLAllowsPlainHTTPOnlyForLoopback(t *testing.T) {
	for _, tc := range []struct {
		value string
		ok    bool
	}{
		{"https://serverless.tidbapi.com", true},
		{"https://example.com:8443/base", true},
		{"http://127.0.0.1:8080", true},
		{"http://127.1.2.3", true},
		{"http://localhost:8080", true},
		{"http://LOCALHOST", true},
		{"http://[::1]:8080", true},
		{"http://example.com", false},
		{"http://10.0.0.1", false},
		{"http://localhost.example.com", false},
		{"ftp://example.com", false},
		{"https://", false},
	} {
		err := validateBaseURL(tc.value)
		if tc.ok && err != nil {
			t.Fatalf("%s: expected valid, got %v", tc.value, err)
		}
		if !tc.ok && err == nil {
			t.Fatalf("%s: expected an error", tc.value)
		}
	}
}
