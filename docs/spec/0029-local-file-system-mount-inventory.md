# Local File System Mount Inventory

## Goal

Add a read-only command that reports where ordinary TiDB Cloud Filesystem resources are mounted on the current machine for the current operating-system user:

```bash
ti fs list-local-file-system-mounts
ti fs list-local-file-system-mounts --file-system-id <file-system-id>
```

The command must report both the local mount path and the remote path exposed through that mount. Its scope is deliberately local. It must not claim to list mounts created by another user, another `TI_HOME`, another machine, a container that does not share the same ti home, or an organization-wide backend inventory.

## Product Decisions

- The command name is `list-local-file-system-mounts`, not `list-file-system-mounts`, because ti has no server-side mount inventory API.
- The command lists ordinary `ti fs mount-file-system` mounts only. `ti fs-vault mount-vault` remains a separate security boundary and is not included.
- A file system may have multiple local mount paths, and every independently tracked mount is returned.
- `--file-system-id` is optional and filters the local result to one server-assigned file system ID.
- The command is read-only, does not accept `--dry-run`, and does not contact TiDB Cloud or Drive9.
- Listing local mounts does not require TiDB Cloud public/private keys, an FS token, or a network connection.
- The result is based on ti-owned locator state plus an operating-system mount check. A locator is not by itself proof that a mount is still active.
- The command never removes stale state. Cleanup remains an explicit unmount operation, including `ti fs unmount-file-system --mount-path <path> --ignore-absent` when the mount is already gone.

## User-facing Command

List all ordinary ti filesystem mounts recorded under the current `TI_HOME`:

```bash
ti fs list-local-file-system-mounts
```

Filter by file system ID:

```bash
ti fs list-local-file-system-mounts --file-system-id tnt_abc123
```

The global `--profile` flag follows a special local-inventory rule: when omitted, the command returns mounts from every profile represented in the current user's locator directory; when explicitly provided, it filters to that profile. This prevents the implicit `default` profile from hiding local mounts created through another profile while preserving an explicit profile filter for automation.

The command supports the ordinary read-only output contract:

```bash
ti fs list-local-file-system-mounts --output text
ti fs list-local-file-system-mounts --query 'mounts[?status == `mounted`].mount_path'
```

## Output Contract

JSON output has one stable envelope:

```json
{
  "mounts": [
    {
      "file_system_id": "tnt_abc123",
      "profile": "default",
      "region_code": "aws-us-east-1",
      "mount_path": "/home/user/workspace",
      "remote_path": "/projects/demo",
      "driver": "fuse",
      "status": "mounted",
      "foreground": false
    }
  ]
}
```

Fields:

- `file_system_id`: the Drive9 tenant ID exposed by ti as the public filesystem identifier.
- `profile`: the ti profile namespace that created the isolated companion context.
- `region_code`: the canonical ti placement code.
- `mount_path`: the canonical absolute local path.
- `remote_path`: the normalized remote path mounted at `mount_path`. It is omitted only for a legacy locator that cannot be enriched safely.
- `driver`: the actual recorded driver, `fuse` or `webdav`. A legacy or in-progress record may report `unknown`; do not guess from the operating system.
- `status`: `mounted`, `stale`, or `unknown`.
- `foreground`: whether ti started the companion in foreground mode.

An implementation may add an optional `pid` only when it comes from structured ti-owned process state. It must not scrape Drive9 human-readable stderr to obtain a PID, and callers must not depend on `pid` being present.

Results are sorted first by `file_system_id`, then by canonical `mount_path`. An empty inventory succeeds with:

```json
{"mounts": []}
```

Text output is a compact table containing at least file system ID, status, driver, local mount path, and remote path. It must not include API keys, FS tokens, companion home paths, state-file paths, log paths, or raw mount-helper output.

## Mount Status Semantics

Status is evaluated independently for each locator:

- `mounted`: the platform mount inspection definitively reports that the canonical local path is an active mount point.
- `stale`: the locator is valid but platform inspection definitively reports that the path is not mounted.
- `unknown`: mount state cannot be determined safely because the platform does not expose a supported check, access is denied, or the locator represents a foreground mount that has not yet become observable.

The command must not report `mounted` merely because a locator file exists or a PID appears alive. It must not treat a directory that merely exists as a mount. Platform checks must avoid cgo and use operating-system facilities or established dependencies already present in the repository:

