package e2e

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
)

// buildReleaseStyleTI builds ti the way goreleaser does: installSource=archive
// and no ti_test_controls tag. Hidden test controls must have no effect on it.
func buildReleaseStyleTI(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "ti")
	build := exec.Command("go", "build", "-trimpath",
		"-ldflags", "-X github.com/tidbcloud/ti-cli/internal/version.installSource=archive",
		"-o", bin, "./cmd/ti")
	build.Dir = ".."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build release-style ti: %v\n%s", err, output)
	}
	return bin
}

func countingServer(t *testing.T, body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

func TestReleaseBuildIgnoresHiddenTestControls(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fake companion build path is covered by unit tests on Windows")
	}
	tiBinary(t) // skip unless the e2e suite is running
	companion := filepath.Join(t.TempDir(), "ti-drive9")
	build := exec.Command("go", "build", "-o", companion, "./testdata/fake-drive9.go")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build fake companion: %v\n%s", err, output)
	}
	bin := installedLayout(t, buildReleaseStyleTI(t), companion)

	starter, starterHits := countingServer(t, `{"clusters":[]}`)
	manifest, manifestHits := countingServer(t, `{"service":"drive9","regions":[{"region_code":"aws-us-east-1","mode":"tidb_cloud_native","server_url":"https://fs-override.test","cloud_provider":"aws","tidb_region":"us-east-1"}]}`)
	telemetry, telemetryHits := countingServer(t, `{}`)
	release, releaseHits := countingServer(t, `{"tag_name":"v99.0.0","assets":[]}`)

	home := t.TempDir()
	writeE2EFile(t, filepath.Join(home, ".ti", "config"), "[default]\nregion_code = 'aws-us-east-1'\nproject_id = 'project-1'\n", 0o600)
	writeE2EFile(t, filepath.Join(home, ".ti", "credentials"), "[default]\ntidb_cloud_public_key = 'public'\ntidb_cloud_private_key = 'private'\n", 0o600)
	overrideRan := filepath.Join(t.TempDir(), "override-ran")
	override := filepath.Join(t.TempDir(), "override-drive9")
	if err := os.WriteFile(override, []byte("#!/bin/sh\ntouch '"+overrideRan+"'\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	recordPath := filepath.Join(t.TempDir(), "calls.jsonl")
	env := []string{
		"HOME=" + home,
		// The default endpoints must not be reached either: send them to a
		// dead proxy. Go doesn't proxy loopback, so the override servers stay
		// reachable if ti were to use them.
		"HTTPS_PROXY=http://127.0.0.1:9",
		"HTTP_PROXY=http://127.0.0.1:9",
		"NO_PROXY=",
		"TI_ALLOW_TEST_ENDPOINTS=1",
		"TI_TEST_STARTER_BASE_URL=" + starter.URL,
		// No command here reaches IAM. The IAM override is covered by the
		// endpoints unit tests, and every IAM client comes from NewResolver.
		"TI_TEST_IAM_BASE_URL=http://127.0.0.1:1",
		"TI_TEST_FS_MANIFEST_URL=" + manifest.URL,
		// Telemetry is off by default; turn it on so the override check can fail.
		"TI_TELEMETRY=on",
		"TI_TEST_TELEMETRY_ENDPOINT=" + telemetry.URL + "/v1/telemetry/batch",
		"TI_RELEASE_API_BASE_URL=" + release.URL,
		"TI_DRIVE9_BIN=" + override,
		"TI_TEST_FAKE_MOUNT_READY=1",
		"FAKE_DRIVE9_RECORD=" + recordPath,
	}
	fsEnv := append(append([]string{}, env...),
		"TDC_FS_TOKEN="+drive9TestToken("tenant-sandbox"),
		"TDC_REGION_CODE=aws-us-east-1",
	)

	runTIWithInput(t, bin, "", env, "db", "list-db-clusters", "--db-cluster-type", "starter", "--page-size", "1")
	runTIWithInput(t, bin, "", env, "update", "--check")

	list := runTIWithInput(t, bin, "", fsEnv, "fs", "list-files", "--path", "/")
	list.wantExitCode(0)
	calls := readFakeDrive9Calls(t, recordPath)
	if len(calls) == 0 {
		t.Fatal("the installed ti-drive9 companion did not run")
	}
	if got := calls[len(calls)-1].Server; got != "https://aws-us-east-1.drive9.ai" {
		t.Fatalf("companion DRIVE9_SERVER = %q, want the built-in FS endpoint", got)
	}

	mount := runTIWithInput(t, bin, "", fsEnv, "fs", "mount-file-system", "--mount-path", filepath.Join(home, "mount"), "--ready-timeout", "2s")
	if mount.exitCode == 0 {
		t.Fatal("a release build accepted the fake mount; TI_TEST_FAKE_MOUNT_READY must be compiled out")
	}
	mount.wantStderrContains("did not become readable")

	if _, err := os.Stat(overrideRan); !os.IsNotExist(err) {
		t.Fatalf("TI_DRIVE9_BIN override companion ran in a release build: %v", err)
	}
	for name, hits := range map[string]*atomic.Int32{
		"Starter": starterHits, "FS manifest": manifestHits,
		"telemetry": telemetryHits, "release metadata": releaseHits,
	} {
		if got := hits.Load(); got != 0 {
			t.Fatalf("%s override received %d requests from a release build", name, got)
		}
	}
}
