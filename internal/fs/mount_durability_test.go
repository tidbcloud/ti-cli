package fs

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/tidbcloud/ti-cli/internal/apperr"
)

// TestNormalizeMountDurability pins the tier contract: --durability keeps the
// interactive default, the legacy boolean alias maps onto two tiers, and an
// explicit but unusable tier is refused instead of silently downgraded.
func TestNormalizeMountDurability(t *testing.T) {
	for _, tc := range []struct {
		name       string
		opts       MountFileSystemOptions
		want       string
		wantCode   string
		forwarded  bool
		writeBack  bool
		explicitly bool
	}{
		{
			name:       "default keeps interactive",
			opts:       MountFileSystemOptions{Driver: "fuse"},
			want:       MountDurabilityInteractive,
			forwarded:  true,
			writeBack:  true,
			explicitly: false,
		},
		{
			name:       "auto resolves to the engine default",
			opts:       MountFileSystemOptions{Driver: "fuse", Durability: MountDurabilityAuto, DurabilitySet: true},
			want:       MountDurabilityAuto,
			forwarded:  true,
			writeBack:  true,
			explicitly: true,
		},
		{
			name:       "fsync stays a write-back tier",
			opts:       MountFileSystemOptions{Driver: "fuse", Durability: MountDurabilityFsync, DurabilitySet: true},
			want:       MountDurabilityFsync,
			forwarded:  true,
			writeBack:  true,
			explicitly: true,
		},
		{
			name:       "close-sync leaves the write-back policy",
			opts:       MountFileSystemOptions{Driver: "fuse", Durability: MountDurabilityCloseSync, DurabilitySet: true},
			want:       MountDurabilityCloseSync,
			forwarded:  true,
			writeBack:  false,
			explicitly: true,
		},
		{
			name:       "write-sync leaves the write-back policy",
			opts:       MountFileSystemOptions{Driver: "fuse", Durability: MountDurabilityWriteSync, DurabilitySet: true},
			want:       MountDurabilityWriteSync,
			forwarded:  true,
			writeBack:  false,
			explicitly: true,
		},
		{
			name:       "value is trimmed and case-folded",
			opts:       MountFileSystemOptions{Driver: "fuse", Durability: "  Close-Sync ", DurabilitySet: true},
			want:       MountDurabilityCloseSync,
			forwarded:  true,
			writeBack:  false,
			explicitly: true,
		},
		{
			name:       "legacy true means interactive",
			opts:       MountFileSystemOptions{Driver: "fuse", WriteBackCache: true, WriteBackCacheSet: true},
			want:       MountDurabilityInteractive,
			forwarded:  true,
			writeBack:  true,
			explicitly: true,
		},
		{
			name:       "legacy false means auto",
			opts:       MountFileSystemOptions{Driver: "fuse", WriteBackCache: false, WriteBackCacheSet: true},
			want:       MountDurabilityAuto,
			forwarded:  true,
			writeBack:  true,
			explicitly: true,
		},
		{
			name:       "legacy alias may agree with the tier",
			opts:       MountFileSystemOptions{Driver: "fuse", Durability: MountDurabilityInteractive, DurabilitySet: true, WriteBackCache: true, WriteBackCacheSet: true},
			want:       MountDurabilityInteractive,
			forwarded:  true,
			writeBack:  true,
			explicitly: true,
		},
		{
			name:     "unknown tier is refused",
			opts:     MountFileSystemOptions{Driver: "fuse", Durability: "turbo", DurabilitySet: true},
			wantCode: "fs.invalid_mount_durability",
		},
		{
			name:     "conflicting alias is refused",
			opts:     MountFileSystemOptions{Driver: "fuse", Durability: MountDurabilityAuto, DurabilitySet: true, WriteBackCache: true, WriteBackCacheSet: true},
			wantCode: "fs.conflicting_mount_durability",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeMountOptions(tc.opts)
			if tc.wantCode != "" {
				if apperr.CodeFor(err) != tc.wantCode {
					t.Fatalf("error = %v, want %s", err, tc.wantCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeMountOptions failed: %v", err)
			}
			if got.Durability != tc.want {
				t.Fatalf("durability = %q, want %q", got.Durability, tc.want)
			}
			if mountDurabilityForwarded(got) != tc.forwarded {
				t.Fatalf("forwarded = %t, want %t", mountDurabilityForwarded(got), tc.forwarded)
			}
			if mountDurabilityUsesWriteBack(got.Durability) != tc.writeBack {
				t.Fatalf("write-back policy = %t, want %t", mountDurabilityUsesWriteBack(got.Durability), tc.writeBack)
			}
			if got.DurabilityExplicit != tc.explicitly {
				t.Fatalf("explicit = %t, want %t", got.DurabilityExplicit, tc.explicitly)
			}
		})
	}
}

// TestNormalizeMountDurabilityWebDAV keeps the WebDAV contract honest: the
// engine has no tiers there, so ti forwards none and refuses an explicit tier
// that would otherwise look honored.
func TestNormalizeMountDurabilityWebDAV(t *testing.T) {
	got, err := normalizeMountOptions(MountFileSystemOptions{Driver: "webdav"})
	if err != nil {
		t.Fatalf("default webdav mount failed: %v", err)
	}
	if got.Durability != MountDurabilityAuto {
		t.Fatalf("webdav durability = %q, want %s", got.Durability, MountDurabilityAuto)
	}
	if mountDurabilityForwarded(got) {
		t.Fatal("webdav mount must not forward --durability")
	}
	if _, err := normalizeMountOptions(MountFileSystemOptions{Driver: "webdav", Durability: MountDurabilityAuto, DurabilitySet: true}); err != nil {
		t.Fatalf("explicit auto webdav mount failed: %v", err)
	}
	for _, tier := range []string{MountDurabilityInteractive, MountDurabilityFsync, MountDurabilityCloseSync, MountDurabilityWriteSync} {
		if _, err := normalizeMountOptions(MountFileSystemOptions{Driver: "webdav", Durability: tier, DurabilitySet: true}); apperr.CodeFor(err) != "fs.webdav_durability_unsupported" {
			t.Fatalf("webdav %s error = %v, want fs.webdav_durability_unsupported", tier, err)
		}
	}
	if _, err := normalizeMountOptions(MountFileSystemOptions{Driver: "webdav", WriteBackCache: true, WriteBackCacheSet: true}); apperr.CodeFor(err) != "fs.webdav_durability_unsupported" {
		t.Fatalf("webdav write-back-cache error = %v, want fs.webdav_durability_unsupported", err)
	}
}

// TestNormalizeMountDurabilityCheckpoint keeps read-only checkpoint mounts on
// the engine default and refuses a tier that cannot take effect there.
func TestNormalizeMountDurabilityCheckpoint(t *testing.T) {
	base := MountFileSystemOptions{Driver: "fuse", LayerRef: "layer", CheckpointID: "cp-1"}
	got, err := normalizeMountOptions(base)
	if err != nil {
		t.Fatalf("default checkpoint mount failed: %v", err)
	}
	if !got.ReadOnly {
		t.Fatal("checkpoint mount must be read-only")
	}
	if got.Durability != MountDurabilityAuto {
		t.Fatalf("checkpoint durability = %q, want %s", got.Durability, MountDurabilityAuto)
	}
	if mountDurabilityForwarded(got) {
		t.Fatal("checkpoint mount must not forward --durability")
	}

	explicitAuto := base
	explicitAuto.Durability = MountDurabilityAuto
	explicitAuto.DurabilitySet = true
	if _, err := normalizeMountOptions(explicitAuto); err != nil {
		t.Fatalf("explicit auto checkpoint mount failed: %v", err)
	}
	explicitLegacyOff := base
	explicitLegacyOff.WriteBackCache = false
	explicitLegacyOff.WriteBackCacheSet = true
	if _, err := normalizeMountOptions(explicitLegacyOff); err != nil {
		t.Fatalf("explicit write-back-cache=false checkpoint mount failed: %v", err)
	}

	for _, tier := range []string{MountDurabilityInteractive, MountDurabilityFsync, MountDurabilityCloseSync, MountDurabilityWriteSync} {
		explicit := base
		explicit.Durability = tier
		explicit.DurabilitySet = true
		if _, err := normalizeMountOptions(explicit); apperr.CodeFor(err) != "fs.checkpoint_mount_read_only" {
			t.Fatalf("checkpoint %s error = %v, want fs.checkpoint_mount_read_only", tier, err)
		}
	}
}

// TestEffectiveMountModeMatchesCompanionAutoResolution pins the local mirror of
// the companion's --mode auto rule, which decides whether a tier is forwarded.
func TestEffectiveMountModeMatchesCompanionAutoResolution(t *testing.T) {
	wantAuto := "fuse"
	if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		wantAuto = "webdav"
	}
	for _, tc := range []struct{ driver, want string }{
		{driver: "", want: wantAuto},
		{driver: "auto", want: wantAuto},
		{driver: " fuse ", want: "fuse"},
		{driver: "webdav", want: "webdav"},
	} {
		if got := effectiveMountMode(tc.driver); got != tc.want {
			t.Fatalf("effectiveMountMode(%q) = %q, want %q", tc.driver, got, tc.want)
		}
	}
}

