//go:build darwin

package fs

import (
	"os"
	"path/filepath"
	"syscall"
)

// defaultMountPointActive reports whether mountPath is the root of an active
// mount. On macOS a mount root sits on a different filesystem than its parent
// directory, so unequal st_dev values prove an active mount (FUSE, WebDAV,
// disk images); a plain subdirectory shares the parent's device.
func defaultMountPointActive(mountPath string) (bool, error) {
	if testFakeMountReady() {
		return true, nil
	}
	if mountPath == string(os.PathSeparator) {
		return true, nil
	}
	info, err := os.Stat(mountPath)
	if err != nil {
		return false, err
	}
	parent, err := os.Stat(filepath.Dir(mountPath))
	if err != nil {
		return false, err
	}
	return info.Sys().(*syscall.Stat_t).Dev != parent.Sys().(*syscall.Stat_t).Dev, nil
}