- Linux: inspect mount information such as `/proc/self/mountinfo` with correct path unescaping and exact mount-point matching.
- macOS: inspect the mounted-filesystem table through a supported Go/syscall path or a stable system command with bounded execution and structured parsing.
- Windows: use an available mount/volume check where it can distinguish an active mount; otherwise return `unknown` rather than a false positive.

A failed inspection for one entry must not hide valid entries. The result may include non-secret warnings for unreadable or malformed locator files, but warnings must not expose file contents or companion credentials.

## Locator Schema And Lifecycle

Extend the ti-owned locator schema under `~/.ti/mounts/` from `ti.fs.mount-locator/v1` to a version that records the information needed for listing:

```json
{
  "schema": "ti.fs.mount-locator/v2",
  "profile": "default",
  "file_system_id": "tnt_abc123",
  "region_code": "aws-us-east-1",
  "companion_home": "/home/user/.ti/drive9-home/...",
  "mount_path": "/home/user/workspace",
  "remote_path": "/projects/demo",
  "driver": "fuse",
  "foreground": false,
  "kind": "fs"
}
```

`companion_home` remains internal routing state. It is required by drain and unmount but is never rendered by the list command.

Background mount lifecycle:

1. Resolve the file system ID, region, companion home, normalized remote path, and canonical local path.
2. Invoke the bundled `ti-drive9 mount` command.
3. After Drive9 confirms readiness and ti has determined the actual driver, atomically write the v2 locator.
4. If locator persistence fails, invoke the companion unmount command and return an error, preserving the existing all-or-nothing routing invariant.
5. A successful `drain-file-system` keeps the locator because the mount still exists.
6. A successful `unmount-file-system` removes the locator.

Foreground mount lifecycle:

1. Write a provisional v2 locator before entering the blocking companion command, after local validation succeeds.
2. Mark it as `foreground: true`; record an explicit requested driver or `unknown` when driver selection is automatic.
3. Keep the locator while the foreground process is running so another ti process can discover the mount.
4. Remove the locator when the foreground command exits, whether it exits normally, is cancelled, or fails startup.
5. Platform mount inspection, not the provisional record, determines whether the status is `mounted` or `unknown`.

Locator writes and removal remain atomic and owner-only. Concurrent listing must see either the previous complete record or the new complete record, never a partially written JSON file.

## Compatibility With Existing Locators

Existing v1 locators must continue to support drain and unmount and must appear in the new list command where possible. The v1 field `file_system_name` is interpreted as the selected filesystem identity used by that release; after the remote-inventory migration, values that are valid Drive9 tenant IDs map to `file_system_id`.

For a v1 locator:

- Preserve profile, region, companion home, canonical mount path, and kind.
- Do not invent a remote path or driver that the locator did not record.
- Report missing recoverable fields as absent or `unknown`.
- Do not read or depend on private Drive9 source packages, fixtures, or undocumented on-disk process-state formats to enrich the result.
- Do not rewrite the locator merely because it was listed. The next successful mount at that path writes v2.
- If a legacy value cannot be represented as a valid file system ID, keep drain/unmount compatibility but return a non-secret warning instead of silently assigning it to another remote filesystem.

This compatibility is a local-state schema migration only. It must not call remote list/get APIs and must not require that the remote filesystem still exists.

## Implementation Design

- `internal/fs/mountlocator` owns locator v1/v2 decoding, strict validation, atomic writes, canonical path identity, directory enumeration, deterministic sorting, and removal.
- `internal/fs` owns local inventory orchestration, optional file system/profile filtering, status evaluation, and public result models.
- Platform-specific files under `internal/fs` or a focused subpackage own mount-point inspection. Keep package names short and do not introduce cgo.
- `internal/cli` registers `ti fs list-local-file-system-mounts` as a read-only command and routes output through the existing JSON/text/query path.
- The existing mount, drain, and unmount handlers remain the only writers/removers of ordinary filesystem locators.
- Do not import any package from `ref/drive9` or make runtime/tests depend on `ref/`.

The locator directory may contain malformed, unsupported, or unrelated files. Enumeration must consider only the expected `*.locator.json` files, reject symlinks and non-regular files, enforce bounded file sizes, and validate that the filename matches the hash of the canonical mount path before trusting a record.

## API And Call Chain

This command adds no TiDB Cloud or Drive9 backend API request.

List flow:

