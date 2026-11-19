package fs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/fs/fscred"
	"github.com/tidbcloud/ti-cli/internal/fs/mountcontrol"
	"github.com/tidbcloud/ti-cli/internal/fs/mountlocator"
)

// TestDrive9DrainReportsPendingCounters keeps the engine's drain report in the
// result instead of discarding the `--json` output.
func TestDrive9DrainReportsPendingCounters(t *testing.T) {
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(home, companion)
	service.mountPointActive = trueMountEvidence
	mountPath := filepath.Join(t.TempDir(), "mnt")
	if _, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile: dataProfile(), MountPath: mountPath, Driver: "fuse",
	}); err != nil {
		t.Fatalf("mount failed: %v", err)
	}

	result, err := service.DrainFileSystem(context.Background(), DrainFileSystemOptions{Profile: dataProfile(), MountPath: mountPath})
	if err != nil {
		t.Fatalf("drain failed: %v", err)
	}
	if result.Status != "drained" || result.Response == nil || !result.Response.OK {
		t.Fatalf("unexpected drain result: %#v", result)
	}
	if result.Driver != "fuse" {
		t.Fatalf("drain driver = %q, want fuse", result.Driver)
	}
	if result.Response.DurationMS != 7 {
		t.Fatalf("drain duration = %d, want the companion's report", result.Response.DurationMS)
	}
	human := result.Human()
	if !strings.Contains(human, "pending: none") {
		t.Fatalf("text output dropped the pending counters:\n%s", human)
	}
	mountHuman := MountResult{Status: "mounted", MountPath: "/mnt", Driver: "fuse", Durability: MountDurabilityCloseSync}.Human()
	if !strings.Contains(mountHuman, "Durability: close-sync") {
		t.Fatalf("mount text output dropped the effective tier:\n%s", mountHuman)
	}
}

// TestDrive9DrainFailureNamesPendingWork turns an undrained mount into an
// actionable error with the counters that explain it.
func TestDrive9DrainFailureNamesPendingWork(t *testing.T) {
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	t.Setenv("TI_FAKE_DRIVE9_DRAIN_PENDING", "1")
	service := testCompanionService(home, companion)
	service.mountPointActive = trueMountEvidence
	mountPath := filepath.Join(t.TempDir(), "mnt")
	if _, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile: dataProfile(), MountPath: mountPath, Driver: "fuse",
	}); err != nil {
		t.Fatalf("mount failed: %v", err)
	}

	_, err := service.DrainFileSystem(context.Background(), DrainFileSystemOptions{Profile: dataProfile(), MountPath: mountPath})
	if apperr.CodeFor(err) != "fs.drain_pending_work" {
		t.Fatalf("drain error = %v, want fs.drain_pending_work", err)
	}
	message := apperr.MessageFor(err)
	for _, want := range []string{"pending_work_remaining", "commit_queue_pending=2", "uploader_queued=2", "ti fs drain-file-system"} {
		if !strings.Contains(message, want) {
			t.Fatalf("drain error %q missing %q", message, want)
		}
	}
}

// TestUnmountDrainsBeforeDetach is the core fix: unmount must not detach while
// local writes are still pending.
func TestUnmountDrainsBeforeDetach(t *testing.T) {
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(home, companion)
	service.mountPointActive = trueMountEvidence
	mountPath := filepath.Join(t.TempDir(), "mnt")
	if _, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile: dataProfile(), MountPath: mountPath, Driver: "fuse",
	}); err != nil {
		t.Fatalf("mount failed: %v", err)
	}

	result, err := service.UnmountFileSystem(context.Background(), UnmountFileSystemOptions{Profile: dataProfile(), MountPath: mountPath})
	if err != nil {
		t.Fatalf("unmount failed: %v", err)
	}
	if result.Status != "unmounted" || result.Drain == nil || result.Drain.Status != "drained" {
		t.Fatalf("unexpected unmount result: %#v", result)
	}
	drainIndex, umountIndex := -1, -1
	for i, call := range readFakeDrive9Calls(t, recordPath) {
		switch {
		case len(call.Args) >= 2 && call.Args[0] == "mount" && call.Args[1] == "drain":
			drainIndex = i
		case len(call.Args) >= 1 && call.Args[0] == "umount":
			umountIndex = i
		}
	}
	if drainIndex < 0 || umountIndex < 0 || drainIndex > umountIndex {
		t.Fatalf("drain did not run before umount: drain=%d umount=%d", drainIndex, umountIndex)
	}
	if !strings.Contains(result.Human(), "drain") {
		t.Fatalf("text output dropped the drain outcome:\n%s", result.Human())
	}
}

