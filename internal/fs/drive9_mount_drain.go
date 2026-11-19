package fs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/tidbcloud/ti-cli/internal/apperr"
	"github.com/tidbcloud/ti-cli/internal/config"
	"github.com/tidbcloud/ti-cli/internal/fs/mountcontrol"
	"github.com/tidbcloud/ti-cli/internal/fs/mountlocator"
)

// decodeDrive9DrainResponse parses the `mount drain --json` report. The
// companion prints the report before it exits, including when the drain failed,
// so a caller can report the undrained work instead of only an exit status.
func decodeDrive9DrainResponse(stdout []byte) (*mountcontrol.DrainResponse, error) {
	trimmed := bytes.TrimSpace(stdout)
	if len(trimmed) == 0 {
		return nil, apperr.New("fs.drain_report_missing", "runtime", 1, "the ti fs companion printed no drain report; update ti-drive9 so `mount drain --json` reports pending work")
	}
	var response mountcontrol.DrainResponse
	if err := json.Unmarshal(trimmed, &response); err != nil {
		return nil, apperr.Wrap("fs.drain_report_invalid", "runtime", 1, "decode the ti-drive9 drain report", err)
	}
	return &response, nil
}

// drainPendingSummary renders the undrained counters the engine reports, so an
// operator can tell "nothing was pending" from "the upload of eight files never
// finished" without reading the raw JSON.
func drainPendingSummary(pending mountcontrol.DrainPending) string {
	counters := []struct {
		name  string
		value int64
	}{
		{"open_handles", int64(pending.OpenHandles)},
		{"dirty_handles", int64(pending.DirtyHandles)},
		{"commit_queue_pending", int64(pending.CommitQueuePending)},
		{"commit_queue_bytes", pending.CommitQueueBytes},
		{"commit_queue_in_flight", int64(pending.CommitQueueInFlight)},
		{"commit_queue_delayed", int64(pending.CommitQueueDelayed)},
		{"commit_queue_conflicts", int64(pending.CommitQueueConflicts)},
		{"uploader_queued", int64(pending.UploaderQueued)},
		{"uploader_in_flight", int64(pending.UploaderInFlight)},
		{"uploader_cached", int64(pending.UploaderCached)},
		{"uploader_cached_bytes", pending.UploaderCachedBytes},
	}
	rendered := make([]string, 0, len(counters))
	for _, counter := range counters {
		if counter.value == 0 {
			continue
		}
		rendered = append(rendered, fmt.Sprintf("%s=%d", counter.name, counter.value))
	}
	if !pending.HasPendingWork() {
		return "none"
	}
	return strings.Join(rendered, " ")
}

// describeDrainFailure renders the engine's drain report for a user-facing
// error: what failed, which path, and what work is still undrained.
func describeDrainFailure(response *mountcontrol.DrainResponse) string {
	if response == nil {
		return "the companion reported no drain detail"
	}
	parts := make([]string, 0, 5)
	if response.MountPoint != "" {
		parts = append(parts, fmt.Sprintf("mount %q", response.MountPoint))
	}
	if response.ErrorKind != "" {
		parts = append(parts, "error_kind="+response.ErrorKind)
	}
	if response.Error != "" {
		parts = append(parts, "error="+response.Error)
	}
	parts = append(parts, "pending: "+drainPendingSummary(response.Pending))
	return strings.Join(parts, "; ")
}

// drainErrorCode classifies a drain failure. Undrained write-back work is the
// dangerous case (the data is still only local), so it gets its own code.
func drainErrorCode(response *mountcontrol.DrainResponse) string {
	if response != nil && (response.ErrorKind == "pending_work_remaining" || response.Pending.HasPendingWork()) {
		return "fs.drain_pending_work"
	}
	return "fs.drain_failed"
}

// mountKindFileSystem marks a ti fs mount in the mount locator. Vault mounts
// record their own kind and are never drained: the engine drains only FUSE
// file-system mounts, and a vault mount has its own control plane.
const mountKindFileSystem = "fs"

// mountDriverSupportsDrain reports whether a recorded mount driver can be
// drained. A FUSE mount exposes a control socket; WebDAV keeps the engine's
// native write behavior and has no drain. An unknown driver (a locator written
// before the driver was recorded) is not gated, so an upgrade never blocks an
// existing mount.
func mountDriverSupportsDrain(driver string) bool {
	switch strings.TrimSpace(driver) {
	case "":
		return false
	case "webdav":
		return false
	case "fuse":
		return true
	default:
		return companionMountMode(driver) == "fuse"
	}
}

