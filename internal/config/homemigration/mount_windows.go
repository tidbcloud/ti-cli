//go:build windows

package homemigration

import "golang.org/x/sys/windows"

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	defer windows.CloseHandle(handle)
	var exitCode uint32
	if err := windows.GetExitCodeProcess(handle, &exitCode); err != nil {
		return false
	}
	return exitCode == 259 // STILL_ACTIVE
}

func mountPointActive(string) bool {
	return false
}

// Windows mount locators do not record the companion PID. A locator is removed
// by normal unmount, so conservatively require the old CLI to clear it.
func locatorWithoutPIDBlocksMigration() bool {
	return true
}