// TestUnmountRefusesToDetachWithPendingWork blocks the unmount and keeps the
// locator so the write loss is explicit and retryable.
func TestUnmountRefusesToDetachWithPendingWork(t *testing.T) {
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(home, companion)
	service.mountPointActive = trueMountEvidence
	mountPath := filepath.Join(t.TempDir(), "mnt")
	if _, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile: dataProfile(), MountPath: mountPath, Driver: "fuse",
	}); err != nil {
		t.Fatalf("mount failed: %v", err)
	}
	t.Setenv("TI_FAKE_DRIVE9_DRAIN_PENDING", "1")

	_, err := service.UnmountFileSystem(context.Background(), UnmountFileSystemOptions{Profile: dataProfile(), MountPath: mountPath})
	if apperr.CodeFor(err) != "fs.drain_pending_work" {
		t.Fatalf("unmount error = %v, want fs.drain_pending_work", err)
	}
	if _, _, locErr := mountlocator.Read(home, mountPath); locErr != nil {
		t.Fatalf("blocked unmount must preserve the locator: %v", locErr)
	}
	for _, call := range readFakeDrive9Calls(t, recordPath) {
		if len(call.Args) > 0 && call.Args[0] == "umount" {
			t.Fatalf("unmount ran despite pending writes: %#v", call.Args)
		}
	}
}

// TestUnmountForceSkipsDrainGate gives the operator an explicit way to accept
// the loss, and records that it was skipped.
func TestUnmountForceSkipsDrainGate(t *testing.T) {
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	t.Setenv("TI_FAKE_DRIVE9_DRAIN_PENDING", "1")
	service := testCompanionService(home, companion)
	service.mountPointActive = trueMountEvidence
	mountPath := filepath.Join(t.TempDir(), "mnt")
	if _, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile: dataProfile(), MountPath: mountPath, Driver: "fuse",
	}); err != nil {
		t.Fatalf("mount failed: %v", err)
	}

	result, err := service.UnmountFileSystem(context.Background(), UnmountFileSystemOptions{Profile: dataProfile(), MountPath: mountPath, Force: true})
	if err != nil {
		t.Fatalf("forced unmount failed: %v", err)
	}
	if result.Drain == nil || result.Drain.Status != "skipped" || !strings.Contains(result.Drain.Message, "--force") {
		t.Fatalf("forced unmount did not record the skipped gate: %#v", result.Drain)
	}
	for _, call := range readFakeDrive9Calls(t, recordPath) {
		if len(call.Args) >= 2 && call.Args[0] == "mount" && call.Args[1] == "drain" {
			t.Fatalf("--force still drained: %#v", call.Args)
		}
	}
}

// TestUnmountSkipsDrainForWebDAV keeps WebDAV unmount working: there is no
// control socket to drain.
func TestUnmountSkipsDrainForWebDAV(t *testing.T) {
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(home, companion)
	service.mountPointActive = trueMountEvidence
	mountPath := filepath.Join(t.TempDir(), "mnt")
	if _, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile: dataProfile(), MountPath: mountPath, Driver: "webdav",
	}); err != nil {
		t.Fatalf("webdav mount failed: %v", err)
	}
	locator, _, err := mountlocator.Read(home, mountPath)
	if err != nil {
		t.Fatalf("read locator: %v", err)
	}
	if locator.Driver != "webdav" {
		t.Fatalf("locator driver = %q, want webdav", locator.Driver)
	}

	result, err := service.UnmountFileSystem(context.Background(), UnmountFileSystemOptions{Profile: dataProfile(), MountPath: mountPath})
	if err != nil {
		t.Fatalf("webdav unmount failed: %v", err)
	}
	if result.Drain == nil || result.Drain.Status != "skipped" || !strings.Contains(result.Drain.Message, "webdav") {
		t.Fatalf("webdav unmount drain record = %#v", result.Drain)
	}
	for _, call := range readFakeDrive9Calls(t, recordPath) {
		if len(call.Args) >= 2 && call.Args[0] == "mount" && call.Args[1] == "drain" {
			t.Fatalf("webdav unmount drained: %#v", call.Args)
		}
	}
}

