//go:build !windows

package fs

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/interrupt"
)

// The signal that cancelled the copy is already queued on the command's channel,
// so a cleanup that reads that channel never runs. A fresh registration must only
// react to a signal delivered after the cleanup started.
func TestDrive9CopyFailureCleansUpAfterTheSignalThatFailedTheCopy(t *testing.T) {
	service, recordPath, state := newStatefulCopyService(t, "/dst.txt", "/dst.txt.ti-tmp-fixed")
	ctx, stop := interrupt.NotifyContext(context.Background(), syscall.SIGINT)
	defer stop()
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("send SIGINT: %v", err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the interrupt context was not cancelled by the signal")
	}

	err := service.drive9CopyFailure(ctx, CopyFileOptions{Profile: dataProfile()}, "/src/a.txt", "/dst.txt", "/dst.txt.ti-tmp-fixed", drive9CopyStageAfterMove, errors.New("copy interrupted"))
	if err == nil || err.Error() != "copy interrupted" {
		t.Fatalf("cleanup error = %v, want the original interruption", err)
	}
	var sawStat, sawRemove bool
	for _, args := range copyCalls(t, recordPath) {
		switch args[1] {
		case "stat":
			sawStat = true
		case "rm":
			sawRemove = true
		}
	}
	if !sawStat || !sawRemove {
		t.Fatalf("calls = %v, want a stat and a removal after the interrupting signal", copyCalls(t, recordPath))
	}
	if paths := fakeDrive9Paths(t, state); paths["/dst.txt.ti-tmp-fixed"] {
		t.Fatalf("paths = %v, want the staging file removed", paths)
	}
}

// A signal that arrives while the cleanup is already running stops it, and the
// error says the staging path was not checked.
func TestDrive9CopyFailureSecondSignalStopsTheCleanup(t *testing.T) {
	service, _, _ := newStatefulCopyService(t, "/dst.txt", "/dst.txt.ti-tmp-fixed")
	t.Setenv("TI_FAKE_DRIVE9_STAT_HANG", "ti-tmp")
	t.Setenv("TI_FAKE_DRIVE9_STAT_HANG_MS", "5000")
	// Keep the process alive when the signal is delivered.
	guard := make(chan os.Signal, 1)
	signal.Notify(guard, syscall.SIGINT)
	defer signal.Stop(guard)

	done := make(chan error, 1)
	go func() {
		done <- service.drive9CopyFailure(context.Background(), CopyFileOptions{Profile: dataProfile()}, "/src/a.txt", "/dst.txt", "/dst.txt.ti-tmp-fixed", drive9CopyStageAfterMove, errors.New("copy interrupted"))
	}()
	time.Sleep(300 * time.Millisecond)
	if err := syscall.Kill(syscall.Getpid(), syscall.SIGINT); err != nil {
		t.Fatalf("send SIGINT: %v", err)
	}
	select {
	case err := <-done:
		if apperr.CodeFor(err) != "fs.copy_staging_unchecked" {
			t.Fatalf("cleanup error = %v, want fs.copy_staging_unchecked", err)
		}
		if !strings.Contains(apperr.MessageFor(err), "interrupted while checking the staging path") {
			t.Fatalf("error %q does not say the check was interrupted", apperr.MessageFor(err))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the cleanup kept waiting after a second signal")
	}
}
