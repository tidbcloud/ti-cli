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
  `fs.drain_pending_work`; a classified failure that reports none uses
  `fs.drain_failed`; a failure with no usable report uses
  `fs.drain_report_missing` (no stdout) or `fs.drain_report_invalid`
  (unparseable stdout). Every error carries `error_kind`, the engine's error
  text, and the non-zero pending counters when there are any, plus the remedy
  command.
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
  the reason, so a forced unmount is still attributable. It is the only way to
  accept undrained writes.
- `--ignore-absent` keeps its cleanup idempotency meaning, not loss acceptance:
  it may tolerate only a drain failure that reports **no pending work** — no
  report at all (typically a mount that is already gone, or a companion that
  predates the report), or a parsed report whose counters are all zero with a
  non-pending classification. The result records `drain.status: "failed"` plus a
  `drain` runtime check on both the `unmounted` and `absent` outcomes. The block
  condition is the **parsed pending counters**, not the engine's classification:
  a drain that hits its timeout against a write-back backlog reports a
  non-pending `error_kind` while still listing queued work, so trusting the
  classification alone would detach with exit 0 and print the counters it is
  discarding. Cleanup scripts pass `--ignore-absent` routinely, so letting it
  bypass the pending check would recreate the silent write loss this spec
  closes. `--force` remains the only loss acceptance.
- The blocked-drain error tail is built from the classification: pending work
  can only be bypassed with `--force` (and says `--ignore-absent` will not
  help), while a non-pending or report-less failure points at
  `--ignore-absent` for a mount that may be gone and at `--force` for accepting
  the loss. The gate itself has one authority, the error code: the
  `fs.drain_pending_work` classification already folds in both the engine's
  `error_kind` and the parsed counters, so the block condition cannot drift from
  the message. A drain that produced no report is a structured
  `fs.drain_report_missing` (no stdout at all) or `fs.drain_report_invalid`
  (unparseable stdout) with the same guidance, instead of a raw companion error.
- The result reports the recorded driver and the drain record. Text output adds
  the `DRAIN` column and the pending counters.
- `--dry-run` reports the gate in a `pre_unmount_drain` check without running
  the drain.

### The pending predicate

`DrainPending.HasPendingWork()` is the single data-safety predicate. It is an
explicit enumeration of the counters, never a parse of the operator-facing
summary, so rewording the text output cannot change a safety decision.

The engine's own drain-failure exit record counts
`dirty_handles + commit_queue_pending + uploader_queued` as pending files and
`commit_queue_bytes + uploader_cached_bytes` as pending bytes, so those counters
are undrained work by construction. The remaining counters are informational,
but they are non-zero only while the mount still has unfinished work, so ti
treats any non-zero counter as pending. That is a deliberate conservative
over-block: refusing to detach is recoverable, silently detaching from queued
write-back is not. If Drive9 later publishes a loss-carrying versus
informational split, tighten the predicate and this section together.

### Drain applicability

`ti fs drain-file-system` runs only for mounts the engine can drain. A non-`fs`
locator kind (for example a `vault` mount) and a WebDAV driver fail with
`fs.drain_not_applicable` and an explanation instead of forwarding a request the
companion rejects with a raw error. A locator that records no driver stays
drainable: the command itself asks for a drain, and only the unmount gate needs
the stricter driver check. The drain `--dry-run` reports the same rule in its
`drain_support` check, with `status: "failed"` when the command would refuse to
run, so the dry run predicts the execution instead of contradicting it.

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
- The e2e mount expectations pin `--driver fuse` wherever a forwarded tier is
  asserted and branch the automatic-mode case on the host platform, so the
  mandatory `make e2e` gate stays green on macOS and Windows as well as Linux.
- The `--ignore-absent` cases are covered both ways: pending work blocks even
  when the engine classifies the failure as something else (unit and e2e), and a
  drain that reports no pending work (no report, or zero counters with a
  non-pending classification) continues with `drain.status: "failed"` on both
  the `unmounted` and `absent` outcomes.
- The drain `--dry-run` check is pinned for both directions (WebDAV reports
  `failed`, and a driver-less legacy locator reports `passed` because the drain
  command still accepts it).
- Both tolerated shapes have their own fixture: a drain that produces no report
  (`fs.drain_report_missing`, tolerated) and a parsed report with a non-pending
  classification and zero counters (tolerated, `drain.status: "failed"`), plus
  the pending shape that blocks under `--ignore-absent`.

The English PingCAP pages under `docs/pingcap-docs/docs/` are a submodule that
was not checked out while this spec was implemented. The published mount and
unmount pages therefore still describe the pre-change CLI; the tracked follow-up
is issue #98 in this repository and must land before the next release cut.
