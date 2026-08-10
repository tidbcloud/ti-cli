//go:build !windows

package homemigration

import (
	"os"
	"path/filepath"
	"syscall"
)

func processAlive(pid int) bool {
	process, err := os.FindProcess(pid)
	return err == nil && process.Signal(syscall.Signal(0)) == nil
}

func mountPointActive(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	parent, err := os.Stat(filepath.Dir(filepath.Clean(path)))
	if err != nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	parentStat, parentOK := parent.Sys().(*syscall.Stat_t)
	return ok && parentOK && stat.Dev != parentStat.Dev
}

func locatorWithoutPIDBlocksMigration() bool {
	return false
}
