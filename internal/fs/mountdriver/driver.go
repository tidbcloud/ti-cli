package mountdriver

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

type Driver interface {
	Name() string
	CheckPrerequisites() error
	Mount(ctx context.Context, serverURL, mountPath string) error
	Unmount(ctx context.Context, mountPath string) error
}

func Resolve(name string) (Driver, error) {
	return ResolveWithDeps(name, runtime.GOOS, exec.LookPath, os.Stat, exec.CommandContext)
}

func ResolveWithDeps(name, goos string, lookPath func(string) (string, error), stat func(string) (os.FileInfo, error), commandContext func(context.Context, string, ...string) *exec.Cmd) (Driver, error) {
	if goos == "" {
		goos = runtime.GOOS
	}
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if stat == nil {
		stat = os.Stat
	}
	if commandContext == nil {
		commandContext = exec.CommandContext
	}
	fuse := FUSE{GOOS: goos, LookPath: lookPath, Stat: stat}
	webdav := WebDAV{GOOS: goos, LookPath: lookPath, CommandContext: commandContext}
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

func (d FUSE) Mount(ctx context.Context, serverURL, mountPath string) error {
	return fmt.Errorf("ti fs FUSE mounts are handled by the in-process FUSE runtime")
}

func (d FUSE) Unmount(ctx context.Context, mountPath string) error {
	return fmt.Errorf("ti fs FUSE unmount is handled by the running FUSE server")
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
	GOOS           string
	LookPath       func(string) (string, error)
	CommandContext func(context.Context, string, ...string) *exec.Cmd
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

func (d WebDAV) Mount(ctx context.Context, serverURL, mountPath string) error {
	if err := d.CheckPrerequisites(); err != nil {
		return err
	}
	cmd := d.command(ctx, "mount_webdav", serverURL, mountPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("mount_webdav failed for %q: %w: %s", mountPath, err, string(output))
	}
	return nil
}

func (d WebDAV) Unmount(ctx context.Context, mountPath string) error {
	goos := d.goos()
	switch goos {
	case "darwin":
		if _, err := d.lookPath()("umount"); err != nil {
			return fmt.Errorf("missing umount: ti fs cannot detach WebDAV mounts on this system")
		}
		cmd := d.command(ctx, "umount", mountPath)
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("umount failed for %q: %w: %s", mountPath, err, string(output))
		}
		return nil
	default:
		return d.CheckPrerequisites()
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

func (d WebDAV) command(ctx context.Context, name string, args ...string) *exec.Cmd {
	if d.CommandContext != nil {
		return d.CommandContext(ctx, name, args...)
	}
	return exec.CommandContext(ctx, name, args...)
}