// TestDrive9MountForwardsDurability proves the selected tier reaches the
// companion argv instead of being collapsed into a boolean.
func TestDrive9MountForwardsDurability(t *testing.T) {
	for _, tier := range []string{MountDurabilityAuto, MountDurabilityInteractive, MountDurabilityFsync, MountDurabilityCloseSync, MountDurabilityWriteSync} {
		t.Run(tier, func(t *testing.T) {
			companion, recordPath := buildFakeDrive9(t)
			t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
			service := testCompanionService(t.TempDir(), companion)
			service.mountPointActive = trueMountEvidence
			mountPath := filepath.Join(t.TempDir(), "mnt")
			result, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
				Profile: dataProfile(), MountPath: mountPath, Driver: "fuse",
				Durability: tier, DurabilitySet: true,
			})
			if err != nil {
				t.Fatalf("mount failed: %v", err)
			}
			if result.Durability != tier {
				t.Fatalf("result durability = %q, want %q", result.Durability, tier)
			}
			call := requireFakeDrive9Call(t, recordPath, "mount")
			want := []string{"--durability", tier}
			for i := 0; i+1 < len(call.Args); i++ {
				if call.Args[i] == want[0] && call.Args[i+1] == want[1] {
					return
				}
			}
			t.Fatalf("mount args %#v missing %#v", call.Args, want)
		})
	}
}

