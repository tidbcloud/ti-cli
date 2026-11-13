package fs

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/config"
	"github.com/tidbcloud/ti-cli/internal/fs/fscred"
	"github.com/tidbcloud/ti-cli/internal/fs/mountlocator"
)

func TestDrive9MountLocatorRoutesDrainAndUnmountWithoutCredentials(t *testing.T) {
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(home, companion)
	service.mountPointActive = trueMountEvidence
	mountPath := filepath.Join(t.TempDir(), "workspace")
	profile := dataProfile()

	if _, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile:        profile,
		FileSystemName: "workspace",
		MountPath:      mountPath,
		RemotePath:     "/",
	}); err != nil {
		t.Fatalf("MountFileSystem failed: %v", err)
	}
	locator, locatorPath, err := mountlocator.Read(home, mountPath)
	if err != nil {
		t.Fatalf("read mount locator: %v", err)
	}
	if locator.FileSystemName != "workspace" || locator.RegionCode != "aws-us-east-1" {
		t.Fatalf("unexpected locator: %#v", locator)
	}
	data, err := os.ReadFile(locatorPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), profile.FSAPIKey) {
		t.Fatalf("mount locator leaked FS token: %s", data)
	}
	mountCall := requireFakeDrive9Call(t, recordPath, "mount")
	for _, arg := range mountCall.Args {
		if arg == "--foreground" {
			t.Fatalf("ti exposed foreground mode to Drive9: %#v", mountCall.Args)
		}
	}

	localProfile := &config.Profile{Name: "default", HomeDir: home}
	if _, err := service.DrainFileSystem(context.Background(), DrainFileSystemOptions{
		Profile:   localProfile,
		MountPath: mountPath,
		Timeout:   time.Second,
	}); err != nil {
		t.Fatalf("DrainFileSystem failed: %v", err)
	}
	if _, err := service.UnmountFileSystem(context.Background(), UnmountFileSystemOptions{
		Profile:   localProfile,
		MountPath: mountPath,
		Timeout:   time.Second,
	}); err != nil {
		t.Fatalf("UnmountFileSystem failed: %v", err)
	}
	if _, _, err := mountlocator.Read(home, mountPath); !os.IsNotExist(err) {
		t.Fatalf("locator was not removed after unmount: %v", err)
	}

	for _, prefix := range [][]string{{"mount"}, {"mount", "drain"}, {"umount"}} {
		call := requireFakeDrive9Call(t, recordPath, prefix...)
		if call.Env["HOME"] != locator.CompanionHome {
			t.Fatalf("%v used HOME %q, want %q", prefix, call.Env["HOME"], locator.CompanionHome)
		}
	}
}

func TestDrive9VaultMountUsesBackgroundMode(t *testing.T) {
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(home, companion)
	service.mountPointActive = trueMountEvidence
	service.Stdout = &bytes.Buffer{}
	service.Stderr = &bytes.Buffer{}
	mountPath := filepath.Join(t.TempDir(), "vault")

	if _, err := service.MountVault(context.Background(), VaultMountOptions{
		Profile:    dataProfile(),
		MountPath:  mountPath,
		VaultToken: "vault-token",
	}); err != nil {
		t.Fatalf("MountVault failed: %v", err)
	}
	call := requireFakeDrive9Call(t, recordPath, "mount", "vault")
	for _, arg := range call.Args {
		if arg == "--foreground" {
			t.Fatalf("ti exposed foreground vault mode to Drive9: %#v", call.Args)
		}
	}
	locator, _, err := mountlocator.Read(home, mountPath)
	if err != nil {
		t.Fatalf("read vault mount locator: %v", err)
	}
	if locator.Kind != "vault" {
		t.Fatalf("vault mount locator kind = %q, want vault", locator.Kind)
	}
}