// mountDrainGate reports whether an unmount of this locator should drain first,
// and why it will not. Only a ti fs FUSE mount has a drain gate.
func mountDrainGate(locator mountlocator.Locator, force bool) (bool, string) {
	if force {
		return false, "unmount --force skips the pre-unmount drain"
	}
	switch strings.TrimSpace(locator.Kind) {
	case mountKindFileSystem:
	case "":
		return false, "the mount locator records no mount kind, so ti cannot confirm the mount supports drain"
	default:
		return false, fmt.Sprintf("%s mounts have no drain gate; the companion stops them as they are", strings.TrimSpace(locator.Kind))
	}
	if !mountDriverSupportsDrain(locator.Driver) {
		return false, mountDrainSkipReason(locator.Driver)
	}
	return true, ""
}

// drainApplicability reports whether `ti fs drain-file-system` can run at all.
// It rejects the mounts whose dry-run already says "not applicable" so the
// command fails with the same explanation instead of a raw companion error. An
// unrecorded driver stays drainable: the command itself asks for a drain, and
// only the unmount gate needs the stricter driver check.
func drainApplicability(locator mountlocator.Locator) (bool, string) {
	if kind := strings.TrimSpace(locator.Kind); kind != "" && kind != mountKindFileSystem {
		return false, fmt.Sprintf("%s mounts have no drain; the engine drains only file-system mounts", kind)
	}
	if strings.TrimSpace(locator.Driver) == "webdav" {
		return false, mountDrainSkipReason(locator.Driver)
	}
	return true, ""
}

// mountDrainSkipReason explains why a driver cannot be drained.
func mountDrainSkipReason(driver string) string {
	if strings.TrimSpace(driver) == "" {
		return "the mount locator records no driver, so ti cannot confirm the mount supports drain"
	}
	return fmt.Sprintf("%s mounts have no mount control socket to drain; the companion flushes them when it unmounts", strings.TrimSpace(driver))
}

// drainRemedy builds the actionable tail of a drain error from its
// classification. Pending work can only be skipped with --force; a failure that
// produced no report is typically a mount that is already gone, which
// --ignore-absent tolerates on unmount.
func drainRemedy(code, mountPath string) string {
	if code == "fs.drain_pending_work" {
		return fmt.Sprintf("close the writers or let the background upload finish, then retry ti fs drain-file-system --mount-path %q; --force is the only way past this on unmount (--ignore-absent does not skip this check)", mountPath)
	}
	return fmt.Sprintf("if the mount is already gone, ti fs unmount-file-system --mount-path %q --ignore-absent continues; --force accepts losing any undrained writes", mountPath)
}

// drainMount runs the companion's drain and reports its result. It returns the
// drain record even when the drain failed, so a caller that continues anyway
// (unmount --force or --ignore-absent) can say what was left undrained.
func (s Service) drainMount(ctx context.Context, profile *config.Profile, mountPath string, timeout time.Duration) (MountDrain, error) {
	args := []string{"mount", "drain", "--json"}
	if timeout > 0 {
		args = append(args, "--timeout", timeout.String())
	}
	args = append(args, mountPath)
	result, runErr := s.drive9Run(ctx, profile, args, true)
	response, decodeErr := decodeDrive9DrainResponse(result.Stdout)
	if decodeErr != nil {
		// No usable report: the mount is most likely gone, or the companion
		// predates the report. That is a drain failure, not pending work, so
		// the error has to carry the same unmount escape hatches as a classified
		// failure instead of surfacing a raw companion error.
		record := MountDrain{Status: "failed", Message: apperr.MessageFor(decodeErr)}
		reason := apperr.MessageFor(decodeErr)
		if runErr != nil {
			reason = fmt.Sprintf("%v", runErr)
		}
		// Keep the report-shaped code: "no report" and "unparseable report"
		// tell an operator different things (a gone mount or an old companion
		// versus a broken companion), and both are tolerated by
		// --ignore-absent because neither shows pending work.
		code := apperr.CodeFor(decodeErr)
		return record, apperr.New(code, "runtime", 1, fmt.Sprintf("drain %s produced no usable report: %s; %s", mountPath, reason, drainRemedy(code, mountPath)))
	}
	if runErr != nil || !response.OK {
		code := drainErrorCode(response)
		record := MountDrain{Status: "failed", Message: describeDrainFailure(response), Response: response}
		return record, apperr.New(code, "runtime", 1, fmt.Sprintf("drain %s did not finish: %s; %s", mountPath, describeDrainFailure(response), drainRemedy(code, mountPath)))
	}
	return MountDrain{Status: "drained", Response: response}, nil
}

// drainBeforeUnmount is the unmount gate: an ordinary unmount drains first and
// refuses to detach while local writes are still pending. --force skips the
// gate explicitly, and a mount that cannot be drained (WebDAV, or a locator
// written before ti recorded the driver) is not gated.
func (s Service) drainBeforeUnmount(ctx context.Context, profile *config.Profile, locator mountlocator.Locator, opts UnmountFileSystemOptions) (MountDrain, error) {
	if drainable, reason := mountDrainGate(locator, opts.Force); !drainable {
		return MountDrain{Status: "skipped", Message: reason}, nil
	}
	return s.drainMount(ctx, profile, opts.MountPath, drainTimeout(opts.Timeout))
}
