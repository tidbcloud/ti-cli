//go:build ti_test_controls

package fs

import (
	"testing"

	"github.com/tidbcloud/ti-cli/internal/version"
)

func TestFakeMountReadyNeedsLocalBuildAndOptIn(t *testing.T) {
	previous := testControlsAllowed
	t.Cleanup(func() { testControlsAllowed = previous })
	t.Setenv("TI_TEST_FAKE_MOUNT_READY", "1")

	for _, tc := range []struct {
		installSource string
		allow         string
		want          bool
	}{
		{"local", "1", true},
		{"local", "", false},
		{"archive", "1", false},
		{"unknown", "1", false},
	} {
		testControlsAllowed = version.Info{InstallSource: tc.installSource}.TestControlsAllowed
		t.Setenv("TI_ALLOW_TEST_ENDPOINTS", tc.allow)
		if got := testFakeMountReady(); got != tc.want {
			t.Fatalf("installSource=%q TI_ALLOW_TEST_ENDPOINTS=%q: got %t, want %t", tc.installSource, tc.allow, got, tc.want)
		}
	}
}
