//go:build unix

package fswrap

import (
	"fmt"
	"os"
	"syscall"
)

// fileOwnerUID reports the owner of the file described by info, when the
// platform exposes it. It is a variable so tests can simulate foreign ownership
// without chown privileges.
var fileOwnerUID = func(info os.FileInfo) (uint32, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return 0, false
	}
	return uint32(stat.Uid), true
}

// fileGroupID reports the group of the file described by info.
var fileGroupID = func(info os.FileInfo) (uint32, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return 0, false
	}
	return uint32(stat.Gid), true
}

// currentProcessGroup reports whether gid belongs to this process. It is a
// variable so tests can simulate an unrelated group without chown privileges.
var currentProcessGroup = func(gid uint32) bool {
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

// validateCompanionFileMode applies the Unix trust rules to a companion binary:
// it must not be writable by another account and must be executable. A binary
// that is only group-writable within the caller's own groups is accepted, so a
// umask of 002 does not make every install unusable.
func validateCompanionFileMode(info os.FileInfo) error {
	perm := info.Mode().Perm()
	if perm&0o002 != 0 {
		return fmt.Errorf("world writable (mode %#o)", perm)
	}
	if perm&0o020 != 0 {
		if gid, ok := fileGroupID(info); ok && !currentProcessGroup(gid) {
			return fmt.Errorf("writable by an unrelated group (mode %#o)", perm)
		}
	}
	if perm&0o100 == 0 {
		return fmt.Errorf("not executable (mode %#o)", perm)
	}
	if uid, ok := fileOwnerUID(info); ok && uid != uint32(os.Getuid()) && uid != 0 {
		return fmt.Errorf("owned by uid %d, want the current user or root", uid)
	}
	return nil
}

// validateCompanionDirMode applies the Unix trust rules to the directory that
// contains the companion: another account must not be able to replace entries,
// and the directory must be owned by the current user or root.
func validateCompanionDirMode(info os.FileInfo, dir string) error {
	perm := info.Mode().Perm()
	if perm&0o002 != 0 && info.Mode()&os.ModeSticky == 0 {
		return fmt.Errorf("parent directory %s is world writable", dir)
	}
	if perm&0o020 != 0 {
		if gid, ok := fileGroupID(info); ok && !currentProcessGroup(gid) {
			return fmt.Errorf("parent directory %s is writable by an unrelated group", dir)
		}
	}
	if uid, ok := fileOwnerUID(info); ok && uid != uint32(os.Getuid()) && uid != 0 {
		return fmt.Errorf("parent directory %s is owned by uid %d, want the current user or root", dir, uid)
	}
	return nil
}
