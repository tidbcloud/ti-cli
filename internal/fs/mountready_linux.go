//go:build linux

package fs

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// defaultMountPointActive reports whether mountPath is the root of an active
// mount by checking /proc/self/mountinfo, the kernel's authoritative mount
// table. The companion mounts FUSE filesystems, which always appear there.
func defaultMountPointActive(mountPath string) (bool, error) {
	if testFakeMountReady() {
		return true, nil
	}
	candidates := mountPathCandidates(mountPath)
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return false, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Split(line, " ")
		if len(fields) < 5 {
			continue
		}
		if _, ok := candidates[unescapeMountinfoPath(fields[4])]; ok {
			return true, nil
		}
	}
	return false, nil
}

func mountPathCandidates(mountPath string) map[string]struct{} {
	candidates := map[string]struct{}{}
	for _, path := range []string{mountPath, filepath.Clean(mountPath)} {
		if path != "" {
			candidates[path] = struct{}{}
		}
	}
	if resolved, err := filepath.EvalSymlinks(mountPath); err == nil {
		candidates[resolved] = struct{}{}
	}
	if abs, err := filepath.Abs(mountPath); err == nil {
		candidates[abs] = struct{}{}
	}
	return candidates
}

// unescapeMountinfoPath decodes the octal escapes (for example \040 for a
// space) that /proc/self/mountinfo uses for special characters.
func unescapeMountinfoPath(path string) string {
	if !strings.Contains(path, "\\") {
		return path
	}
	var out strings.Builder
	for i := 0; i < len(path); i++ {
		if path[i] == '\\' && i+3 < len(path) {
			if value, err := strconv.ParseUint(path[i+1:i+4], 8, 8); err == nil {
				out.WriteByte(byte(value))
				i += 3
				continue
			}
		}
		out.WriteByte(path[i])
	}
	return out.String()
}
