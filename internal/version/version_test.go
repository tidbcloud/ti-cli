package version

import (
	"os"
	"strings"
	"testing"
)

func TestTestControlsAllowedNeedsLocalBuildAndExactOptIn(t *testing.T) {
	for _, tc := range []struct {
		installSource string
		allow         string
		want          bool
	}{
		{"local", "1", true},
		{"local", "", false},
		{"local", "true", false},
		{"local", "yes", false},
		{"local", " 1", false},
		{"local", "01", false},
		{"LOCAL", "1", false},
		{"archive", "1", false},
		{"script", "1", false},
		{"homebrew", "1", false},
		{"scoop", "1", false},
		{"winget", "1", false},
		{"unknown", "1", false},
		{"", "1", false},
	} {
		t.Setenv("TI_ALLOW_TEST_ENDPOINTS", tc.allow)
		if got := (Info{InstallSource: tc.installSource}).TestControlsAllowed(); got != tc.want {
			t.Fatalf("installSource=%q TI_ALLOW_TEST_ENDPOINTS=%q: got %t, want %t", tc.installSource, tc.allow, got, tc.want)
		}
	}
}

// Release binaries must never honour hidden test controls, so the release
// build must stamp installSource=archive and must not set the test tag.
func TestGoReleaserBuildsAreNotLocal(t *testing.T) {
	data, err := os.ReadFile("../../.goreleaser.yaml")
	if err != nil {
		t.Fatalf("read .goreleaser.yaml: %v", err)
	}
	config := string(data)
	stamps := strings.Count(config, "/internal/version.installSource=")
	archive := strings.Count(config, "/internal/version.installSource=archive")
	if stamps == 0 || stamps != archive {
		t.Fatalf("every goreleaser build must stamp installSource=archive (stamps=%d, archive=%d)", stamps, archive)
	}
	if builds := strings.Count(config, "main: ./cmd/ti\n"); builds != stamps {
		t.Fatalf("%d ti builds but %d installSource stamps", builds, stamps)
	}
	if strings.Contains(config, "ti_test_controls") {
		t.Fatal("goreleaser must not build with the ti_test_controls tag")
	}
}
