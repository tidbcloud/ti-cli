//go:build unix

package fswrap

import (
	"os"
	"syscall"
)

// fileOwnerUID reports the owner of the file described by info, when the
// platform exposes it.
func fileOwnerUID(info os.FileInfo) (uint32, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return 0, false
	}
	return uint32(stat.Uid), true
}

// fileGroupID reports the group of the file described by info.
func fileGroupID(info os.FileInfo) (uint32, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return 0, false
	}
	return uint32(stat.Gid), true
}

// currentProcessGroup reports whether gid belongs to this process. A binary that
// is only group-writable within the user's own groups is accepted, so a umask of
// 002 does not make every install unusable.
func currentProcessGroup(gid uint32) bool {
	if uint32(os.Getegid()) == gid {
		return true
	}
	groups, err := os.Getgroups()
	if err != nil {
		return false
	}
	for _, group := range groups {
		if uint32(group) == gid {
			return true
		}
	}
	return false
}
