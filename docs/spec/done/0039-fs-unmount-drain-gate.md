# Drain Before Unmount

## Goal

Stop `ti fs unmount-file-system` from reporting success while local write-back
writes never reached the remote. An unmount must drain pending writes first, and
either refuse to detach while work is still pending or say explicitly that the
caller accepted the loss. `ti fs drain-file-system` must show what the engine
counted instead of discarding its report.

## Problem

Drive9's write-back policy keeps a write durable in the local staging journal
and uploads it in the background after flush. That makes the client's loss
window a background-upload race, and three holes made it invisible:

1. `drive9 umount` stops the mount with a signal; the worker's SIGTERM handler
   runs `flushOpenHandles` and `chain.Drain` but discards both errors
   (`pkg/fuse/mount.go`, the shutdown goroutine), then exits with status 0
   because the stop was requested. A failed drain therefore looks like a clean
   unmount, and the pending writes are gone with the mount.
2. `drive9 umount` never asks the mount's control socket for pending work, so it
   cannot gate the detach on it.
3. `ti fs drain-file-system` ran `mount drain --json` and discarded the report,
   and `ti fs unmount-file-system` never drained at all.

Consequence: unmount returned success, the upload never happened, nothing
warned, and afterwards neither the user nor support could tell a client-side
loss from a server-side failure.

## User Outcome

```bash
# Reports the engine's drain report, including the pending counters:
ti fs drain-file-system --mount-path ./workspace

# Refuses to detach while the drain reports pending work, keeping the locator:
ti fs unmount-file-system --mount-path ./workspace

# Explicitly accepts the loss and detaches anyway:
ti fs unmount-file-system --mount-path ./workspace --force
```

A pending unmount fails with `fs.drain_pending_work`, names the engine's
`error_kind`, prints the non-zero pending counters, and shows the retry and
`--force` commands. A successful unmount reports the drain pass in its result:

```json
{
  "status": "unmounted",
  "mount_path": "./workspace",
  "drain": { "status": "drained", "response": { "ok": true, "pending": { } } }
}
```

## Command Contract

### `ti fs drain-file-system`

- The companion's `mount drain --json` report is parsed into
  `response` (same shape as `mountcontrol.DrainResponse`) instead of being
  dropped.
- `status` is `drained`. A drain that leaves write-side work fails with
  `fs.drain_pending_work`; any other drain failure uses `fs.drain_failed`. Both
  errors carry `error_kind`, the engine's error text, and the non-zero pending
  counters, plus the retry command.
- Text output prints the pending counters, so `--output text` is usable in an
  operator runbook.

### `ti fs unmount-file-system`

- Unless skipped, ti runs the same drain before `umount`. The drain uses the
  command's `--timeout` (default 30s).
- The drain applies only to ti fs FUSE mounts. ti records the driver the
  companion reported in the mount locator at mount time; WebDAV mounts keep
  their native write behavior and have no control socket, so they are not
  gated. The locator's mount kind gates too, because the engine drains only
  file-system mounts: `ti fs-vault unmount-vault` (kind `vault`) is never
  drained. A locator written before ti recorded the driver is also not gated,
  so an upgrade never blocks an existing unmount.
- A failed or pending drain blocks the unmount and keeps the locator, so the
  caller can close the writers and retry.
- `--force` skips the gate. The result records `drain.status: "skipped"` with
  the reason, so a forced unmount is still attributable.
- `--ignore-absent` keeps its cleanup meaning: a drain that fails because the
  mount is already gone does not block, but the result records
  `drain.status: "failed"` and a `drain` runtime check instead of pretending the
  drain succeeded.
- The result reports the recorded driver and the drain record. Text output adds
  the `DRAIN` column and the pending counters.
- `--dry-run` reports the gate in a `pre_unmount_drain` check without running
  the drain.

The `--force` flag previously promised to "kill the mount process" but no code
read it; it now means "accept the undrained writes".

## Scope

In scope (this repository):

- `internal/fs`: drain report parsing and rendering, the unmount gate, driver
  recording in the mount locator, dry-run checks, result types.
- `internal/cli`: `--force` help text.
- `internal/fs/mountlocator`: the non-secret `driver` field.
- README, AGENTS.md, unit tests, fake-companion e2e, and live e2e.

Out of scope (Drive9 repository, tracked there):

- `drive9 umount` reporting and gating on pending work itself, so a non-ti
  caller gets the same protection.
- The SIGTERM handler surfacing `flushOpenHandles`/`chain.Drain` failures
  instead of discarding them.
- `ti fs-vault unmount-vault`, which forwards no durability tier and is not
  gated: the engine drains only file-system mounts.

ti can only refuse to detach; it cannot make the engine's shutdown path report
its own drain failure. Until Drive9 changes that, a `kill -TERM` of the mount
process (or a supervisor restart) can still lose staged writes, and ti's gate
remains the client-side mitigation.

## Verification

- Unit tests cover: a healthy drain report in the result and text output, a
  pending drain error with counters and codes, the gate blocking and preserving
  the locator, `--force` skipping it, WebDAV and legacy-locator skips,
  `--ignore-absent` still reporting the failure, and the driver recorded at
  mount time.
- `make e2e` drives a pending drain through `ti fs drain` and `ti fs unmount`
  against the fake companion: both fail with the counters, `--force` unmounts
  and reports `skipped`, a WebDAV unmount issues no drain call, and the drain
  result carries `response.pending`.
- `make live-e2e-fs` asserts the real drain report fields on a live FUSE mount;
  the live unmount now drains first.

The English PingCAP pages under `docs/pingcap-docs/docs/` are a submodule that
was not checked out while this spec was implemented; the drain/unmount page must
document the gate and `--force` when that submodule is available.