// TestUnmountSkipsDrainForVaultMount keeps `ti fs-vault unmount-vault` working:
// a vault mount has no file-system drain, and the engine rejects one.
func TestUnmountSkipsDrainForVaultMount(t *testing.T) {
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(home, companion)
	mountPath := filepath.Join(t.TempDir(), "vault")
	companionHome, err := fscred.CompanionHome(home, "default", "tenant-1")
	if err != nil {
		t.Fatalf("resolve companion home: %v", err)
	}
	locator, err := mountlocator.New("default", "tenant-1", "aws-us-east-1", companionHome, mountPath, "vault")
	if err != nil {
		t.Fatalf("construct locator: %v", err)
	}
	if _, err := mountlocator.Write(home, locator.WithDriver("fuse")); err != nil {
		t.Fatalf("write locator: %v", err)
	}

	result, err := service.UnmountFileSystem(context.Background(), UnmountFileSystemOptions{Profile: dataProfile(), MountPath: mountPath})
	if err != nil {
		t.Fatalf("vault unmount failed: %v", err)
	}
	if result.Drain == nil || result.Drain.Status != "skipped" || !strings.Contains(result.Drain.Message, "vault") {
		t.Fatalf("vault drain record = %#v", result.Drain)
	}
	for _, call := range readFakeDrive9Calls(t, recordPath) {
		if len(call.Args) >= 2 && call.Args[0] == "mount" && call.Args[1] == "drain" {
			t.Fatalf("vault unmount drained: %#v", call.Args)
		}
	}
}

// TestUnmountSkipsDrainForLegacyLocator never gates a mount recorded before the
// driver was written, so an upgrade cannot block an existing unmount.
func TestUnmountSkipsDrainForLegacyLocator(t *testing.T) {
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(home, companion)
	profile := dataProfile()
	mountPath := filepath.Join(t.TempDir(), "mnt")
	if _, err := mountlocator.Write(home, legacyTestLocator(t, home, mountPath)); err != nil {
		t.Fatalf("write locator: %v", err)
	}

	result, err := service.UnmountFileSystem(context.Background(), UnmountFileSystemOptions{Profile: profile, MountPath: mountPath})
	if err != nil {
		t.Fatalf("legacy unmount failed: %v", err)
	}
	if result.Drain == nil || result.Drain.Status != "skipped" || !strings.Contains(result.Drain.Message, "no driver") {
		t.Fatalf("legacy locator drain record = %#v", result.Drain)
	}
	for _, call := range readFakeDrive9Calls(t, recordPath) {
		if len(call.Args) >= 2 && call.Args[0] == "mount" && call.Args[1] == "drain" {
			t.Fatalf("legacy locator drained: %#v", call.Args)
		}
	}
}

// TestUnmountIgnoreAbsentStillBlocksPendingWork keeps --ignore-absent from
// becoming a silent loss-acceptance flag: it is the idempotency flag cleanup
// scripts pass, so pending writes must still block the detach.
func TestUnmountIgnoreAbsentStillBlocksPendingWork(t *testing.T) {
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(home, companion)
	service.mountPointActive = trueMountEvidence
	mountPath := filepath.Join(t.TempDir(), "mnt")
	if _, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile: dataProfile(), MountPath: mountPath, Driver: "fuse",
	}); err != nil {
		t.Fatalf("mount failed: %v", err)
	}
	t.Setenv("TI_FAKE_DRIVE9_DRAIN_PENDING", "1")

	_, err := service.UnmountFileSystem(context.Background(), UnmountFileSystemOptions{Profile: dataProfile(), MountPath: mountPath, IgnoreAbsent: true})
	if apperr.CodeFor(err) != "fs.drain_pending_work" {
		t.Fatalf("ignore-absent unmount error = %v, want fs.drain_pending_work", err)
	}
	if !strings.Contains(apperr.MessageFor(err), "--force") || !strings.Contains(apperr.MessageFor(err), "--ignore-absent does not skip") {
		t.Fatalf("blocked drain error must name both flags: %v", apperr.MessageFor(err))
	}
	if _, _, locErr := mountlocator.Read(home, mountPath); locErr != nil {
		t.Fatalf("blocked unmount must preserve the locator: %v", locErr)
	}
	for _, call := range readFakeDrive9Calls(t, recordPath) {
		if len(call.Args) > 0 && call.Args[0] == "umount" {
			t.Fatalf("unmount ran despite pending writes: %#v", call.Args)
		}
	}
}

