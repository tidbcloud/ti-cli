//go:build !darwin && !linux

package fs

// ti fs background mounts are supported on macOS and Linux only. On other
// platforms there is no supported driver to detect, so mount evidence cannot
// be observed and readiness falls back to the readability probe alone.
func defaultMountPointActive(string) (bool, error) {
	return true, nil
}
