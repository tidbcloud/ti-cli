package fs

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/tidbcloud/ti-cli/internal/apperr"
)

// Write-durability tiers accepted by the ti-drive9 companion's
// `mount --durability`. Each tier selects the engine's sync mode and write
// policy; ti forwards one explicitly instead of letting the companion pick its
// own default, because the engine default is auto and ti has always mounted
// with interactive semantics.
const (
	// MountDurabilityAuto lets the engine measure round-trip time at mount
	// time and choose interactive or strict accordingly. It is the engine
	// default and the only tier a WebDAV mount can be described with.
	MountDurabilityAuto = "auto"
	// MountDurabilityInteractive keeps writes durable in the local staging
	// journal and uploads them asynchronously after flush.
	MountDurabilityInteractive = "interactive"
	// MountDurabilityFsync makes fsync(2) block until the remote commit
	// succeeds while ordinary writes still stage locally.
	MountDurabilityFsync = "fsync"
	// MountDurabilityCloseSync makes close(2) remote-durable by uploading from
	// Flush, so a completed close means the data left the client.
	MountDurabilityCloseSync = "close-sync"
	// MountDurabilityWriteSync makes every write remote-durable before it
	// returns success. It is the strictest and most expensive tier.
	MountDurabilityWriteSync = "write-sync"
)

// DefaultMountDurability is the tier an ordinary ti mount uses. It preserves
// the behavior ti had before the tier was selectable.
const DefaultMountDurability = MountDurabilityInteractive

// mountDurabilityValues lists the accepted tiers in increasing strictness for
// help text and error messages.
var mountDurabilityValues = []string{
	MountDurabilityAuto,
	MountDurabilityInteractive,
	MountDurabilityFsync,
	MountDurabilityCloseSync,
	MountDurabilityWriteSync,
}

// MountDurabilityHelp renders the accepted tiers for flag and error text.
func MountDurabilityHelp() string {
	return strings.Join(mountDurabilityValues, ", ")
}

func validMountDurability(value string) bool {
	for _, candidate := range mountDurabilityValues {
		if value == candidate {
			return true
		}
	}
	return false
}

// mountDurabilityUsesWriteBack reports whether a tier stages writes locally and
// leaves the remote upload to the background (drive9's writeback policy). The
// close-sync and write-sync tiers make the close or the write itself
// remote-durable instead.
func mountDurabilityUsesWriteBack(value string) bool {
	switch value {
	case MountDurabilityAuto, MountDurabilityInteractive, MountDurabilityFsync:
		return true
	default:
		return false
	}
}

// normalizeMountDurability resolves --durability together with its legacy
// --write-back-cache alias into the single tier ti forwards.
func normalizeMountDurability(opts MountFileSystemOptions) (MountFileSystemOptions, error) {
	value := strings.ToLower(strings.TrimSpace(opts.Durability))
	if value == "" {
		value = DefaultMountDurability
	}
	if !validMountDurability(value) {
		return MountFileSystemOptions{}, apperr.New("fs.invalid_mount_durability", "usage", 2, fmt.Sprintf("unsupported --durability value %q; supported values: %s", strings.TrimSpace(opts.Durability), MountDurabilityHelp()))
	}
	explicit := opts.DurabilitySet
	if opts.WriteBackCacheSet {
		legacy := MountDurabilityInteractive
		if !opts.WriteBackCache {
			legacy = MountDurabilityAuto
		}
		if opts.DurabilitySet && value != legacy {
			return MountFileSystemOptions{}, apperr.New("fs.conflicting_mount_durability", "usage", 2, fmt.Sprintf("--durability=%s conflicts with --write-back-cache=%t; pass only --durability (--write-back-cache=true means interactive and --write-back-cache=false means auto)", value, opts.WriteBackCache))
		}
		value = legacy
		explicit = true
	}
	opts.Durability = value
	opts.DurabilityExplicit = explicit
	return opts, nil
}

// rejectUnsupportedMountDurability refuses a tier the effective mount driver
// cannot honor. WebDAV mounts keep the engine's native write behavior, so ti
// forwards no tier for them and reports auto. An explicit strict tier is a
// usage error instead of a silent downgrade, so a caller that asked for
// close-sync or write-sync never believes it got it.
func rejectUnsupportedMountDurability(opts MountFileSystemOptions) (MountFileSystemOptions, error) {
	if effectiveMountMode(opts.Driver) != "webdav" {
		return opts, nil
	}
	if opts.DurabilityExplicit && opts.Durability != MountDurabilityAuto {
		return MountFileSystemOptions{}, apperr.New("fs.webdav_durability_unsupported", "usage", 2, fmt.Sprintf("--durability=%s is not supported for WebDAV mounts, which always keep their native write behavior; remove --durability or use --driver fuse where FUSE is available", opts.Durability))
	}
	opts.Durability = MountDurabilityAuto
	return opts, nil
}

// effectiveMountMode mirrors the companion's own `--mode auto` resolution
// (WebDAV on macOS and Windows, FUSE everywhere else) so ti's durability
// decision matches the mode the companion will actually mount with. It
// duplicates the companion rule on purpose: resolving it locally keeps the
// flag decision deterministic instead of depending on host prerequisites.
func effectiveMountMode(driver string) string {
	switch strings.TrimSpace(driver) {
	case "", "auto":
		if runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
			return "webdav"
		}
		return "fuse"
	case "webdav":
		return "webdav"
	default:
		return "fuse"
	}
}

// mountDurabilityForwarded reports whether ti pins the tier on the companion
// argv. It pins FUSE mounts, including automatic ones that resolve to FUSE, so
// an ordinary mount keeps the documented interactive default instead of the
// engine's auto default. Checkpoint mounts are read-only and forward nothing.
func mountDurabilityForwarded(opts MountFileSystemOptions) bool {
	return opts.CheckpointID == "" && effectiveMountMode(opts.Driver) == "fuse"
}
