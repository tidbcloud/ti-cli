package endpoints

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/config"
	"github.com/tidbcloud/ti-cli/internal/config/region"
	"github.com/tidbcloud/ti-cli/internal/version"
)

// setLocalBuild makes the resolver behave as if ti were a local build (or a
// release build) for one test; TI_ALLOW_TEST_ENDPOINTS still decides. A go test
// binary is neither, so test controls are off unless a test sets this.
func setLocalBuild(t *testing.T, local bool) {
	t.Helper()
	info := version.Info{InstallSource: "archive"}
	if local {
		info.InstallSource = "local"
	}
	previous := testControlsAllowed
	testControlsAllowed = info.TestControlsAllowed
	t.Cleanup(func() { testControlsAllowed = previous })
}

func overrideEnvironment(t *testing.T) *atomic.Int32 {
	t.Helper()
	var catalogRequests atomic.Int32
	catalog := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		catalogRequests.Add(1)
		_, _ = w.Write([]byte(`{"regions":[{"name":"regions/aws-us-east-1","regionId":"aws-us-east-1","cloudProvider":"aws","displayName":"N. Virginia (us-east-1)","serviceAddress":"https://override.drive9.example","state":"ACTIVE"}],"totalSize":1,"nextPageToken":""}`))
	}))
	t.Cleanup(catalog.Close)
	t.Setenv("TI_ALLOW_TEST_ENDPOINTS", "1")
	t.Setenv("TI_TEST_STARTER_BASE_URL", "https://starter.override.example")
	t.Setenv("TI_TEST_IAM_BASE_URL", "https://iam.override.example")
	t.Setenv("TI_TEST_FS_REGIONS_URL", catalog.URL)
	return &catalogRequests
}

func overrideProfile(t *testing.T) *config.Profile {
	t.Helper()
	return &config.Profile{
		Name:                config.DefaultProfile,
		HomeDir:             t.TempDir(),
		TiDBCloudPublicKey:  "public",
		TiDBCloudPrivateKey: "private",
	}
}

func TestNewResolverIgnoresTestOverridesInReleaseBuilds(t *testing.T) {
	catalogRequests := overrideEnvironment(t)
	setLocalBuild(t, false)
	resolver := NewResolver()

	starter, err := resolver.ResolveStarter(region.ProviderAWS, "us-east-1")
	if err != nil || starter.BaseURL != DefaultStarterBaseURL {
		t.Fatalf("Starter endpoint = %q, %v; want the default", starter.BaseURL, err)
	}
	iam, err := resolver.ResolveIAM()
	if err != nil || iam.BaseURL != DefaultIAMBaseURL {
		t.Fatalf("IAM endpoint = %q, %v; want the default", iam.BaseURL, err)
	}
	if _, err := resolver.ResolveFSProfile(overrideProfile(t)); err == nil {
		t.Fatal("expected Filesystem resolution to fail without the test catalog")
	}
	if got := catalogRequests.Load(); got != 0 {
		t.Fatalf("overridden FS region catalog was fetched %d times", got)
	}
}

func TestNewResolverAppliesTestOverridesInLocalBuilds(t *testing.T) {
	catalogRequests := overrideEnvironment(t)
	setLocalBuild(t, true)
	resolver := NewResolver()

	starter, err := resolver.ResolveStarter(region.ProviderAWS, "us-east-1")
	if err != nil || starter.BaseURL != "https://starter.override.example" {
		t.Fatalf("Starter endpoint = %q, %v; want the override", starter.BaseURL, err)
	}
	iam, err := resolver.ResolveIAM()
	if err != nil || iam.BaseURL != "https://iam.override.example" {
		t.Fatalf("IAM endpoint = %q, %v; want the override", iam.BaseURL, err)
	}
	profile := overrideProfile(t)
	profile.FSPlacementRegionCode = "aws-us-east-1"
	fs, err := resolver.ResolveFSProfile(profile)
	if err != nil || fs.BaseURL != "https://override.drive9.example" {
		t.Fatalf("FS endpoint = %q, %v; want the catalog override", fs.BaseURL, err)
	}
	if got := catalogRequests.Load(); got != 1 {
		t.Fatalf("FS region catalog requests = %d, want 1", got)
	}
}
