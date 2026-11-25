//go:build !windows

package fs

import (
	"context"
	"errors"
	"syscall"
	"testing"
	"time"

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
