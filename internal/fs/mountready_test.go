package fs

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/fs/mountlocator"
	"golang.org/x/net/webdav"
)

func trueMountEvidence(string) (bool, error) { return true, nil }

func blockingMountEvidence(d time.Duration) func(string) (bool, error) {
	return func(string) (bool, error) {
		time.Sleep(d)
		return true, nil
	}
}

func TestProbeMountPointReady(t *testing.T) {
	t.Run("plain directory is not ready without mount evidence", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "entry.txt"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		err := probeMountPointReady(dir, defaultMountPointActive)
		if err == nil || !strings.Contains(err.Error(), "not an active mount") {
			t.Fatalf("plain directory must not be ready, got %v", err)
		}
	})
	t.Run("empty directory is not ready without mount evidence", func(t *testing.T) {
		err := probeMountPointReady(t.TempDir(), defaultMountPointActive)
		if err == nil || !strings.Contains(err.Error(), "not an active mount") {
			t.Fatalf("empty plain directory must not be ready, got %v", err)
		}
	})
	t.Run("missing path is not ready", func(t *testing.T) {
		if err := probeMountPointReady(filepath.Join(t.TempDir(), "missing"), trueMountEvidence); err == nil {
			t.Fatal("expected error for missing path")
		}
	})
	t.Run("file path is not a mount point", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "not-a-dir")
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := probeMountPointReady(path, trueMountEvidence); err == nil {
			t.Fatal("expected error for file path")
		}
	})
	t.Run("readability with injected evidence", func(t *testing.T) {
		dir := t.TempDir()
		if err := probeMountPointReady(dir, trueMountEvidence); err != nil {
			t.Fatalf("expected ready with evidence, got %v", err)
		}
	})
}

func TestDefaultMountPointActiveRejectsPlainDirectory(t *testing.T) {
	active, err := defaultMountPointActive(t.TempDir())
	if err != nil {
		t.Fatalf("detection failed: %v", err)
	}
	if active {
		t.Fatal("a plain directory must not count as an active mount")
	}
}

// TestDefaultMountPointActiveAcceptsRealWebDAVMount proves the evidence
// predicate passes only after a real kernel mount exists. It failed against
// the pre-regression behavior where any readable directory counted as ready.
func TestDefaultMountPointActiveAcceptsRealWebDAVMount(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("real-mount evidence test uses macOS mount_webdav")
	}
	if _, err := exec.LookPath("mount_webdav"); err != nil {
		t.Skip("mount_webdav is not available")
	}
	// The served tree must not contain the mountpoint: listing a share that
	// contains its own mount root recurses kernel WebDAV into itself and
	// deadlocks the readdir.
	serveRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(serveRoot, "seed.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	mountParent := t.TempDir()
	mp := filepath.Join(mountParent, "mp")
	if err := os.MkdirAll(mp, 0o755); err != nil {
		t.Fatal(err)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("cannot listen for WebDAV server: %v", err)
	}
	server := &http.Server{Handler: &webdav.Handler{
		FileSystem: webdav.Dir(serveRoot),
		LockSystem: webdav.NewMemLS(),
	}}
	go func() { _ = server.Serve(listener) }()
	defer func() { _ = server.Close() }()

	serverURL := fmt.Sprintf("http://127.0.0.1:%d/", listener.Addr().(*net.TCPAddr).Port)
	mountCmd := exec.Command("mount_webdav", serverURL, mp)
	if out, err := mountCmd.CombinedOutput(); err != nil {
		_ = exec.Command("umount", mp).Run()
		t.Skipf("mount_webdav failed in this environment (%v): %s", err, out)
	}
	defer func() { _ = exec.Command("umount", mp).Run() }()

	if active, err := defaultMountPointActive(mp); err != nil || !active {
		t.Fatalf("real WebDAV mount must count as active: active=%v err=%v", active, err)
	}
	if err := probeMountPointOnce(context.Background(), mp, 10*time.Second, defaultMountPointActive); err != nil {
		t.Fatalf("real WebDAV mount must be ready: %v", err)
	}
	if _, err := os.ReadDir(mp); err != nil {
		t.Fatalf("real WebDAV mount must list entries: %v", err)
	}
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
	service := Service{
		MountReadyPollInterval: 5 * time.Millisecond,
		mountPointActive:       trueMountEvidence,
	}
	if err := service.waitForMountReady(context.Background(), path, 2*time.Second, ""); err != nil {
		t.Fatalf("expected ready, got %v", err)
	}
}