// TestDrive9MountAuditsUnpackedOverlaySymlinks covers the reported gap: mount-time
// auto/explicit unpack must run the same overlay symlink audit as
// unpack-file-system, remove the escaping link, and not leave a mount running.
func TestDrive9MountAuditsUnpackedOverlaySymlinks(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(t.TempDir(), companion)
	service.mountPointActive = trueMountEvidence
	mountPath := filepath.Join(t.TempDir(), "mnt")
	localRoot := filepath.Join(t.TempDir(), "local")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("victim"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TI_FAKE_MOUNT_ESCAPE_SYMLINK", "1")
	t.Setenv("TI_FAKE_ESCAPE_TARGET", outside)

	_, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile: dataProfile(), MountPath: mountPath, MountProfile: "portable", LocalRoot: localRoot,
	})
	if apperr.CodeFor(err) != "fs.unsafe_pack_symlink" {
		t.Fatalf("escaping overlay symlink error = %v", err)
	}
	if _, statErr := os.Lstat(filepath.Join(localRoot, "overlay", "evil")); !os.IsNotExist(statErr) {
		t.Fatalf("escaping symlink was not removed: %v", statErr)
	}
	requireFakeDrive9Call(t, recordPath, "umount")
	if data, readErr := os.ReadFile(outside); readErr != nil || string(data) != "victim" {
		t.Fatalf("audit changed the escape target: %q, %v", data, readErr)
	}
}

// TestDrive9MountDerivesOverlayRootForAudit proves the companion's own default
// overlay root is audited without being pinned, so an auto-unpack without an
// explicit --local-root is covered too.
func TestDrive9MountDerivesOverlayRootForAudit(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	home := t.TempDir()
	service := testCompanionService(home, companion)
	service.mountPointActive = trueMountEvidence
	mountPath := filepath.Join(t.TempDir(), "mnt")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("victim"), 0o644); err != nil {
		t.Fatal(err)
	}
	root, err := service.companionMountLocalRoot(dataProfile(), "/")
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("TI_FAKE_MOUNT_ESCAPE_SYMLINK", "1")
	t.Setenv("TI_FAKE_ESCAPE_TARGET", outside)
	t.Setenv("TI_FAKE_ESCAPE_ROOT", root)

	if _, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile: dataProfile(), MountPath: mountPath, MountProfile: "coding-agent", PackPaths: []string{"/"},
	}); apperr.CodeFor(err) != "fs.unsafe_pack_symlink" {
		t.Fatalf("derived overlay audit error = %v", err)
	}
	call := requireFakeDrive9Call(t, recordPath, "mount")
	if recordedFlagValue(call.Args, "--local-root") != "" {
		t.Fatalf("derived root must not be pinned to the companion: %#v", call.Args)
	}
	if _, statErr := os.Lstat(filepath.Join(root, "overlay", "evil")); !os.IsNotExist(statErr) {
		t.Fatalf("escaping symlink at the companion root was not removed: %v", statErr)
	}
}

// TestCompanionMountLocalRootMatchesDrive9 pins the replica of drive9's default
// root derivation against directories observed from the real companion binary
// (server https://drive9.example, credential sk-test): `/`, a nested root, the
// same root with a trailing slash, and a non-ASCII root. The cache root itself is
// covered separately by TestCompanionCacheRootMatchesUserCacheDir.
func TestCompanionMountLocalRootMatchesDrive9(t *testing.T) {
	t.Setenv("XDG_CACHE_HOME", "")
	home := filepath.Join(t.TempDir(), "companion-home")
	for _, tc := range []struct{ remote, dir string }{
		{"/", "root-26d2b529bbcb6733"},
		{"/workspace/repo", "repo-60c459977811d6a0"},
		{"/workspace/repo/", "repo-60c459977811d6a0"},
		{"/café", "caf-23458cc75cc9d02d"},
		{"/工作区/repo", "repo-993cccf97afe07b6"},
		// A mid-segment rune discriminates the per-rune label rule from a
		// byte-wise one (a-b vs a--b), which the trimmed vectors above cannot.
		{"/aéb", "a-b-c2e2dc4f153648d1"},
		{"/.hidden", "hidden-654418136e00c6da"},
		{"/x.y", "x.y-114815b006fbfd7c"},
	} {
		root, err := companionMountLocalRootAt(home, "https://drive9.example", tc.remote, "sk-test")
		if err != nil {
			t.Fatalf("%s: %v", tc.remote, err)
		}
		want := filepath.Join(companionCacheRoot(home), "drive9", "mounts", tc.dir)
		if root != want {
			t.Fatalf("%s: companion mount root = %q, want %q", tc.remote, root, want)
		}
	}
}