// TestUnmountIgnoreAbsentToleratesGoneMount keeps cleanup idempotent: a drain
// that cannot reach the mount (not pending work) may continue.
func TestUnmountIgnoreAbsentToleratesGoneMount(t *testing.T) {
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(home, companion)
	service.mountPointActive = trueMountEvidence
	mountPath := filepath.Join(t.TempDir(), "mnt")
	if _, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile: dataProfile(), MountPath: mountPath, Driver: "fuse",
	}); err != nil {
		t.Fatalf("mount failed: %v", err)
	}
	t.Setenv("TI_FAKE_DRIVE9_DRAIN_FAIL", "1")

	result, err := service.UnmountFileSystem(context.Background(), UnmountFileSystemOptions{Profile: dataProfile(), MountPath: mountPath, IgnoreAbsent: true})
	if err != nil {
		t.Fatalf("ignore-absent unmount failed: %v", err)
	}
	if result.Drain == nil || result.Drain.Status != "failed" {
		t.Fatalf("drain record = %#v, want failed", result.Drain)
	}
	found := false
	for _, check := range result.Checks {
		if check.Name == "drain" && check.Status == "failed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("unmount did not report the failed drain: %#v", result.Checks)
	}
}

// TestUnmountIgnoreAbsentAbsentPathStillReportsDrainFailure keeps the two
// success shapes aligned: when the unmount itself reports the mount absent, the
// tolerated drain failure is still recorded.
func TestUnmountIgnoreAbsentAbsentPathStillReportsDrainFailure(t *testing.T) {
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(home, companion)
	service.mountPointActive = trueMountEvidence
	mountPath := filepath.Join(t.TempDir(), "mnt")
	if _, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile: dataProfile(), MountPath: mountPath, Driver: "fuse",
	}); err != nil {
		t.Fatalf("mount failed: %v", err)
	}
	t.Setenv("TI_FAKE_DRIVE9_DRAIN_FAIL", "1")
	t.Setenv("TI_FAKE_UMOUNT_FAIL", "1")

	result, err := service.UnmountFileSystem(context.Background(), UnmountFileSystemOptions{Profile: dataProfile(), MountPath: mountPath, IgnoreAbsent: true})
	if err != nil {
		t.Fatalf("ignore-absent unmount failed: %v", err)
	}
	if result.Status != "absent" || result.Drain == nil || result.Drain.Status != "failed" {
		t.Fatalf("unexpected absent result: %#v", result)
	}
	found := false
	for _, check := range result.Checks {
		if check.Name == "drain" && check.Status == "failed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("absent path did not report the tolerated drain failure: %#v", result.Checks)
	}
}

// TestUnmountIgnoreAbsentStillBlocksParsedPendingWork covers the classification
// hole: a drain that fails with a non-pending error_kind while the report shows
// queued work must still block.
func TestUnmountIgnoreAbsentStillBlocksParsedPendingWork(t *testing.T) {
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(home, companion)
	service.mountPointActive = trueMountEvidence
	mountPath := filepath.Join(t.TempDir(), "mnt")
	if _, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile: dataProfile(), MountPath: mountPath, Driver: "fuse",
	}); err != nil {
		t.Fatalf("mount failed: %v", err)
	}
	t.Setenv("TI_FAKE_DRIVE9_DRAIN_PENDING_KIND", "deadline_exceeded")

	_, err := service.UnmountFileSystem(context.Background(), UnmountFileSystemOptions{Profile: dataProfile(), MountPath: mountPath, IgnoreAbsent: true})
	if apperr.CodeFor(err) != "fs.drain_pending_work" {
		t.Fatalf("ignore-absent unmount error = %v, want fs.drain_pending_work", err)
	}
	for _, call := range readFakeDrive9Calls(t, recordPath) {
		if len(call.Args) > 0 && call.Args[0] == "umount" {
			t.Fatalf("unmount ran with queued work: %#v", call.Args)
		}
	}
}

