package endpoints

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

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
	var manifestRequests atomic.Int32
	manifest := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		manifestRequests.Add(1)
		_, _ = w.Write([]byte(`{"service":"drive9","regions":[{"region_code":"aws-us-east-1","mode":"tidb_cloud_native","server_url":"https://override.drive9.example","cloud_provider":"aws","tidb_region":"us-east-1"}]}`))
	}))
	t.Cleanup(manifest.Close)
	t.Setenv("TI_ALLOW_TEST_ENDPOINTS", "1")
	t.Setenv("TI_TEST_STARTER_BASE_URL", "https://starter.override.example")
	t.Setenv("TI_TEST_IAM_BASE_URL", "https://iam.override.example")
	t.Setenv("TI_TEST_FS_MANIFEST_URL", manifest.URL)
	return &manifestRequests
}

func TestNewResolverIgnoresTestOverridesInReleaseBuilds(t *testing.T) {
	manifestRequests := overrideEnvironment(t)
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
	fs, err := resolver.ResolveFS(region.ProviderAWS, "us-east-1")
	if err != nil || fs.BaseURL != "https://aws-us-east-1.drive9.ai" {
		t.Fatalf("FS endpoint = %q, %v; want the built-in mapping", fs.BaseURL, err)
	}
	if got := manifestRequests.Load(); got != 0 {
		t.Fatalf("overridden FS manifest was fetched %d times", got)
	}
}

func TestNewResolverAppliesTestOverridesInLocalBuilds(t *testing.T) {
	manifestRequests := overrideEnvironment(t)
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
	fs, err := resolver.ResolveFS(region.ProviderAWS, "us-east-1")
	if err != nil || fs.BaseURL != "https://override.drive9.example" {
		t.Fatalf("FS endpoint = %q, %v; want the manifest override", fs.BaseURL, err)
	}
	if got := manifestRequests.Load(); got != 1 {
		t.Fatalf("FS manifest requests = %d, want 1", got)
	}
}