// TestCompanionCacheRootMatchesUserCacheDir pins the cache root against
// os.UserCacheDir semantics, which the companion inherits: XDG_CACHE_HOME applies
// only on the default Unix branch (not darwin or Windows), and a relative value is
// used verbatim so the audit fails loudly instead of checking a wrong directory.
func TestCompanionCacheRootMatchesUserCacheDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", "")
	if got, want := companionCacheRoot(home), filepath.Join(home, ".cache"); runtime.GOOS != "darwin" && runtime.GOOS != "windows" && got != want {
		t.Fatalf("default cache root = %q, want %q", got, want)
	}
	if runtime.GOOS == "darwin" {
		if got, want := companionCacheRoot(home), filepath.Join(home, "Library", "Caches"); got != want {
			t.Fatalf("darwin cache root = %q, want %q", got, want)
		}
	}

	xdg := filepath.Join(t.TempDir(), "xdg-cache")
	t.Setenv("XDG_CACHE_HOME", xdg)
	root, err := companionMountLocalRootAt(home, "https://drive9.example", "/", "sk-test")
	if err != nil {
		t.Fatal(err)
	}
	switch runtime.GOOS {
	case "darwin":
		if !strings.HasPrefix(root, filepath.Join(home, "Library", "Caches")) {
			t.Fatalf("darwin ignored Library/Caches in favour of XDG: %q", root)
		}
	case "windows":
		// Windows uses LOCALAPPDATA, not XDG_CACHE_HOME.
		if strings.HasPrefix(root, xdg) {
			t.Fatalf("windows honoured XDG_CACHE_HOME: %q", root)
		}
	default:
		if !strings.HasPrefix(root, xdg) {
			t.Fatalf("unix branch did not honour XDG_CACHE_HOME: %q", root)
		}
	}

	t.Setenv("XDG_CACHE_HOME", "relative-cache")
	_, err = companionMountLocalRootAt(home, "https://drive9.example", "/", "sk-test")
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		if err != nil {
			t.Fatalf("XDG_CACHE_HOME must be ignored on this platform: %v", err)
		}
	} else if apperr.CodeFor(err) != "fs.unpack_unknown_local_root" || !strings.Contains(apperr.MessageFor(err), "XDG_CACHE_HOME") {
		t.Fatalf("relative XDG_CACHE_HOME error = %v", err)
	}
}

// TestCompanionMountLocalRootRejectsMissingProfile keeps the security linchpin
// from silently succeeding without a profile.
func TestCompanionMountLocalRootRejectsMissingProfile(t *testing.T) {
	service := Service{HomeDir: t.TempDir()}
	if _, err := service.companionMountLocalRoot(nil, "/"); apperr.CodeFor(err) != "fs.missing_profile" {
		t.Fatalf("nil profile error = %v", err)
	}
}

// TestDrive9MountDryRunMatchesLiveUnpackEligibility keeps --dry-run and real
// execution on the same eligibility owner for the none-profile unpack rule.
func TestDrive9MountDryRunMatchesLiveUnpackEligibility(t *testing.T) {
	service := testCompanionService(t.TempDir(), filepath.Join(t.TempDir(), "ti-drive9"))
	opts := MountFileSystemOptions{
		Profile: dataProfile(), MountPath: filepath.Join(t.TempDir(), "mnt"),
		MountProfile: "none", UnpackArchivePath: "/workspace/backup.tgz",
	}
	if _, err := service.DryRunMountFileSystem(context.Background(), "ti fs mount-file-system", opts); apperr.CodeFor(err) != "fs.mount_unpack_requires_profile" {
		t.Fatalf("dry-run unpack eligibility error = %v", err)
	}
	if _, err := service.MountFileSystem(context.Background(), opts); apperr.CodeFor(err) != "fs.mount_unpack_requires_profile" {
		t.Fatalf("live unpack eligibility error = %v", err)
	}
}

