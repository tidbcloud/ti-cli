package fs

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/tidbcloud/ti-cli/internal/apperr"
)

// This file owns --pack-path: one normalizer, the mount-time refusals, and the
// mapping onto the companion's archive and overlay paths.

// normalizeMountPackPaths trims and de-duplicates --pack-path once, where the
// companion argv is decided, and refuses the shapes that cannot survive to the
// automatic pack: a WebDAV mount has no local overlay to pack, and the
// companion's joinArchivePath rejects a backslash or a relative path with an
// empty, "." or ".." segment.
func normalizeMountPackPaths(opts MountFileSystemOptions) (MountFileSystemOptions, error) {
	opts.PackPaths = mergeMountPackPaths(opts.PackPaths)
	if len(opts.PackPaths) == 0 {
		return opts, nil
	}
	if companionMountMode(opts.Driver) == "webdav" {
		return MountFileSystemOptions{}, apperr.New("fs.pack_path_requires_fuse", "usage", 2,
			"--pack-path needs a FUSE mount: the automatic pack reads the local overlay, and a WebDAV mount has no local overlay; remove --pack-path or use --driver fuse where FUSE is available")
	}
	for _, raw := range opts.PackPaths {
		if strings.Contains(raw, "\\") {
			return MountFileSystemOptions{}, apperr.New("fs.invalid_pack_path", "usage", 2,
				fmt.Sprintf("--pack-path %q contains a backslash; use forward slashes", raw))
		}
		if strings.HasPrefix(raw, "/") {
			// An absolute value is mapped onto an archive path by the pack, so
			// the companion's joinArchivePath still refuses "." and ".." there.
			// Empty segments are accepted: `/` and `/a//b` are valid pack paths
			// and the builtin profiles pack with `/`.
			for _, part := range strings.Split(raw, "/") {
				if part == "." || part == ".." {
					return MountFileSystemOptions{}, apperr.New("fs.invalid_pack_path", "usage", 2,
						fmt.Sprintf("--pack-path %q contains a %q segment; the automatic pack refuses it", raw, part))
				}
			}
			continue
		}
		for _, part := range strings.Split(raw, "/") {
			if part == "" || part == "." || part == ".." {
				return MountFileSystemOptions{}, apperr.New("fs.invalid_pack_path", "usage", 2,
					fmt.Sprintf("--pack-path %q is not a clean relative path; the automatic pack refuses empty, \".\" and \"..\" segments", raw))
			}
		}
	}
	return opts, nil
}

// mountPackArchivePath maps a --pack-path value onto the archive path the
// companion packs. It mirrors drive9's resolvePackPath with the empty local
// prefix the automatic pack uses: an absolute path under the mount's remote
// root is remote-relative, any other absolute path is already mount-local, and
// a relative path resolves under the mount root.
func mountPackArchivePath(remoteRoot, raw string) string {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "/") {
		return "/" + raw
	}
	root := strings.TrimSuffix(strings.TrimSpace(remoteRoot), "/")
	if root != "" && root != "/" {
		if raw == root {
			return "/"
		}
		if strings.HasPrefix(raw, root+"/") {
			return strings.TrimPrefix(raw, root)
		}
	}
	return raw
}

// mountPackOverlayPath returns where an archive path lives in the local
// overlay the companion packs: <local-root>/overlay/<archive path>.
func mountPackOverlayPath(localRoot, archivePath string) string {
	root := filepath.Join(localRoot, "overlay")
	rel := strings.TrimPrefix(archivePath, "/")
	if rel == "" {
		return root
	}
	return filepath.Join(root, filepath.FromSlash(rel))
}

func mergeMountPackPaths(groups ...[]string) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, group := range groups {
		for _, value := range group {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			if _, ok := seen[value]; ok {
				continue
			}
			seen[value] = struct{}{}
			out = append(out, value)
		}
	}
	return out
}
