package version

import (
	"fmt"
	"os"
	"runtime"
)

var (
	version           = "dev"
	commit            = "none"
	date              = "unknown"
	installSource     = "unknown"
	releaseChannel    = "stable"
	telemetryEndpoint = ""
)

type Info struct {
	Version           string
	Commit            string
	Date              string
	OS                string
	Arch              string
	InstallSource     string
	ReleaseChannel    string
	TelemetryEndpoint string
}

func Current() Info {
	return Info{
		Version:           version,
		Commit:            commit,
		Date:              date,
		OS:                runtime.GOOS,
		Arch:              runtime.GOARCH,
		InstallSource:     installSource,
		ReleaseChannel:    releaseChannel,
		TelemetryEndpoint: telemetryEndpoint,
	}
}

func (i Info) String() string {
	return fmt.Sprintf("ti %s (%s, %s, %s/%s)", i.Version, i.Commit, i.Date, i.OS, i.Arch)
}

// TestControlsAllowed reports whether hidden test and developer controls
// (TI_TEST_*, TI_RELEASE_API_BASE_URL and TI_DRIVE9_BIN) may apply. They do only
// in a local build, which only make build stamps, with
// TI_ALLOW_TEST_ENDPOINTS=1; release builds (installSource=archive), go install
// and go test binaries always ignore them.
func (i Info) TestControlsAllowed() bool {
	return i.InstallSource == "local" && os.Getenv("TI_ALLOW_TEST_ENDPOINTS") == "1"
}
