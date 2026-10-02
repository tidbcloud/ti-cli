package fs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/tidbcloud/ti-cli/internal/apperr"
)

const (
	defaultMountReadyTimeout      = 30 * time.Second
	defaultMountReadyPollInterval = 100 * time.Millisecond
	mountReadyProbeTimeout        = 2 * time.Second
)

// probeMountPointReady reports whether mountPath is a directory that the
// kernel can list through the mounted filesystem. A background mount is only
// usable once stat and readdir are served from the mount root, which can lag
// the mount process exit while the companion runtime warms up.
func probeMountPointReady(mountPath string) error {
	info, err := os.Stat(mountPath)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("mount path %q is not a directory", mountPath)
	}
	dir, err := os.Open(mountPath)
	if err != nil {
		return err
	}
	defer dir.Close()
	if _, err := dir.Readdirnames(1); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// probeMountPointOnce bounds one readiness probe. A wedged mount can block
// readdir indefinitely, so slow probes are abandoned and treated as not
// ready; the abandoned goroutine finishes and closes its handle once the
// syscall returns.
func probeMountPointOnce(mountPath string) error {
	done := make(chan error, 1)
	go func() {
		done <- probeMountPointReady(mountPath)
	}()
	select {
	case err := <-done:
		return err
	case <-time.After(mountReadyProbeTimeout):
		return fmt.Errorf("mount readiness probe did not complete within %s", mountReadyProbeTimeout)
	}
}

func (s Service) waitForMountReady(ctx context.Context, mountPath string, timeout time.Duration, stopHint string) error {
	if timeout <= 0 {
		timeout = defaultMountReadyTimeout
	}
	interval := s.MountReadyPollInterval
	if interval <= 0 {
		interval = defaultMountReadyPollInterval
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if err := probeMountPointOnce(mountPath); err != nil {
			lastErr = err
		} else {
			return nil
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		wait := interval
		if remaining < wait {
			wait = remaining
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return mountReadyCanceled(mountPath, stopHint, ctx.Err())
		case <-timer.C:
		}
	}
	return apperr.Wrap(
		"fs.mount_ready_timeout",
		"runtime",
		1,
		fmt.Sprintf("background mount at %q did not become readable within %s; the mount is still running%s", mountPath, timeout, stopHint),
		lastErr,
	)
}

func mountReadyCanceled(mountPath, stopHint string, cause error) error {
	return apperr.Wrap(
		"fs.mount_ready_canceled",
		"runtime",
		1,
		fmt.Sprintf("waiting for the background mount at %q to become readable was canceled; the mount is still running%s", mountPath, stopHint),
		cause,
	)
}