// TestDrive9MountTrimsUnpackArchivePath keeps a whitespace-only flag from
// reaching the companion while ti treats it as absent.
func TestDrive9MountTrimsUnpackArchivePath(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(t.TempDir(), companion)
	service.mountPointActive = trueMountEvidence
	mountPath := filepath.Join(t.TempDir(), "mnt")
	if _, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile: dataProfile(), MountPath: mountPath, MountProfile: "coding-agent", UnpackArchivePath: "   ",
	}); err != nil {
		t.Fatalf("whitespace unpack mount failed: %v", err)
	}
	call := requireFakeDrive9Call(t, recordPath, "mount")
	if containsArg(call.Args, "--unpack") || containsArg(call.Args, "--local-root") {
		t.Fatalf("whitespace unpack path was forwarded: %#v", call.Args)
	}
}

// TestDrive9MountRejectsPackFlagsWithoutProfile keeps the live path aligned with
// --dry-run: mountOverlayPolicy is the single eligibility owner.
func TestDrive9MountRejectsPackFlagsWithoutProfile(t *testing.T) {
	companion, _ := buildFakeDrive9(t)
	service := testCompanionService(t.TempDir(), companion)
	for _, opts := range []MountFileSystemOptions{
		{Profile: dataProfile(), MountPath: filepath.Join(t.TempDir(), "mnt"), MountProfile: "none", PackPaths: []string{"/"}},
		{Profile: dataProfile(), MountPath: filepath.Join(t.TempDir(), "mnt"), MountProfile: "none", LocalRoot: filepath.Join(t.TempDir(), "local")},
	} {
		if _, err := service.MountFileSystem(context.Background(), opts); apperr.CodeFor(err) != "fs.invalid_mount_profile" {
			t.Fatalf("mount profile eligibility error = %v for %#v", err, opts)
		}
	}
}

// TestDrive9MountAuditsLinkInstalledDuringReadinessWait discriminates the audit
// placement: the fake companion installs the escaping link only after the
// foreground mount command returned, so an audit placed before the readiness wait
// would miss it and report a clean mount.
func TestDrive9MountAuditsLinkInstalledDuringReadinessWait(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(t.TempDir(), companion)
	mountPath := filepath.Join(t.TempDir(), "mnt")
	localRoot := filepath.Join(t.TempDir(), "local")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("victim"), 0o644); err != nil {
		t.Fatal(err)
	}
	evil := filepath.Join(localRoot, "overlay", "evil")
	service.mountPointActive = func(string) (bool, error) {
		_, statErr := os.Lstat(evil)
		return statErr == nil, nil
	}
	t.Setenv("TI_FAKE_ESCAPE_TARGET", outside)
	t.Setenv("TI_FAKE_MOUNT_ESCAPE_DELAY_MS", "200")

	_, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile: dataProfile(), MountPath: mountPath, MountProfile: "portable", LocalRoot: localRoot,
		ReadyTimeout: 5 * time.Second,
	})
	if apperr.CodeFor(err) != "fs.unsafe_pack_symlink" {
		t.Fatalf("link installed during readiness was not audited: %v", err)
	}
	if _, statErr := os.Lstat(evil); !os.IsNotExist(statErr) {
		t.Fatalf("escaping symlink was not removed: %v", statErr)
	}
	requireFakeDrive9Call(t, recordPath, "umount")
}

