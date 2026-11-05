//go:build !windows

package fswrap

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"testing"
	"time"

	"github.com/tidbcloud/ti-cli/internal/apperr"
)

func TestStreamingContextCancellation(t *testing.T) {
	if os.Getenv("TI_TEST_STREAMING_CHILD") == "1" {
		signals := make(chan os.Signal, 1)
		signal.Notify(signals, os.Interrupt)
		fmt.Println("ready")
		select {
		case <-signals:
			os.Exit(130)
		case <-time.After(20 * time.Second):
			os.Exit(99)
		}
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TI_TEST_STREAMING_CHILD", "1")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := &readyWriter{ready: make(chan struct{})}
	runner := Runner{HomeDir: t.TempDir(), CompanionPath: executable, Stdout: ready}
	done := make(chan error, 1)
	go func() {
		done <- runner.RunStreaming(ctx, RunOptions{ResourceName: "test", Args: []string{"-test.run=^TestStreamingContextCancellation$"}})
	}()
	select {
	case <-ready.ready:
	case err := <-done:
		t.Fatalf("child exited before ready: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("child did not become ready")
	}
	cancel()
	select {
	case err := <-done:
		if apperr.ExitCodeFor(err) != 130 || !apperr.IsSilent(err) {
			t.Fatalf("cancel=%v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("context cancellation did not reach companion")
	}
}

type readyWriter struct {
	ready chan struct{}
	once  sync.Once
}

func (w *readyWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.ready) })
	return len(p), nil
}
