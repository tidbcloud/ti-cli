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
	// mountReadyProbeTimeout bounds a single probe so a wedged mount cannot
	// block one probe forever. It is an upper bound only: every probe is also
	// capped by the remaining --ready-timeout budget and the command context,
	// so a short --ready-timeout never waits a full probe bound. It must stay
	// well above a healthy cold first readdir: a WebDAV mount's initial
	// directory listing crosses the companion proxy and the remote region and
	// can legitimately take seconds.
	mountReadyProbeTimeout = 10 * time.Second
)

// errMountReadyProbeExhausted marks a probe whose budget elapsed before the
// probe answered. It means "not ready yet", not a mount failure.
var errMountReadyProbeExhausted = errors.New("mount readiness probe budget exhausted")

// probeMountPointReady reports whether mountPath is an active mount that the
// kernel can list. A background mount is only usable once the mount exists,
// is visible in the mount table, and readdir is served from the mount root.
func probeMountPointReady(mountPath string, mounted func(string) (bool, error)) error {
	info, err := os.Stat(mountPath)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("mount path %q is not a directory", mountPath)
	}
	active, err := mounted(mountPath)
	if err != nil {
		return fmt.Errorf("mount evidence for %q: %w", mountPath, err)
	}
	if !active {
		return fmt.Errorf("mount path %q is not an active mount", mountPath)
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

// probeMountPointOnce runs one probe bounded by budget and ctx. A blocked or
// wedged probe is abandoned when either expires; the abandoned goroutine
// finishes and closes its handle once the underlying syscall returns.
func probeMountPointOnce(ctx context.Context, mountPath string, budget time.Duration, mounted func(string) (bool, error)) error {
	done := make(chan error, 1)
	go func() {
		done <- probeMountPointReady(mountPath, mounted)
	}()
	timer := time.NewTimer(budget)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errMountReadyProbeExhausted
	}
}

func (s Service) mountEvidence() func(string) (bool, error) {
	if s.mountPointActive != nil {
		return s.mountPointActive
	}
	return defaultMountPointActive
}

func (s Service) waitForMountReady(ctx context.Context, mountPath string, timeout time.Duration, stopHint string) error {
	if timeout <= 0 {
		timeout = defaultMountReadyTimeout
	}
	interval := s.MountReadyPollInterval
	if interval <= 0 {
		interval = defaultMountReadyPollInterval
	}
	mounted := s.mountEvidence()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		budget := mountReadyProbeTimeout
		if remaining < budget {
			budget = remaining
		}
		err := probeMountPointOnce(ctx, mountPath, budget, mounted)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
			return mountReadyCanceled(mountPath, stopHint, err)
		default:
			lastErr = err
		}
		remaining = time.Until(deadline)
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