// TestDrive9MountStopsUnpackMountOnReadyTimeout covers the residual window: a
// clean audit at the timeout point does not prove unpack finished, so an
// unpack-capable mount is stopped instead of being left running.
func TestDrive9MountStopsUnpackMountOnReadyTimeout(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	home := t.TempDir()
	service := testCompanionService(home, companion)
	service.mountPointActive = func(string) (bool, error) { return false, nil }
	mountPath := filepath.Join(t.TempDir(), "mnt")
	localRoot := filepath.Join(t.TempDir(), "local")

	_, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile: dataProfile(), MountPath: mountPath, MountProfile: "portable", LocalRoot: localRoot,
		ReadyTimeout: 50 * time.Millisecond,
	})
	if apperr.CodeFor(err) != "fs.mount_ready_timeout" {
		t.Fatalf("unpack ready-timeout error = %v", err)
	}
	if !strings.Contains(apperr.MessageFor(err), "stopped") {
		t.Fatalf("timeout error does not explain the teardown: %v", err)
	}
	requireFakeDrive9Call(t, recordPath, "umount")
	if _, _, locatorErr := mountlocator.Read(home, mountPath); !os.IsNotExist(locatorErr) {
		t.Fatalf("locator left behind after the timeout teardown: %v", locatorErr)
	}
}

// TestDrive9MountAuditsWhenMountFailsAfterUnpack covers the companion-failure
// branch: a partial unpack can install the link even though mount reported an
// error, and the mount must still be torn down.
func TestDrive9MountAuditsWhenMountFailsAfterUnpack(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(t.TempDir(), companion)
	mountPath := filepath.Join(t.TempDir(), "mnt")
	localRoot := filepath.Join(t.TempDir(), "local")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("victim"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TI_FAKE_MOUNT_ESCAPE_SYMLINK", "1")
	t.Setenv("TI_FAKE_ESCAPE_TARGET", outside)
	t.Setenv("TI_FAKE_DRIVE9_MOUNT_FAIL_AFTER_UNPACK", "1")

	_, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile: dataProfile(), MountPath: mountPath, MountProfile: "portable", LocalRoot: localRoot,
	})
	if apperr.CodeFor(err) != "fs.mount_failed_unsafe_overlay" {
		t.Fatalf("mount-failure audit error = %v", err)
	}
	requireFakeDrive9Call(t, recordPath, "umount")
	if _, statErr := os.Lstat(filepath.Join(localRoot, "overlay", "evil")); !os.IsNotExist(statErr) {
		t.Fatalf("escaping symlink was not removed: %v", statErr)
	}
}

// TestDrive9MountStopsDaemonAfterFailedMountWithCleanAudit covers the case where
// the foreground client failed but the daemon may still be unpacking: the mount
// is stopped even when the audit is clean.
func TestDrive9MountStopsDaemonAfterFailedMountWithCleanAudit(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(t.TempDir(), companion)
	mountPath := filepath.Join(t.TempDir(), "mnt")
	localRoot := filepath.Join(t.TempDir(), "local")
	t.Setenv("TI_FAKE_DRIVE9_MOUNT_FAIL", "1")

	if _, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile: dataProfile(), MountPath: mountPath, MountProfile: "portable", LocalRoot: localRoot,
	}); err == nil {
		t.Fatal("failed mount reported success")
	}
	requireFakeDrive9Call(t, recordPath, "umount")
}

// TestMountStopHintMatchesLocatorState pins the advice to observed state: with a
// locator the ti unmount works, without one it must fall back to the companion.
// TestMountErrorKeepsCauseCodeAndFullMessage pins the composition helper every
// teardown site uses: the CLI renders apperr.MessageFor, which only sees an
// embedded apperr, so the helper must preserve the cause code and take the full
// text as the apperr message.
func TestMountErrorKeepsCauseCodeAndFullMessage(t *testing.T) {
	cause := apperr.New("fs.mount_ready_timeout", "runtime", 1, "cause")
	err := mountError(cause, "detail %d; %s", 7, "stop advice")
	if code := apperr.CodeFor(err); code != "fs.mount_ready_timeout" {
		t.Fatalf("code = %q, want the cause code", code)
	}
	if message := apperr.MessageFor(err); message != "detail 7; stop advice" {
		t.Fatalf("MessageFor = %q, want the composed text", message)
	}
	var appErr *apperr.Error
	if !errors.As(err, &appErr) {
		t.Fatalf("composition is not an apperr error: %T", err)
	}
}

