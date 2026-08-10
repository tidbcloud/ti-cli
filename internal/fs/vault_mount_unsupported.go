//go:build windows

package fs

import (
	"context"

	"github.com/tidbcloud/ti-cli/internal/apperr"
)

func (s Service) mountVaultForeground(ctx context.Context, inputs vaultMountInputs, checks []MountRuntimeCheck) (MountResult, error) {
	_ = ctx
	_ = inputs
	_ = checks
	return MountResult{}, apperr.New("vault.mount_unsupported", "runtime", 1, "ti fs-vault FUSE mount is not supported on Windows; use ti fs-vault read-secret/list-secrets or run mount-vault on macOS or Linux")
}
