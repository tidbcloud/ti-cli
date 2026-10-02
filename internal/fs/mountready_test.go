package fs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/fs/mountlocator"
)

func TestProbeMountPointReady(t *testing.T) {
	t.Run("directory with entries is ready", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "entry.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := probeMountPointReady(dir); err != nil {
			t.Fatalf("expected ready, got %v", err)
		}
	})
	t.Run("empty directory is ready", func(t *testing.T) {
		if err := probeMountPointReady(t.TempDir()); err != nil {
			t.Fatalf("expected ready, got %v", err)
		}
	})
	t.Run("missing path is not ready", func(t *testing.T) {
		if err := probeMountPointReady(filepath.Join(t.TempDir(), "missing")); err == nil {
			t.Fatal("expected error for missing path")
		}
	})
	t.Run("file path is not a mount point", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "not-a-dir")
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := probeMountPointReady(path); err == nil {
			t.Fatal("expected error for file path")
		}
	})
}

func TestWaitForMountReadySucceedsOnceReadable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mp")
	go func() {
		time.Sleep(30 * time.Millisecond)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Error(err)
		}
	}()
	service := Service{MountReadyPollInterval: 5 * time.Millisecond}
	if err := service.waitForMountReady(context.Background(), path, 2*time.Second, ""); err != nil {
		t.Fatalf("expected ready, got %v", err)
	}
}

func TestWaitForMountReadyTimeout(t *testing.T) {
	service := Service{MountReadyPollInterval: 5 * time.Millisecond}
	path := filepath.Join(t.TempDir(), "missing")
	err := service.waitForMountReady(context.Background(), path, 50*time.Millisecond, "; to stop it run: ti fs unmount-file-system --mount-path x")
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if apperr.CodeFor(err) != "fs.mount_ready_timeout" {
		t.Fatalf("unexpected error code %q: %v", apperr.CodeFor(err), err)
	}
	if !strings.Contains(err.Error(), "ti fs unmount-file-system --mount-path x") {
		t.Fatalf("error lacks stop hint: %v", err)
	}
}

func TestWaitForMountReadyCanceled(t *testing.T) {
	service := Service{MountReadyPollInterval: 5 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	err := service.waitForMountReady(ctx, filepath.Join(t.TempDir(), "missing"), 2*time.Second, "")
	if apperr.CodeFor(err) != "fs.mount_ready_canceled" {
		t.Fatalf("unexpected error code %q: %v", apperr.CodeFor(err), err)
	}
}

func TestDrive9MountWaitsUntilMountPointIsReadable(t *testing.T) {
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(home, companion)
	service.MountReadyPollInterval = 5 * time.Millisecond
	mountPath := filepath.Join(t.TempDir(), "workspace")

	result, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile:        dataProfile(),
		FileSystemName: "workspace",
		MountPath:      mountPath,
		RemotePath:     "/",
		ReadyTimeout:   2 * time.Second,
	})
	if err != nil {
		t.Fatalf("MountFileSystem failed: %v", err)
	}
	if result.Status != "mounted" {
		t.Fatalf("unexpected status %q", result.Status)
	}
	if info, err := os.Stat(mountPath); err != nil || !info.IsDir() {
		t.Fatalf("mount path was not made readable: %v", err)
	}
}

func TestDrive9MountTimeoutPreservesMountLocator(t *testing.T) {
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(home, companion)
	service.MountReadyPollInterval = 5 * time.Millisecond
	// A file at the mount path never becomes a readable mount root, so the
	// readiness wait must time out while keeping the mount and its locator.
	mountPath := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(mountPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile:        dataProfile(),
		FileSystemName: "workspace",
		MountPath:      mountPath,
		RemotePath:     "/",
		ReadyTimeout:   80 * time.Millisecond,
	})
	if apperr.CodeFor(err) != "fs.mount_ready_timeout" {
		t.Fatalf("unexpected error code %q: %v", apperr.CodeFor(err), err)
	}
	if !strings.Contains(err.Error(), "ti fs unmount-file-system --mount-path") {
		t.Fatalf("error lacks unmount guidance: %v", err)
	}
	if _, _, locErr := mountlocator.Read(home, mountPath); locErr != nil {
		t.Fatalf("mount locator must survive a readiness timeout: %v", locErr)
	}
	requireFakeDrive9Call(t, recordPath, "mount")
}

func TestDrive9VaultMountTimeoutPreservesMountLocator(t *testing.T) {
	home := t.TempDir()
	companion, _ := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", filepath.Join(t.TempDir(), "unused.jsonl"))
	service := testCompanionService(home, companion)
	service.MountReadyPollInterval = 5 * time.Millisecond
	mountPath := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(mountPath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := service.MountVault(context.Background(), VaultMountOptions{
		Profile:      dataProfile(),
		MountPath:    mountPath,
		VaultToken:   "vault-token",
		ReadyTimeout: 80 * time.Millisecond,
	})
	if apperr.CodeFor(err) != "fs.mount_ready_timeout" {
		t.Fatalf("unexpected error code %q: %v", apperr.CodeFor(err), err)
	}
	if !strings.Contains(err.Error(), "ti fs-vault unmount-vault --mount-path") {
		t.Fatalf("error lacks unmount guidance: %v", err)
	}
	if _, _, locErr := mountlocator.Read(home, mountPath); locErr != nil {
		t.Fatalf("mount locator must survive a readiness timeout: %v", locErr)
	}
}