func TestMountStopHintMatchesLocatorState(t *testing.T) {
	home := t.TempDir()
	service := testCompanionService(home, filepath.Join(t.TempDir(), "ti-drive9"))
	profile := dataProfile()
	mountPath := filepath.Join(t.TempDir(), "mnt")
	companionHome, err := fscred.CompanionHome(home, profile.Name, profile.FSResourceName)
	if err != nil {
		t.Fatal(err)
	}

	hint := service.mountStopHint(profile, mountPath, "ti fs unmount-file-system")
	if !strings.Contains(hint, "ti-drive9 umount") || !strings.Contains(hint, companionHome) {
		t.Fatalf("no-locator hint = %q, want the companion umount with its HOME", hint)
	}

	locator, err := mountlocator.New(profile.Name, profile.FSResourceName, profile.FSPlacementRegionCode, companionHome, mountPath, "fs")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := mountlocator.Write(home, locator); err != nil {
		t.Fatal(err)
	}
	hint = service.mountStopHint(profile, mountPath, "ti fs unmount-file-system")
	if !strings.Contains(hint, "ti fs unmount-file-system") || strings.Contains(hint, "ti-drive9") {
		t.Fatalf("locator hint = %q, want the ti unmount command", hint)
	}
}

// TestDrive9MountPreLocatorTeardownFailureAdvisesCompanion covers the
// failed-foreground double failure: no locator exists yet, so the only working
// stop advice is the companion's own umount.
func TestDrive9MountPreLocatorTeardownFailureAdvisesCompanion(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(t.TempDir(), companion)
	t.Setenv("TI_FAKE_DRIVE9_MOUNT_FAIL", "1")
	t.Setenv("TI_FAKE_UMOUNT_FAIL", "1")

	_, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile: dataProfile(), MountPath: filepath.Join(t.TempDir(), "mnt"), MountProfile: "portable",
		LocalRoot: filepath.Join(t.TempDir(), "local"),
	})
	if err == nil {
		t.Fatal("failed mount reported success")
	}
	message := apperr.MessageFor(err)
	if !strings.Contains(message, "may still be running") || !strings.Contains(message, "ti-drive9 umount") {
		t.Fatalf("teardown failure message = %q, want companion umount advice", message)
	}
	if strings.Contains(message, "unmount-file-system") {
		t.Fatalf("teardown failure message points at a locator that does not exist: %q", message)
	}
}

// TestDrive9MountLocatorWriteFailureTeardownAdvisesCompanion covers the second
// pre-locator double failure: the locator write failed and the teardown failed.
func TestDrive9MountLocatorWriteFailureTeardownAdvisesCompanion(t *testing.T) {
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(home, companion)
	// A file where the locator directory belongs makes the locator write fail.
	if err := os.MkdirAll(filepath.Join(home, ".ti"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ti", "mounts"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TI_FAKE_UMOUNT_FAIL", "1")

	_, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile: dataProfile(), MountPath: filepath.Join(t.TempDir(), "mnt"), MountProfile: "portable",
		LocalRoot: filepath.Join(t.TempDir(), "local"),
	})
	if err == nil {
		t.Fatal("mount with a failing locator write reported success")
	}
	message := apperr.MessageFor(err)
	if !strings.Contains(message, "may still be running") || !strings.Contains(message, "ti-drive9 umount") {
		t.Fatalf("locator failure message = %q, want companion umount advice", message)
	}
}

// TestDrive9MountUnsafeAuditPreLocatorTeardownAdvisesCompanion covers the
// runErr + audit-violation branch when its teardown also fails before a locator
// exists.
func TestDrive9MountUnsafeAuditPreLocatorTeardownAdvisesCompanion(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(t.TempDir(), companion)
	localRoot := filepath.Join(t.TempDir(), "local")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("victim"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TI_FAKE_MOUNT_ESCAPE_SYMLINK", "1")
	t.Setenv("TI_FAKE_ESCAPE_TARGET", outside)
	t.Setenv("TI_FAKE_DRIVE9_MOUNT_FAIL_AFTER_UNPACK", "1")
	t.Setenv("TI_FAKE_UMOUNT_FAIL", "1")

	_, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile: dataProfile(), MountPath: filepath.Join(t.TempDir(), "mnt"), MountProfile: "portable",
		LocalRoot: localRoot,
	})
	if apperr.CodeFor(err) != "fs.mount_failed_unsafe_overlay" {
		t.Fatalf("unsafe audit teardown error = %v", err)
	}
	if message := apperr.MessageFor(err); !strings.Contains(message, "ti-drive9 umount") || strings.Contains(message, "unmount-file-system") {
		t.Fatalf("unsafe audit teardown message = %q, want companion umount advice", message)
	}
}

