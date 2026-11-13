package fs

import (
	"errors"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/tidbcloud/ti-cli/internal/api"
	apifs "github.com/tidbcloud/ti-cli/internal/api/fs"
)

// Helpers shared by the ti fs data-plane code. They live here rather than in a
// WebDAV bridge: ti no longer exposes a local WebDAV or FUSE mount, so mount
// semantics belong to the ti-drive9 companion and these are only small
// conversions between Drive9 metadata and os.FileInfo.

func treePrefix(name string) string {
	if strings.HasSuffix(name, "/") {
		return name
	}
	return name + "/"
}

type remoteFileInfo struct {
	name    string
	size    int64
	mode    os.FileMode
	modTime time.Time
}

func (i remoteFileInfo) Name() string {
	if i.name == "" || i.name == "." || i.name == "/" {
		return "/"
	}
	return path.Base(i.name)
}

func (i remoteFileInfo) Size() int64 {
	return i.size
}

func (i remoteFileInfo) Mode() os.FileMode {
	return i.mode
}

func (i remoteFileInfo) ModTime() time.Time {
	return i.modTime
}

func (i remoteFileInfo) IsDir() bool {
	return i.mode.IsDir()
}

func (i remoteFileInfo) Sys() any {
	return nil
}

func fileInfoFromMetadata(remotePath string, metadata apifs.StatMetadataResponse) os.FileInfo {
	mode := os.FileMode(0o644)
	if metadata.IsDir {
		mode = os.ModeDir | 0o755
	}
	return remoteFileInfo{name: path.Base(remotePath), size: metadata.Size, mode: mode, modTime: unixModTime(metadata.Mtime)}
}

func fileInfoFromStat(remotePath string, stat apifs.StatResponse) os.FileInfo {
	mode := os.FileMode(0o644)
	if stat.HasMode {
		mode = os.FileMode(stat.Mode)
	}
	if stat.IsDir {
		mode |= os.ModeDir
		if stat.Mode == 0 {
			mode |= 0o755
		}
	}
	return remoteFileInfo{name: path.Base(remotePath), size: stat.SizeBytes, mode: mode, modTime: unixModTime(stat.Mtime)}
}

func unixModTime(value int64) time.Time {
	if value <= 0 {
		return time.Now()
	}
	return time.Unix(value, 0)
}

func mapWebDAVError(err error) error {
	if err == nil {
		return nil
	}
	if isAPINotFound(err) {
		return os.ErrNotExist
	}
	var apiErr *api.Error
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case http.StatusForbidden, http.StatusUnauthorized:
			return os.ErrPermission
		case http.StatusConflict:
			return os.ErrExist
		}
	}
	return err
}

func isAPINotFound(err error) bool {
	var apiErr *api.Error
	return errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound
}
