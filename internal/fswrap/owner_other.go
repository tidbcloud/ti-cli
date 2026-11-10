//go:build !unix

package fswrap

import "os"

// fileOwnerUID is unavailable on this platform, so ownership is not enforced.
func fileOwnerUID(info os.FileInfo) (uint32, bool) {
	return 0, false
}

// fileGroupID is unavailable on this platform, so group ownership is not enforced.
func fileGroupID(info os.FileInfo) (uint32, bool) {
	return 0, false
}

// currentProcessGroup accepts every group on platforms without Unix groups.
func currentProcessGroup(gid uint32) bool {
	return true
}
