package version

import (
	"fmt"
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
	return fmt.Sprintf("tdc %s (%s, %s, %s/%s)", i.Version, i.Commit, i.Date, i.OS, i.Arch)
}
