package fs

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/fs/fscred"
	"github.com/tidbcloud/ti-cli/internal/fs/mountlocator"
	"github.com/tidbcloud/ti-cli/internal/fs/mountstate"
)

// unpackAuditLocalRoot returns the local root whose overlay the companion
// unpack writes. An explicit --local-root wins. A mount path only resolves when
// the mount recorded its local root, because the companion derives the overlay
// itself; an undeterminable overlay is an error rather than a silent skip.
func (s Service) unpackAuditLocalRoot(opts UnpackFileSystemOptions) (string, error) {
	if localRoot := strings.TrimSpace(opts.LocalRoot); localRoot != "" {
		if !filepath.IsAbs(localRoot) {
			return "", apperr.New("fs.invalid_local_root", "usage", 2, "--local-root must be an absolute path")
		}
		return filepath.Clean(localRoot), nil
	}
	mountPath := strings.TrimSpace(opts.MountPath)
	if mountPath == "" {
		return "", apperr.New("fs.missing_local_root", "usage", 2, "unpack-file-system requires --local-root or --mount-path")
	}
	homeDir, err := s.homeDir()
	if err != nil {
		return "", err
	}
	if state, _, readErr := mountstate.Read(homeDir, mountPath); readErr == nil {
		if localRoot := strings.TrimSpace(state.LocalRoot); localRoot != "" && filepath.IsAbs(localRoot) {
			return filepath.Clean(localRoot), nil
		}
	}
	return "", apperr.New("fs.unpack_unknown_local_root", "runtime", 1, fmt.Sprintf("cannot determine the local overlay root for mount %q, so the unpacked symlinks cannot be audited; pass --local-root with the local_root value reported by ti fs mount-file-system for that mount", mountPath))
}

