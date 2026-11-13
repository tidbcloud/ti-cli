//go:build !unix

package fswrap

import "os"

// fileOwnerUID is unavailable on this platform, so ownership is not enforced.
var fileOwnerUID = func(info os.FileInfo) (uint32, bool) { return 0, false }

// fileGroupID is unavailable on this platform, so group ownership is not
// enforced.
var fileGroupID = func(info os.FileInfo) (uint32, bool) { return 0, false }

// currentProcessGroup accepts every group on platforms without Unix groups.
var currentProcessGroup = func(gid uint32) bool { return true }

// validateCompanionFileMode does not enforce POSIX mode bits on this platform:
// Windows derives them from the read-only attribute (a normal file reports
// 0666), so the Unix checks would reject every legitimate install. Access
// control there is carried by ACLs, which the companion installer owns.
func validateCompanionFileMode(info os.FileInfo) error {
	return nil
}

// validateCompanionDirMode does not enforce POSIX mode bits on this platform.
func validateCompanionDirMode(info os.FileInfo, dir string) error {
	return nil
}