// TestDrive9MountNoAutoUnpackKeepsOldFlagShape covers the eligibility predicate:
// --no-auto-unpack disables the profile-driven unpack, so no root is pinned and
// no audit runs.
func TestDrive9MountNoAutoUnpackKeepsOldFlagShape(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(t.TempDir(), companion)
	service.mountPointActive = trueMountEvidence
	mountPath := filepath.Join(t.TempDir(), "mnt")
	if _, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile: dataProfile(), MountPath: mountPath, MountProfile: "portable",
		PackPaths: []string{"/"}, NoAutoUnpack: true,
	}); err != nil {
		t.Fatalf("no-auto-unpack mount failed: %v", err)
	}
	call := requireFakeDrive9Call(t, recordPath, "mount")
	if containsArg(call.Args, "--local-root") {
		t.Fatalf("no-auto-unpack mount pinned a local root: %#v", call.Args)
	}
}

// TestDrive9MountAuditsEvenWhenReadinessTimesOut covers the ready-timeout path:
// the mount is deliberately left running there, so the audit must still run and
// the mount must be stopped when it finds an escaping link.
func TestDrive9MountAuditsEvenWhenReadinessTimesOut(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	home := t.TempDir()
	service := testCompanionService(home, companion)
	service.mountPointActive = func(string) (bool, error) { return false, nil }
	mountPath := filepath.Join(t.TempDir(), "mnt")
	localRoot := filepath.Join(t.TempDir(), "local")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("victim"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TI_FAKE_MOUNT_ESCAPE_SYMLINK", "1")
	t.Setenv("TI_FAKE_ESCAPE_TARGET", outside)

	_, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile: dataProfile(), MountPath: mountPath, MountProfile: "portable", LocalRoot: localRoot,
		ReadyTimeout: 50 * time.Millisecond,
	})
	if apperr.CodeFor(err) != "fs.unsafe_pack_symlink" {
		t.Fatalf("ready-timeout audit error = %v", err)
	}
	requireFakeDrive9Call(t, recordPath, "umount")
	if _, _, locatorErr := mountlocator.Read(home, mountPath); !os.IsNotExist(locatorErr) {
		t.Fatalf("locator left behind after the mount was stopped: %v", locatorErr)
	}
}

// TestDrive9MountAuditCleanupFailureKeepsLocator covers the teardown path when
// the companion cannot stop the mount: the error must say so and the locator must
// survive so ti fs unmount-file-system can retry.
func TestDrive9MountAuditCleanupFailureKeepsLocator(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	home := t.TempDir()
	service := testCompanionService(home, companion)
	service.mountPointActive = trueMountEvidence
	mountPath := filepath.Join(t.TempDir(), "mnt")
	localRoot := filepath.Join(t.TempDir(), "local")
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("victim"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TI_FAKE_MOUNT_ESCAPE_SYMLINK", "1")
	t.Setenv("TI_FAKE_ESCAPE_TARGET", outside)
	t.Setenv("TI_FAKE_UMOUNT_FAIL", "1")

	_, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile: dataProfile(), MountPath: mountPath, MountProfile: "portable", LocalRoot: localRoot,
	})
	if apperr.CodeFor(err) != "fs.unsafe_pack_symlink" {
		t.Fatalf("cleanup failure error = %v", err)
	}
	if !strings.Contains(apperr.MessageFor(err), "may still be running") || !strings.Contains(apperr.MessageFor(err), "unmount-file-system") {
		t.Fatalf("cleanup failure error lacks guidance: %v", err)
	}
	if _, _, locatorErr := mountlocator.Read(home, mountPath); locatorErr != nil {
		t.Fatalf("locator must survive a failed cleanup: %v", locatorErr)
	}
}