func (s Service) drive9MountFileSystem(ctx context.Context, opts MountFileSystemOptions) (MountResult, error) {
	remotePath, err := normalizeRemotePath(defaultRemotePath(opts.RemotePath))
	if err != nil {
		return MountResult{}, err
	}
	// Security (information protection): mount-time auto/explicit unpack writes a
	// local overlay through the companion, so audit it exactly like
	// unpack-file-system does. An unpack target ti cannot resolve is refused
	// rather than silently skipped, and the resolved root is passed to the
	// companion so both sides write the same overlay.
	auditRoot, unpackPossible, err := s.mountOverlayAudit(opts, remotePath)
	if err != nil {
		return MountResult{}, err
	}
	var overlayBefore map[string]overlayEntryInfo
	if unpackPossible {
		var snapshotProblems []string
		overlayBefore, snapshotProblems, err = scanOverlayEntries(auditRoot)
		if err != nil {
			return MountResult{}, err
		}
		if len(snapshotProblems) > 0 {
			return MountResult{}, apperr.New("fs.unpack_audit_incomplete", "runtime", 1, fmt.Sprintf("cannot snapshot the overlay under %q before mounting: %s", auditRoot, strings.Join(snapshotProblems, "; ")))
		}
	}
	args := []string{"mount"}
	if opts.Driver != "" {
		args = append(args, "--mode", opts.Driver)
	}
	if opts.ReadOnly {
		args = append(args, "--read-only")
	}
	if opts.LayerRef != "" {
		capabilities := []string{drive9CapabilityLayerMount}
		if opts.CheckpointID != "" {
			capabilities = append(capabilities, drive9CapabilityCheckpointMount)
		}
		if err := s.requireDrive9Capabilities(ctx, opts.Profile, capabilities...); err != nil {
			return MountResult{}, err
		}
		appendFlagValue(&args, "--layer", opts.LayerRef)
		appendFlagValue(&args, "--checkpoint", opts.CheckpointID)
	}
	fuseRequested := strings.TrimSpace(opts.Driver) == "fuse"
	if fuseRequested {
		appendFlagValue(&args, "--cache-dir", opts.CacheDir)
		if opts.ReadCacheMB > 0 {
			args = append(args, "--cache-size", strconv.FormatInt(opts.ReadCacheMB, 10))
		}
		if opts.ReadCacheFileMB > 0 {
			args = append(args, "--read-cache-max-file-mb", strconv.FormatInt(opts.ReadCacheFileMB, 10))
		}
		if opts.ReadCacheTTL > 0 {
			args = append(args, "--read-cache-ttl", opts.ReadCacheTTL.String())
		}
		if opts.WriteBackCache {
			args = append(args, "--durability", "interactive")
		}
	}
	appendFlagValue(&args, "--profile", opts.MountProfile)
	appendFlagValue(&args, "--local-root", opts.LocalRoot)
	if opts.UnpackArchivePath != "" {
		args = append(args, "--unpack", drive9RemoteMust(opts.UnpackArchivePath))
	}
	if opts.NoAutoUnpack {
		args = append(args, "--no-auto-unpack")
	}
	args = append(args, drive9Remote(remotePath), opts.MountPath)
	result, runErr := s.drive9Run(ctx, opts.Profile, args, true)
	if runErr != nil {
		if unpackPossible {
			// Unpack is not atomic, so a partial install can already have written
			// an escaping link even though the companion reported failure.
			if auditErr := auditUnpackedOverlaySymlinks(auditRoot, overlayBefore); auditErr != nil {
				stopErr := s.stopMountAfterUnsafeAudit(ctx, opts, auditErr)
				return MountResult{}, apperr.New("fs.mount_failed_unsafe_overlay", "runtime", 1, fmt.Sprintf("%v; %v", runErr, apperr.MessageFor(stopErr)))
			}
			// The daemon can outlive its failing foreground client and keep
			// unpacking, so stop it even when the audit was clean.
			if umountErr := s.umountMount(ctx, opts); umountErr != nil {
				return MountResult{}, apperr.New(apperr.CodeFor(runErr), "runtime", 1, fmt.Sprintf("%v; the mount at %q may still be running and could not be stopped (%v) while an unpack may still be in progress; %s", runErr, opts.MountPath, umountErr, s.mountStopHint(opts)))
			}
		}
		return MountResult{}, runErr
	}
	if err := s.writeDrive9MountLocator(opts.Profile, opts.MountPath, "fs", opts); err != nil {
		// A partially completed unpack can leave an escaping link behind, and a
		// later mount would record it as pre-existing state, so audit before
		// deciding whether the failed mount can be torn down.
		if unpackPossible {
			if auditErr := auditUnpackedOverlaySymlinks(auditRoot, overlayBefore); auditErr != nil {
				stopErr := s.stopMountAfterUnsafeAudit(ctx, opts, auditErr)
				return MountResult{}, apperr.New("fs.mount_failed_unsafe_overlay", "runtime", 1, fmt.Sprintf("%v; %v", err, apperr.MessageFor(stopErr)))
			}
		}
		if umountErr := s.umountMount(ctx, opts); umountErr != nil {
			return MountResult{}, apperr.New(apperr.CodeFor(err), "runtime", 1, fmt.Sprintf("%v; the mount at %q may still be running and could not be stopped (%v) while an unpack may still be in progress; %s", err, opts.MountPath, umountErr, s.mountStopHint(opts)))
		}
		return MountResult{}, err
	}
	// The foreground mount command returns before the companion daemon finishes
	// its startup work, so the unpack may only complete during the readiness
	// wait. Once the mount path is readable, unpack is finished, which is the
	// point where the audit is meaningful; it also runs when readiness timed out
	// so a slow archive cannot slip past it.
	readyErr := s.waitForMountReady(ctx, opts.MountPath, opts.ReadyTimeout, fmt.Sprintf("; to stop it run: ti fs unmount-file-system --mount-path %q", opts.MountPath))
	if unpackPossible {
		if auditErr := auditUnpackedOverlaySymlinks(auditRoot, overlayBefore); auditErr != nil {
			return MountResult{}, s.stopMountAfterUnsafeAudit(ctx, opts, auditErr)
		}
	}
	if readyErr != nil {
		if unpackPossible {
			// Readiness never completed, so unpack may still be running; leaving
			// the mount up would leave that unpack permanently unaudited.
			return MountResult{}, s.stopMountAfterReadyTimeout(ctx, opts, readyErr)
		}
		return MountResult{}, readyErr
	}
	endpoint, _ := s.resolveFS(opts.Profile)
	driver := drive9MountedDriver(result.Stderr, opts.Driver)
	if driver == "" {
		driver = "auto"
	}
	return MountResult{Status: "mounted", Profile: profileName(opts.Profile), FileSystemName: opts.Profile.FSResourceName, MountPath: opts.MountPath, RemotePath: remotePath, Driver: driver, Endpoint: &endpoint, MountProfile: opts.MountProfile, LocalRoot: auditRoot, PackPaths: opts.PackPaths, WriteBackCache: opts.WriteBackCache, LayerRef: opts.LayerRef, CheckpointID: opts.CheckpointID, ReadOnly: opts.ReadOnly}, nil
}