func TestWaitForMountReadyTimeout(t *testing.T) {
	service := Service{MountReadyPollInterval: 5 * time.Millisecond, mountPointActive: trueMountEvidence}
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
	service := Service{MountReadyPollInterval: 5 * time.Millisecond, mountPointActive: trueMountEvidence}
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

// TestWaitForMountReadyBoundsBlockedProbeByTimeout pins the review blocker:
// a blocked probe must be abandoned at the --ready-timeout budget, not at the
// full per-probe bound. Against the previous implementation (10s fixed probe
// budget) this test failed because the wait took the full probe bound.
func TestWaitForMountReadyBoundsBlockedProbeByTimeout(t *testing.T) {
	service := Service{
		MountReadyPollInterval: 5 * time.Millisecond,
		mountPointActive:       blockingMountEvidence(3 * time.Second),
	}
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	err := service.waitForMountReady(context.Background(), dir, 200*time.Millisecond, "")
	elapsed := time.Since(start)
	if apperr.CodeFor(err) != "fs.mount_ready_timeout" {
		t.Fatalf("unexpected error code %q: %v", apperr.CodeFor(err), err)
	}
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("short --ready-timeout must bound a blocked probe: took %s", elapsed)
	}
}

// TestWaitForMountReadyCancelsBlockedProbe pins the second half of the review
// blocker: Ctrl-C during a blocked probe must return promptly instead of
// waiting out the full probe budget.
func TestWaitForMountReadyCancelsBlockedProbe(t *testing.T) {
	service := Service{
		MountReadyPollInterval: 5 * time.Millisecond,
		mountPointActive:       blockingMountEvidence(5 * time.Second),
	}
	dir := t.TempDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	err := service.waitForMountReady(ctx, dir, 30*time.Second, "")
	elapsed := time.Since(start)
	if apperr.CodeFor(err) != "fs.mount_ready_canceled" {
		t.Fatalf("unexpected error code %q: %v", apperr.CodeFor(err), err)
	}
	if elapsed > 1500*time.Millisecond {
		t.Fatalf("cancellation during a blocked probe must be prompt: took %s", elapsed)
	}
}

func TestDrive9MountWaitsUntilMountPointIsReadable(t *testing.T) {
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(home, companion)
	service.MountReadyPollInterval = 5 * time.Millisecond
	service.mountPointActive = trueMountEvidence
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

// TestDrive9MountRequiresActiveMountEvidence is the companion-exits-0-but-
// nothing-mounted regression: the fake companion creates only a plain
// directory, which must not satisfy readiness even though it is readable.
// The previous implementation reported status "mounted" here.
func TestDrive9MountRequiresActiveMountEvidence(t *testing.T) {
	home := t.TempDir()
	companion, _ := buildFakeDrive9(t)
	service := testCompanionService(home, companion)
	service.MountReadyPollInterval = 5 * time.Millisecond
	mountPath := filepath.Join(t.TempDir(), "workspace")

	_, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile:        dataProfile(),
		FileSystemName: "workspace",
		MountPath:      mountPath,
		RemotePath:     "/",
		ReadyTimeout:   80 * time.Millisecond,
	})
	if apperr.CodeFor(err) != "fs.mount_ready_timeout" {
		t.Fatalf("plain directory must not satisfy mount readiness, got %v", err)
	}
	if !strings.Contains(err.Error(), "not an active mount") && !strings.Contains(err.Error(), "did not become readable") {
		t.Fatalf("unexpected error detail: %v", err)
	}
	if _, _, locErr := mountlocator.Read(home, mountPath); locErr != nil {
		t.Fatalf("mount locator must survive a readiness timeout: %v", locErr)
	}
}

func TestDrive9MountTimeoutPreservesMountLocator(t *testing.T) {
	home := t.TempDir()
	companion, _ := buildFakeDrive9(t)
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