// TestUnmountIgnoreAbsentToleratesZeroCounterFailure pins the tolerated shape:
// a parsed report with a non-pending classification and no queued counters
// reports drain.status "failed" but lets the cleanup unmount continue.
func TestUnmountIgnoreAbsentToleratesZeroCounterFailure(t *testing.T) {
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(home, companion)
	service.mountPointActive = trueMountEvidence
	mountPath := filepath.Join(t.TempDir(), "mnt")
	if _, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile: dataProfile(), MountPath: mountPath, Driver: "fuse",
	}); err != nil {
		t.Fatalf("mount failed: %v", err)
	}
	t.Setenv("TI_FAKE_DRIVE9_DRAIN_ZERO_KIND", "deadline_exceeded")

	result, err := service.UnmountFileSystem(context.Background(), UnmountFileSystemOptions{Profile: dataProfile(), MountPath: mountPath, IgnoreAbsent: true})
	if err != nil {
		t.Fatalf("ignore-absent unmount failed: %v", err)
	}
	if result.Status != "unmounted" || result.Drain == nil || result.Drain.Status != "failed" {
		t.Fatalf("unexpected unmount result: %#v", result)
	}
	if result.Drain.Response == nil || result.Drain.Response.Pending.HasPendingWork() {
		t.Fatalf("fixture was supposed to report no queued work: %#v", result.Drain.Response)
	}
}

// TestDryRunDrainSupportForDriverlessLocator keeps the drain dry run honest for
// mounts whose locator predates the recorded driver: the drain command still
// accepts them, so the check must not claim it cannot run.
func TestDryRunDrainSupportForDriverlessLocator(t *testing.T) {
	home := t.TempDir()
	companion, _ := buildFakeDrive9(t)
	service := testCompanionService(home, companion)
	mountPath := filepath.Join(t.TempDir(), "mnt")
	if _, err := mountlocator.Write(home, legacyTestLocator(t, home, mountPath)); err != nil {
		t.Fatalf("write locator: %v", err)
	}
	result, err := service.DryRunDrainFileSystem(context.Background(), "ti fs drain-file-system", DrainFileSystemOptions{Profile: dataProfile(), MountPath: mountPath})
	if err != nil {
		t.Fatalf("dry run failed: %v", err)
	}
	for _, check := range result.Checks {
		if check.Name != "drain_support" {
			continue
		}
		if check.Status != "passed" {
			t.Fatalf("driver-less drain dry run = %#v, want passed", check)
		}
		return
	}
	t.Fatal("drain_support check missing")
}

// TestHasPendingWorkIsIndependentOfRendering pins every counter the predicate
// enumerates and keeps it from ever depending on the operator-facing summary.
func TestHasPendingWorkIsIndependentOfRendering(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pending mountcontrol.DrainPending
		want    bool
	}{
		{name: "empty", pending: mountcontrol.DrainPending{}, want: false},
		{name: "open handles", pending: mountcontrol.DrainPending{OpenHandles: 1}, want: true},
		{name: "dirty handles", pending: mountcontrol.DrainPending{DirtyHandles: 1}, want: true},
		{name: "commit queue pending", pending: mountcontrol.DrainPending{CommitQueuePending: 1}, want: true},
		{name: "commit queue bytes", pending: mountcontrol.DrainPending{CommitQueueBytes: 1}, want: true},
		{name: "commit queue in flight", pending: mountcontrol.DrainPending{CommitQueueInFlight: 1}, want: true},
		{name: "commit queue delayed", pending: mountcontrol.DrainPending{CommitQueueDelayed: 1}, want: true},
		{name: "commit queue conflicts", pending: mountcontrol.DrainPending{CommitQueueConflicts: 1}, want: true},
		{name: "uploader queued", pending: mountcontrol.DrainPending{UploaderQueued: 1}, want: true},
		{name: "uploader in flight", pending: mountcontrol.DrainPending{UploaderInFlight: 1}, want: true},
		{name: "uploader cached", pending: mountcontrol.DrainPending{UploaderCached: 1}, want: true},
		{name: "uploader cached bytes", pending: mountcontrol.DrainPending{UploaderCachedBytes: 1}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.pending.HasPendingWork()
			if got != tc.want {
				t.Fatalf("HasPendingWork(%+v) = %t, want %t", tc.pending, got, tc.want)
			}
			summary := drainPendingSummary(tc.pending)
			if tc.want != (summary != "none") {
				t.Fatalf("summary %q disagrees with the predicate for %+v", summary, tc.pending)
			}
		})
	}
}