// stopMountAfterUnsafeAudit tears down a mount whose overlay failed the audit. It
// keeps the locator when the unmount fails so the user can retry through ti, and
// says so in the error.
func (s Service) stopMountAfterUnsafeAudit(ctx context.Context, opts MountFileSystemOptions, auditErr error) error {
	umountErr := s.umountMount(ctx, opts)
	if umountErr != nil {
		return apperr.New(apperr.CodeFor(auditErr), "runtime", 1, fmt.Sprintf("%v; the mount at %q may still be running and could not be stopped: %v; %s", auditErr, opts.MountPath, umountErr, s.mountStopHint(opts)))
	}
	s.removeMountLocator(opts)
	return auditErr
}

// stopMountAfterReadyTimeout tears down an unpack-capable mount whose readiness
// timed out. The availability contract leaves a timed-out mount running, but an
// unreadable mount may still be unpacking, so an escaping link could be installed
// after ti returns; this is the documented exception.
func (s Service) stopMountAfterReadyTimeout(ctx context.Context, opts MountFileSystemOptions, readyErr error) error {
	umountErr := s.umountMount(ctx, opts)
	if umountErr != nil {
		return apperr.New(apperr.CodeFor(readyErr), "runtime", 1, fmt.Sprintf("the mount at %q did not become readable and could not be stopped (%v) while an unpack may still be running; %s", opts.MountPath, umountErr, s.mountStopHint(opts)))
	}
	s.removeMountLocator(opts)
	return apperr.New(apperr.CodeFor(readyErr), "runtime", 1, fmt.Sprintf("the mount at %q did not become readable and was stopped because an unpack may still be running and would otherwise stay unaudited", opts.MountPath))
}

// mountStopHint names a command that can actually stop the mount. ti's unmount
// resolves through the locator, which exists only after a successful foreground
// mount, so before that it points at the companion's own umount with the
// resource-scoped HOME.
func (s Service) mountStopHint(opts MountFileSystemOptions) string {
	if homeDir, err := s.homeDir(); err == nil {
		if _, _, locatorErr := mountlocator.Read(homeDir, opts.MountPath); locatorErr == nil {
			return fmt.Sprintf("retry ti fs unmount-file-system --mount-path %q", opts.MountPath)
		}
		if opts.Profile != nil {
			if companionHome, companionErr := fscred.CompanionHome(homeDir, opts.Profile.Name, opts.Profile.FSResourceName); companionErr == nil {
				return fmt.Sprintf("stop it with HOME=%q ti-drive9 umount %q", companionHome, opts.MountPath)
			}
		}
	}
	return fmt.Sprintf("retry ti fs unmount-file-system --mount-path %q", opts.MountPath)
}

func (s Service) umountMount(ctx context.Context, opts MountFileSystemOptions) error {
	_, err := s.drive9Run(ctx, opts.Profile, []string{"umount", opts.MountPath}, false)
	return err
}

func (s Service) removeMountLocator(opts MountFileSystemOptions) {
	if homeDir, err := s.homeDir(); err == nil {
		_ = mountlocator.Remove(homeDir, opts.MountPath)
	}
}
