//go:build windows

package fs

import (
	"context"

	apifs "github.com/tidbcloud/ti-cli/internal/api/fs"
	"github.com/tidbcloud/ti-cli/internal/apperr"
)

func (s Service) mountFUSEForeground(ctx context.Context, inputs mountInputs, remote apifs.StatusResponse, checks []MountRuntimeCheck) (MountResult, error) {
	return MountResult{}, apperr.New("fs.fuse_unsupported", "runtime", 1, "ti fs FUSE mount is not supported on Windows; explicitly use --driver webdav if a WebDAV mount is available")
}
