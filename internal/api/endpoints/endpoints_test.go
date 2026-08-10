package endpoints

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/apperr"
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

func TestResolveFSWithTestOverrides(t *testing.T) {
	resolver := NewResolver()
	resolver.FSBaseURLs = map[ProviderRegion]string{
		{Provider: region.ProviderAWS, Region: "us-east-1"}:               "https://fs.aws.test",
		{Provider: region.ProviderAlibabaCloud, Region: "ap-southeast-1"}: "https://fs.alibaba.test",
	}

	aws, err := resolver.ResolveFS(region.ProviderAWS, "us-east-1")
	if err != nil {
		t.Fatalf("ResolveFS aws failed: %v", err)
	}
	alibaba, err := resolver.ResolveFS(region.ProviderAlibabaCloud, "ap-southeast-1")
	if err != nil {
		t.Fatalf("ResolveFS alibaba failed: %v", err)
	}
	if aws.BaseURL != "https://fs.aws.test" || alibaba.BaseURL != "https://fs.alibaba.test" {
		t.Fatalf("unexpected fs endpoints: %#v %#v", aws, alibaba)
	}
}

func TestResolveFSFromRegionManifest(t *testing.T) {
	resolver := Resolver{
		FSManifest: &FSRegionManifest{
			Default: &FSRegionManifestDefault{
				RegionCode: "aws-ap-southeast-1",
				Mode:       "tidb_cloud_starter",
			},
			Regions: []FSRegionManifestEntry{
				{
					RegionCode:    "aws-ap-southeast-1",
					Mode:          "tidb_cloud_starter",
					ServerURL:     "https://api.drive9.ai",
					CloudProvider: "aws",
					TiDBRegion:    "ap-southeast-1",
				},
				{
					RegionCode:    "aws-us-east-1",
					Mode:          DefaultFSMode,
					ServerURL:     "https://aws-us-east-1.drive9.ai",
					CloudProvider: "aws",
					TiDBRegion:    "us-east-1",
				},
			},
		},
	}

	endpoint, err := resolver.ResolveFS(region.ProviderAWS, "us-east-1")
	if err != nil {
		t.Fatalf("ResolveFS failed: %v", err)
	}
	if endpoint.BaseURL != "https://aws-us-east-1.drive9.ai" {
		t.Fatalf("unexpected fs endpoint: %#v", endpoint)
	}
	if endpoint.RegionName != "aws-us-east-1" {
		t.Fatalf("unexpected fs region name: %#v", endpoint)
	}
}

func TestResolveFSUnsupportedManifestRegion(t *testing.T) {
	resolver := Resolver{
		FSManifest: &FSRegionManifest{
			Regions: []FSRegionManifestEntry{
				{
					RegionCode:    "aws-us-east-1",
					Mode:          DefaultFSMode,
					ServerURL:     "https://aws-us-east-1.drive9.ai",
					CloudProvider: "aws",
					TiDBRegion:    "us-east-1",
				},
			},
		},
	}

	_, err := resolver.ResolveFS(region.ProviderAWS, "us-west-2")
	if err == nil {
		t.Fatal("expected unsupported fs region to fail")
	}
	if got := apperr.ExitCodeFor(err); got != 2 {
		t.Fatalf("expected exit 2, got %d", got)
	}
	message := apperr.MessageFor(err)
	if !strings.Contains(message, "ti fs is not available") || !strings.Contains(message, "aws/us-east-1") {
		t.Fatalf("unexpected message: %q", message)
	}
}

func TestResolveFSFetchesManifestURL(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{
			"service": "drive9",
			"regions": [
				{
					"region_code": "aws-us-east-1",
					"mode": "tidb_cloud_native",
					"server_url": "https://aws-us-east-1.drive9.ai",
					"cloud_provider": "aws",
					"tidb_region": "us-east-1"
				}
			]
		}`))
	}))
	defer server.Close()

	endpoint, err := (Resolver{FSManifestURL: server.URL}).ResolveFS(region.ProviderAWS, "us-east-1")
	if err != nil {
		t.Fatalf("ResolveFS failed: %v", err)
	}
	if endpoint.BaseURL != "https://aws-us-east-1.drive9.ai" {
		t.Fatalf("unexpected endpoint: %#v", endpoint)
	}
}

func TestResolveFSRetriesManifestFetch(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			http.Error(w, "temporary manifest failure", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{
			"service": "drive9",
			"regions": [
				{
					"region_code": "aws-us-east-1",
					"mode": "tidb_cloud_native",
					"server_url": "https://aws-us-east-1.drive9.ai",
					"cloud_provider": "aws",
					"tidb_region": "us-east-1"
				}
			]
		}`))
	}))
	defer server.Close()

	endpoint, err := (Resolver{FSManifestURL: server.URL}).ResolveFS(region.ProviderAWS, "us-east-1")
	if err != nil {
		t.Fatalf("ResolveFS failed after retry: %v", err)
	}
	if endpoint.BaseURL != "https://aws-us-east-1.drive9.ai" {
		t.Fatalf("unexpected endpoint: %#v", endpoint)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("manifest calls = %d, want 2", got)
	}
}

func TestResolveFSUsesCachedManifestAfterFetchFailure(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var fail atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail.Load() {
			http.Error(w, "temporary manifest failure", http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{
			"service": "drive9",
			"regions": [
				{
					"region_code": "aws-us-east-1",
					"mode": "tidb_cloud_native",
					"server_url": "https://aws-us-east-1.drive9.ai",
					"cloud_provider": "aws",
					"tidb_region": "us-east-1"
				}
			]
		}`))
	}))
	defer server.Close()

	resolver := Resolver{FSManifestURL: server.URL}
	if _, err := resolver.ResolveFS(region.ProviderAWS, "us-east-1"); err != nil {
		t.Fatalf("initial ResolveFS failed: %v", err)
	}
	fail.Store(true)
	endpoint, err := resolver.ResolveFS(region.ProviderAWS, "us-east-1")
	if err != nil {
		t.Fatalf("ResolveFS did not fall back to cache: %v", err)
	}
	if endpoint.BaseURL != "https://aws-us-east-1.drive9.ai" {
		t.Fatalf("unexpected endpoint from cache: %#v", endpoint)
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
