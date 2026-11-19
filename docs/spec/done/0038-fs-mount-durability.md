# FUSE Mount Write Durability Selection

## Goal

Let a `ti fs mount-file-system` caller pick the write-durability tier the
ti-drive9 companion applies to a FUSE mount:

```bash
ti fs mount-file-system --file-system-id <id> --mount-path ./workspace
ti fs mount-file-system --file-system-id <id> --mount-path ./workspace --durability close-sync
ti fs mount-file-system --file-system-id <id> --mount-path ./workspace --driver fuse --durability write-sync
```

Before this spec, ti exposed only the boolean `--write-back-cache`, and only an
explicit `--driver fuse` mount forwarded anything: `true` (the default) pinned
the companion to `interactive`, and `false` forwarded nothing, so the companion
fell back to its own `auto` default. The companion has supported five tiers for
longer than ti has exposed them (`mount --durability auto|interactive|fsync|
close-sync|write-sync`), so a platform customer that needed `close-sync` or
`write-sync` had to mount around ti and drain by hand, which leaves the
client-side loss window unbounded and unobservable.

## User Outcome

A caller can state the durability it needs and get it, or get a clear error:

- `interactive` (default) keeps the previous behavior: writes are durable in the
  local staging journal and the remote upload runs in the background after
  flush.
- `close-sync` makes `close(2)` remote-durable, so a completed close means the
  data left the client.
- `write-sync` makes every `write(2)` remote-durable before it returns.
- `fsync` makes `fsync(2)` wait for the remote commit while ordinary writes keep
  the write-back policy.
- `auto` leaves the choice to the companion's round-trip measurement.

The mount result reports the effective tier, so a script can verify what it got
instead of assuming:

```json
{ "status": "mounted", "driver": "fuse", "durability": "close-sync" }
```

## Command Contract

`ti fs mount-file-system` (and its `ti fs mount` alias) gains:

- `--durability <string>`, default `interactive`, one of `auto`, `interactive`,
  `fsync`, `close-sync`, `write-sync`. The value is trimmed and case-folded
  before validation; anything else fails with `fs.invalid_mount_durability` and
  exit code 2 before the companion runs. An explicitly empty value fails with
  `fs.missing_mount_durability`, matching the CLI's rule for `--region` and
  `--profile`, instead of being silently treated as the default.

`--write-back-cache` stays as a legacy alias so existing scripts keep working:

- `--write-back-cache=true` means `--durability=interactive`.
- `--write-back-cache=false` means `--durability=auto`.
- Setting both explicitly to inconsistent values fails with
  `fs.conflicting_mount_durability` instead of silently preferring one.

### Forwarding rules

ti forwards the tier as `mount --durability <tier>` only when the mount resolves
to FUSE, mirroring the companion's own `--mode auto` rule (WebDAV on macOS and
Windows, FUSE elsewhere). That rule lives in one place,
`internal/fs/mount_durability.go`'s `companionMountMode`. It deliberately does
not use `mountdriver.Resolve`, which is prerequisite-aware (macFUSE on darwin)
and would forward a tier to a WebDAV mount the companion chose. The mount result
reconciles the reported `durability` with the driver the companion reports, so
the result never claims a tier that is not in effect:

- Explicit `--driver fuse`, and `--driver auto` on a platform whose automatic
  choice is FUSE, forward the tier. An ordinary mount therefore keeps the
  `interactive` default instead of following the companion's `auto` default.
- `--driver webdav` forwards no tier. WebDAV mounts keep the engine's native
  write behavior, so an explicit tier other than `auto` fails with
  `fs.webdav_durability_unsupported` and exit code 2. Reporting a tier that
  cannot take effect would be worse than refusing it.
- Checkpoint mounts (`--layer-ref` plus `--checkpoint-id`) are read-only: they
  force `auto`, forward nothing, and reject any other explicit tier with
  `fs.checkpoint_mount_read_only`. `--write-back-cache=true` keeps its existing
  rejection there.

The result reports the effective tier as `durability` (`auto` for WebDAV and
checkpoint mounts). It replaces the previous `write_back_cache` boolean: a
boolean cannot express five tiers, and keeping a derived boolean alongside the
tier invites it to disagree with the mount. The removed field was never
documented as a stable contract.

`--dry-run` reports the same effective tier in its request body and applies the
same validations, including the WebDAV rejection.

## Scope

In scope:

- `internal/cli` flag registration and option wiring.
- `internal/fs` tier normalization, legacy alias reconciliation, FUSE/WebDAV
  and checkpoint rules, companion argv construction, mount result, and dry-run.
- README, AGENTS.md, e2e (fake companion argv + failures), and live e2e mount
  coverage.

Out of scope:

- `ti fs-vault mount-vault`, which forwards no tier and reports `auto`.
- Any ti-native mount implementation. Mounting still belongs to the companion.
- Drive9-side behavior: ti only selects an existing engine tier.

## Verification

- Unit tests pin the tier table, the legacy alias mapping, the conflict error,
  the explicit-empty rejection, the WebDAV and checkpoint rejections, the local
  mirror of the companion's `--mode auto` rule, the forwarded argv for every
  tier, the interactive default, and the dry-run body.
- `make e2e` covers the default and a strict tier end to end through the fake
  companion, plus the invalid-value and WebDAV rejections. Mounts that assert a
  forwarded tier pin `--driver fuse`, and the automatic-mode case branches on
  the host platform, so the gate is green on macOS and Windows too.
- `make live-e2e-fs` and `make live-e2e-fs-git` mount with an explicit tier
  (`close-sync`) and assert the reported value, and the live WebDAV mount
  asserts `auto`.

The English PingCAP documentation pages under `docs/pingcap-docs/docs/` are a
submodule that was not checked out while this spec was implemented. The
published mount page must gain the `--durability` section; the tracked follow-up
is issue #98 in this repository and must land before the next release cut.