// TestDrive9MountRejectsUnpackWithoutOverlayProfile refuses an unpack target that
// cannot be audited instead of skipping the audit.
func TestDrive9MountRejectsUnpackWithoutOverlayProfile(t *testing.T) {
	companion, _ := buildFakeDrive9(t)
	service := testCompanionService(t.TempDir(), companion)
	_, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile: dataProfile(), MountPath: filepath.Join(t.TempDir(), "mnt"), MountProfile: "none",
		UnpackArchivePath: "/workspace/backup.tgz",
	})
	if apperr.CodeFor(err) != "fs.mount_unpack_requires_profile" {
		t.Fatalf("unpack without overlay profile error = %v", err)
	}
}

// TestDrive9MountKeepsDefaultFlagShape pins that a plain mount still passes no
// --local-root, so the audit change does not alter ordinary mounts.
func TestDrive9MountKeepsDefaultFlagShape(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(t.TempDir(), companion)
	service.mountPointActive = trueMountEvidence
	mountPath := filepath.Join(t.TempDir(), "mnt")
	if _, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile: dataProfile(), MountPath: mountPath, ReadyTimeout: time.Second,
	}); err != nil {
		t.Fatalf("plain mount failed: %v", err)
	}
	call := requireFakeDrive9Call(t, recordPath, "mount")
	for i, arg := range call.Args {
		if arg == "--local-root" {
			t.Fatalf("plain mount passed --local-root %q", call.Args[i+1])
		}
	}
}

func TestDrive9MountSuppressesCompanionSuccessChatter(t *testing.T) {
	home := t.TempDir()
	companion, _ := buildFakeDrive9(t)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	service := testCompanionService(home, companion)
	service.mountPointActive = trueMountEvidence
	service.Stdout = &stdout
	service.Stderr = &stderr

	result, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile:        dataProfile(),
		FileSystemName: "workspace",
		MountPath:      filepath.Join(t.TempDir(), "workspace"),
		RemotePath:     "/",
	})
	if err != nil {
		t.Fatalf("MountFileSystem failed: %v", err)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("companion mount output leaked to ti output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if result.Driver != "webdav" || result.Status != "mounted" {
		t.Fatalf("mount result did not retain captured companion state: %#v", result)
	}
}

func TestDrive9MountPreservesCapturedFailureDiagnostics(t *testing.T) {
	companion, _ := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_MOUNT_FAIL", "1")
	_, err := testCompanionService(t.TempDir(), companion).MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile:        dataProfile(),
		FileSystemName: "workspace",
		MountPath:      filepath.Join(t.TempDir(), "workspace"),
		RemotePath:     "/",
	})
	if err == nil {
		t.Fatal("expected mount failure")
	}
	if message := apperr.MessageFor(err); !strings.Contains(message, "background mount exited before becoming ready") {
		t.Fatalf("mount failure lost companion diagnostics: %q", message)
	}
}

func TestDrive9FailedUnmountPreservesMountLocator(t *testing.T) {
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(home, companion)
	service.mountPointActive = trueMountEvidence
	mountPath := filepath.Join(t.TempDir(), "workspace")
	if _, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile:        dataProfile(),
		FileSystemName: "workspace",
		MountPath:      mountPath,
		RemotePath:     "/",
	}); err != nil {
		t.Fatalf("MountFileSystem failed: %v", err)
	}

	service.CompanionPath = filepath.Join(t.TempDir(), "missing-ti-drive9")
	if _, err := service.UnmountFileSystem(context.Background(), UnmountFileSystemOptions{
		Profile:   &config.Profile{Name: "default", HomeDir: home},
		MountPath: mountPath,
		Timeout:   time.Second,
	}); err == nil {
		t.Fatal("expected unmount failure")
	}
	if _, _, err := mountlocator.Read(home, mountPath); err != nil {
		t.Fatalf("failed unmount removed mount locator: %v", err)
	}
}