// TestDrainRemedyMatchesClassification keeps the error guidance accurate: only
// pending work is unbailable by --ignore-absent.
func TestDrainRemedyMatchesClassification(t *testing.T) {
	pending := drainRemedy("fs.drain_pending_work", "/mnt")
	if !strings.Contains(pending, "--force") || !strings.Contains(pending, "--ignore-absent does not skip") {
		t.Fatalf("pending remedy = %q", pending)
	}
	other := drainRemedy("fs.drain_failed", "/mnt")
	if strings.Contains(other, "does not skip") || !strings.Contains(other, "--ignore-absent") || !strings.Contains(other, "--force") {
		t.Fatalf("non-pending remedy = %q", other)
	}
}

// TestDrainRejectsNonFileSystemMount keeps the drain command from reaching the
// companion with a mount the engine cannot drain.
func TestDrainRejectsNonFileSystemMount(t *testing.T) {
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(home, companion)
	mountPath := filepath.Join(t.TempDir(), "vault")
	if _, err := mountlocator.Write(home, legacyTestLocatorWithKind(t, home, mountPath, "vault", "fuse")); err != nil {
		t.Fatalf("write locator: %v", err)
	}
	if drainable, _ := drainApplicability(mountlocator.Locator{Kind: mountKindFileSystem, Driver: "webdav"}); drainable {
		t.Fatal("webdav locator reported drainable")
	}
	if drainable, _ := drainApplicability(mountlocator.Locator{Kind: mountKindFileSystem}); !drainable {
		t.Fatal("locator with an unrecorded driver must stay drainable")
	}
	if _, err := service.DrainFileSystem(context.Background(), DrainFileSystemOptions{Profile: dataProfile(), MountPath: mountPath}); apperr.CodeFor(err) != "fs.drain_not_applicable" {
		t.Fatalf("vault drain error = %v, want fs.drain_not_applicable", err)
	}
	if _, statErr := os.Stat(recordPath); statErr == nil {
		for _, call := range readFakeDrive9Calls(t, recordPath) {
			if len(call.Args) >= 2 && call.Args[0] == "mount" && call.Args[1] == "drain" {
				t.Fatalf("vault drain reached the companion: %#v", call.Args)
			}
		}
	}
}

// TestMountRecordsDriverInLocator records what the companion actually mounted,
// because the unmount gate depends on it.
func TestMountRecordsDriverInLocator(t *testing.T) {
	home := t.TempDir()
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(home, companion)
	service.mountPointActive = trueMountEvidence
	mountPath := filepath.Join(t.TempDir(), "mnt")
	if _, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile: dataProfile(), MountPath: mountPath, Driver: "fuse",
	}); err != nil {
		t.Fatalf("mount failed: %v", err)
	}
	locator, _, err := mountlocator.Read(home, mountPath)
	if err != nil {
		t.Fatalf("read locator: %v", err)
	}
	if locator.Driver != "fuse" {
		t.Fatalf("locator driver = %q, want fuse", locator.Driver)
	}
}

func legacyTestLocatorWithKind(t *testing.T, home, mountPath, kind, driver string) mountlocator.Locator {
	t.Helper()
	companionHome, err := fscred.CompanionHome(home, "default", "tenant-1")
	if err != nil {
		t.Fatalf("resolve companion home: %v", err)
	}
	locator, err := mountlocator.New("default", "tenant-1", "aws-us-east-1", companionHome, mountPath, kind)
	if err != nil {
		t.Fatalf("construct locator: %v", err)
	}
	return locator.WithDriver(driver)
}

func legacyTestLocator(t *testing.T, home, mountPath string) mountlocator.Locator {
	t.Helper()
	companionHome, err := fscred.CompanionHome(home, "default", "tenant-1")
	if err != nil {
		t.Fatalf("resolve companion home: %v", err)
	}
	locator, err := mountlocator.New("default", "tenant-1", "aws-us-east-1", companionHome, mountPath, "fs")
	if err != nil {
		t.Fatalf("construct locator: %v", err)
	}
	return locator
}