// TestDrive9MountDefaultKeepsInteractiveDurability pins the default so an
// ordinary mount never silently follows the engine's auto default.
func TestDrive9MountDefaultKeepsInteractiveDurability(t *testing.T) {
	companion, recordPath := buildFakeDrive9(t)
	t.Setenv("TI_FAKE_DRIVE9_RECORD", recordPath)
	service := testCompanionService(t.TempDir(), companion)
	service.mountPointActive = trueMountEvidence
	mountPath := filepath.Join(t.TempDir(), "mnt")
	result, err := service.MountFileSystem(context.Background(), MountFileSystemOptions{
		Profile: dataProfile(), MountPath: mountPath, Driver: "fuse",
	})
	if err != nil {
		t.Fatalf("mount failed: %v", err)
	}
	if result.Durability != MountDurabilityInteractive {
		t.Fatalf("result durability = %q, want %q", result.Durability, MountDurabilityInteractive)
	}
	call := requireFakeDrive9Call(t, recordPath, "mount")
	joined := strings.Join(call.Args, " ")
	if !strings.Contains(joined, "--durability "+MountDurabilityInteractive) {
		t.Fatalf("mount args %#v missing the interactive default", call.Args)
	}
}

// TestDryRunMountDurability keeps --dry-run aligned with the live path.
func TestDryRunMountDurability(t *testing.T) {
	service := Service{}
	profile := dataProfile()
	opts := MountFileSystemOptions{Profile: profile, MountPath: filepath.Join(t.TempDir(), "mnt"), Driver: "fuse", Durability: MountDurabilityCloseSync, DurabilitySet: true}
	result, err := service.DryRunMountFileSystem(context.Background(), "ti fs mount-file-system", opts)
	if err != nil {
		t.Fatalf("dry run failed: %v", err)
	}
	if body, ok := result.Request.Body.(map[string]any); !ok || body["durability"] != MountDurabilityCloseSync {
		t.Fatalf("dry-run body = %#v", result.Request.Body)
	}
	opts.Durability = MountDurabilityWriteSync
	opts.Driver = "webdav"
	if _, err := service.DryRunMountFileSystem(context.Background(), "ti fs mount-file-system", opts); apperr.CodeFor(err) != "fs.webdav_durability_unsupported" {
		t.Fatalf("dry-run webdav error = %v, want fs.webdav_durability_unsupported", err)
	}
}
