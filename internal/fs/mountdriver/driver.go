package mountdriver

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

// Driver describes the mount driver named by --driver. Mounting itself belongs
// to the ti-drive9 companion, so a driver only names itself and reports whether
// the host could support it; ti never starts a local mount helper itself.
type Driver interface {
	Name() string
	CheckPrerequisites() error
}

func Resolve(name string) (Driver, error) {
	return resolve(name, runtime.GOOS, exec.LookPath, os.Stat)
}

func resolve(name, goos string, lookPath func(string) (string, error), stat func(string) (os.FileInfo, error)) (Driver, error) {
	if goos == "" {
		goos = runtime.GOOS
	}
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if stat == nil {
		stat = os.Stat
	}
	fuse := FUSE{GOOS: goos, LookPath: lookPath, Stat: stat}
	webdav := WebDAV{GOOS: goos, LookPath: lookPath}
	switch name {
	case "", "auto":
		if err := fuse.CheckPrerequisites(); err == nil {
			return fuse, nil
		}
		if err := webdav.CheckPrerequisites(); err == nil {
			return webdav, nil
		}
		return fuse, nil
	case "fuse":
		return fuse, nil
	case "webdav":
		return webdav, nil
	default:
		return nil, fmt.Errorf("unsupported ti fs mount driver %q; supported values: auto, fuse, webdav", name)
	}
}

type FUSE struct {
	GOOS     string
	LookPath func(string) (string, error)
	Stat     func(string) (os.FileInfo, error)
}

func (d FUSE) Name() string {
	return "fuse"
}

func (d FUSE) CheckPrerequisites() error {
	goos := d.goos()
	lookPath := d.lookPath()
	stat := d.stat()
	switch goos {
	case "darwin":
		for _, path := range []string{
			"/Library/Filesystems/macfuse.fs/Contents/Resources/mount_macfuse",
			"/Library/Filesystems/osxfuse.fs/Contents/Resources/mount_osxfuse",
		} {
			if _, err := stat(path); err == nil {
				return nil
			}
		}
		return fmt.Errorf("missing macFUSE mount helper: install macFUSE and approve its system extension, or explicitly use --driver webdav")
	case "linux":
		if _, err := stat("/dev/fuse"); err != nil {
			return fmt.Errorf("missing /dev/fuse: install or enable FUSE, or explicitly use --driver webdav where supported")
		}
		for _, name := range []string{"fusermount3", "fusermount"} {
			if _, err := lookPath(name); err == nil {
				return nil
			}
		}
		for _, path := range []string{"/bin/fusermount3", "/bin/fusermount"} {
			if _, err := stat(path); err == nil {
				return nil
			}
		}
		return fmt.Errorf("missing fusermount3 or fusermount: install FUSE userspace tools, or explicitly use --driver webdav where supported")
	case "windows":
		return fmt.Errorf("ti fs FUSE mount is not supported on Windows; explicitly use --driver webdav if a WebDAV mount is available")
	default:
		return fmt.Errorf("ti fs FUSE mount is not supported on %s", goos)
	}
}

func (d FUSE) goos() string {
	if d.GOOS != "" {
		return d.GOOS
	}
	return runtime.GOOS
}

func (d FUSE) lookPath() func(string) (string, error) {
	if d.LookPath != nil {
		return d.LookPath
	}
	return exec.LookPath
}

func (d FUSE) stat() func(string) (os.FileInfo, error) {
	if d.Stat != nil {
		return d.Stat
	}
	return os.Stat
}

type WebDAV struct {
	GOOS     string
	LookPath func(string) (string, error)
}

func (d WebDAV) Name() string {
	return "webdav"
}

func (d WebDAV) CheckPrerequisites() error {
	goos := d.goos()
	lookPath := d.lookPath()
	switch goos {
	case "darwin":
		if _, err := lookPath("mount_webdav"); err != nil {
			return fmt.Errorf("missing mount_webdav: install or enable the macOS WebDAV filesystem helper")
		}
		if _, err := lookPath("umount"); err != nil {
			return fmt.Errorf("missing umount: ti fs cannot detach WebDAV mounts on this system")
		}
		return nil
	case "linux":
		return fmt.Errorf("ti fs WebDAV mount is not supported on Linux yet; use ti fs data-plane commands or run mount on macOS")
	case "windows":
		return fmt.Errorf("ti fs WebDAV mount is not supported on Windows yet; use ti fs data-plane commands or run mount on macOS")
	default:
		return fmt.Errorf("ti fs mount is not supported on %s", goos)
	}
}

func (d WebDAV) goos() string {
	if d.GOOS != "" {
		return d.GOOS
	}
	return runtime.GOOS
}

func (d WebDAV) lookPath() func(string) (string, error) {
	if d.LookPath != nil {
		return d.LookPath
	}
	return exec.LookPath
}