1. Resolve the current ti home without loading cloud credentials or migrating unrelated profile state.
2. Enumerate ti-owned ordinary filesystem locator files under `~/.ti/mounts/`.
3. Parse and validate supported locator schemas.
4. Apply an explicitly supplied profile filter and optional `--file-system-id` filter.
5. Inspect each local mount path through the platform-specific mount checker.
6. Sort the results deterministically.
7. Apply `--query`, then render JSON or text.

Mount and unmount continue to call the public bundled Drive9 CLI. The list command must not invoke Drive9 merely to inspect local state.

## Dependencies And Platform Impact

- No new third-party dependency is expected.
- No cgo dependency is allowed.
- Linux and macOS must distinguish active and stale mounts.
- Windows must return honest `unknown` status where active mount detection cannot be implemented reliably.
- The command must work without network connectivity.
- The command must not require FUSE libraries merely to list WebDAV or stale mount records.

## Tests

Unit tests must cover:

- v2 locator round-trip, modes, atomic replacement, and deterministic enumeration;
- backward-compatible v1 reads;
- multiple local paths for one file system;
- multiple file systems and profiles;
- omitted versus explicitly supplied `--profile` behavior;
- `--file-system-id` filtering and an empty match;
- stable sorting;
- canonical path and locator filename validation;
- malformed JSON, unsupported schema, oversized files, symlinks, and non-regular files;
- no token, API key, companion home, state path, or log path in rendered output or errors;
- platform checker results for mounted, stale, and unknown states;
- background mount writes v2 only after readiness;
- failed background mount leaves no locator;
- foreground mount creates a provisional locator and removes it on every exit path;
- drain retains the locator and unmount removes it;
- list never deletes a stale locator.

Black-box `make e2e` coverage must use the fake companion to mount two local paths for one filesystem and one path for another filesystem, verify list/filter/query/text behavior, unmount one path, and verify only that path disappears.

Focused `make live-e2e-fs` coverage must mount a temporary remote path, list local mounts, verify the returned file system ID plus exact local and remote paths, drain when supported, unmount, and verify the locator is absent. The test must clean up only its own mount and remote paths.

## Documentation Updates

When implemented:

- Add the command to `README.md` and its folded all-commands inventory.
- Add one command-reference page with examples to the PingCAP Preview documentation.
- Explain that this is current-machine, current-user state and not a backend-wide answer to "where is this filesystem mounted?".
- Document stale status and explicit cleanup without advising users to delete locator files manually.

## After This Spec

A user can inspect all locally tracked mounts without remembering their mount paths:

```bash
ti fs list-local-file-system-mounts --output text
```

An agent can locate active local paths for one filesystem:

```bash
ti fs list-local-file-system-mounts \
  --file-system-id tnt_abc123 \
  --query 'mounts[?status == `mounted`].{local: mount_path, remote: remote_path}'
```

The result is sufficient for local drain/unmount orchestration but does not claim to discover mounts on other hosts.

## Acceptance Criteria

- `ti fs list-local-file-system-mounts` lists ordinary filesystem mounts recorded under the current user's ti home.
- Every new mount record includes the public file system ID, canonical local mount path, normalized remote path, region, profile, driver, and foreground mode.
- `--file-system-id` filters without requiring cloud credentials or a token.
- Omitted `--profile` lists all local profile namespaces; an explicitly supplied profile filters them.
- Active mounts are not inferred from locator existence alone.
- Stale records are clearly marked and are never removed by the list command.
- Existing v1 locators remain usable for drain/unmount and appear without fabricated fields.
- Foreground and background mount lifecycles cannot leave a locator after the owning mount command has definitively failed or exited.
- JSON, text, and `--query` behavior is deterministic and contains no secrets or internal companion paths.
- `make test`, `make e2e`, and focused live FS coverage pass.

## Out Of Scope

- Organization-wide or server-side mount inventory.
- Discovering mounts on another machine, container, user account, or ti home.
- A backend mount registration, heartbeat, lease, or last-seen API.
- Listing Vault mounts.
- Automatically pruning stale locator files.
- Terminating mount processes from the list command.
- Reading private Drive9 source packages or undocumented Drive9 state files.

## Dependencies

- `docs/spec/done/0015-drive9-companion-wrapper-for-tdc-fs.md`
- `docs/spec/done/0020-explicit-file-system-selection.md`
- `docs/spec/done/0027-ti-cli-rename-and-migration.md`
- `docs/spec/done/0028-remote-fs-resource-inventory.md`
