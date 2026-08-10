# tdc fs Mount Runtime

> **Latest identity update:** `0027-remote-fs-resource-inventory.md` supersedes file system name selectors. Current mount commands select a server-assigned ID or derive it from an explicitly supplied FS token.

> **Current status:** This is the historical tdc-native mount design. `0015-drive9-companion-wrapper-for-tdc-fs.md` transferred FUSE, WebDAV, cache, write-back, drain, and unmount semantics to `tdc-drive9`; tdc now owns only command validation, resource/auth resolution, companion invocation, output/errors, and a non-secret background-mount locator. Automatic driver selection is FUSE on Linux and WebDAV on macOS and Windows; macOS users can install macFUSE and explicitly select FUSE. There is no native mount fallback.

## Goal

Allow users and agents to mount and unmount tdc fs as a local filesystem.

## User-facing Commands

Initial command set:

- `tdc fs mount-file-system`
- `tdc fs unmount-file-system`

## Behavior

- Mount lifecycle stays under `tdc fs` to keep the two-level command tree.
- Commands must not prompt.
- Mount should validate local platform prerequisites before attempting to mount.
- Mount should write enough local state to support reliable unmount and status
  diagnostics.
- FUSE is the default runtime when local prerequisites are present. WebDAV is an
  explicit compatibility fallback and an automatic fallback only when FUSE is
  unavailable and WebDAV is supported.
- FUSE should keep a bounded userspace read cache for small and medium files.
- FUSE read cache entries should be keyed by path plus known remote object
  version metadata when the data-plane stat API provides `revision` or
  `resource_id`.
- FUSE should persist pending writes locally before remote upload so failed
  flushes or interrupted mount processes do not silently lose local data.
- A new FUSE mount should recover and upload pending writes before serving the
  filesystem.
- Pending write recovery should validate mount cache identity before upload so
  explicit cache directory reuse cannot upload data into the wrong resource.
- FUSE should preserve basic open-file behavior for rename and unlink: renaming
  a path retargets matching open handles, and deleting a path marks matching
  handles deleted so closing them does not recreate the remote file.
- WebDAV fallback should support WebDAV dead properties so common desktop
  clients can store client-side metadata during the mount session.
- Unmount should be idempotent where practical and return actionable errors
  when the mount is owned by another process or cannot be found.
- Platform-specific behavior must be isolated behind build tags or small
  platform packages.

## Inputs And Config

Required inputs:

- local mount path
- resolved credentials/profile
- fs endpoint/config resolved from cloud provider and region
- stored `tdc fs` resource API key from `~/.tdc/credentials`

Optional inputs use explicit long flags:

- `--driver auto|fuse|webdav`
- `--foreground`
- `--read-only`
- `--ready-timeout <duration>`
- `--cache-dir <path>`
- `--read-cache-size-mb <n>`
- `--read-cache-max-file-mb <n>`
- `--read-cache-ttl <duration>`
- `--write-back-cache=<true|false>`

## Output And Errors

- JSON is the default for mount and unmount status.
- Human output may be concise status text for terminal use.
- Prerequisite failures must name the missing local dependency and the next
  action.
- Runtime state errors must identify the mount path.
- Permission errors must name `fs.mount` or the lower-level fs read/write
  permission that failed.

## After This Spec

Users can expose a `tdc fs` resource as a local filesystem:

```bash
tdc fs mount-file-system --file-system-name workspace --mount-path ./workspace
tdc fs unmount-file-system --mount-path ./workspace
```

Agents can use ordinary local file tools against the mount after
`tdc fs mount-file-system` succeeds. The CLI still owns mount state and unmount
recovery.

## Implementation Design

- `internal/cli/commands.go` registers `mount-file-system` and
  `unmount-file-system` with explicit long flags.
- `internal/fs/mount.go` owns mount orchestration, foreground/background
  process behavior, status checks, and structured results.
- `internal/fs/fuse_mount.go` implements the default FUSE runtime with
  `github.com/hanwen/go-fuse/v2`, mapping FUSE callbacks to the existing tdc fs
  data-plane client.
- `internal/fs/fuse_cache.go` implements the bounded TTL/LRU read cache used by
  FUSE file reads and validates cache hits against known `revision` and
  `resource_id` metadata.
- `internal/fs/fuse_writeback.go` implements local pending-write persistence and
  recovery for FUSE flush/release paths. Pending metadata stores the mount
  cache identity and the opened base object version, and recovery refuses
  entries from another identity.
- `internal/fs/fuse_version.go` defines FUSE object-version comparison for
  revision-aware read caching and best-effort stale-write detection.
- `internal/fs/webdavfs.go` maps WebDAV filesystem callbacks to the existing
  tdc fs data-plane client for explicit `--driver webdav` fallback and keeps an
  in-memory WebDAV dead-property store for compatibility with WebDAV clients.
- `internal/fs/mountstate` records active mount metadata under `~/.tdc/mounts/`
  using profile, fs resource name, mount path, remote path, driver, process id,
  endpoint, read-only mode, and started time.
- `internal/fs/mountdriver` hides platform mount helpers behind a small
  interface.
- `internal/fs/mountprocess` hides process signaling behind small platform
  files.
- `--driver auto` prefers FUSE. If FUSE prerequisites are unavailable, it falls
  back to WebDAV only when WebDAV prerequisites are available. `--driver fuse`
  and `--driver webdav` force a concrete runtime.
- FUSE is supported on macOS with macFUSE and on Linux with `/dev/fuse` plus
  `fusermount3` or `fusermount`. WebDAV fallback currently uses macOS
  `mount_webdav`/`umount`.
- Default FUSE cache state lives under `~/.tdc/cache/mounts/<mount-hash>/`.
  The hash is computed from profile, fs resource name, tenant id, endpoint,
  remote path, mount path, and a fingerprint of the fs API key. Users may
  override this with `--cache-dir`.

## API Call Chain

Mount does not introduce a new remote control API. The runtime uses the tdc fs
data-plane client from `0010-tdc-fs-data-plane.md`:

1. Resolve profile, credentials, provider, region, and fs base URL.
2. Load the stored fs resource API key.
3. Call `GET /v1/status` with `Authorization: Bearer <api-key>` before mounting
   to verify reachability and feature capabilities.
4. For FUSE, recover pending local writes from the mount cache directory by
   validating mount identity metadata and uploading matching entries with
   `PUT /v1/fs/<path>` before exposing the mount.
5. Start the selected mount runtime and map filesystem callbacks to data-plane
   calls:
   - read: `GET /v1/fs/<path>`
   - write: `PUT /v1/fs/<path>`
   - list: `GET /v1/fs/<path>?list=1`
   - stat: `GET /v1/fs/<path>?stat=1`
   - remove: `DELETE /v1/fs/<path>`
   - rename: `POST /v1/fs/<target>?rename` with `X-Dat9-Rename-Source`
   - mkdir: `POST /v1/fs/<path>?mkdir`
6. Store local mount state under `~/.tdc/mounts/`.

No reference implementation package may be imported. Mount behavior can copy
protocol concepts only.

## Dependencies And Platform

- Use `github.com/hanwen/go-fuse/v2` for the default FUSE runtime.
- Use `golang.org/x/net/webdav` for the explicit WebDAV compatibility bridge.
- The mount driver must not import code from `ref/`; copy concepts only.
- FUSE and WebDAV support are platform-specific. Keep non-mount packages pure
  Go and cross-platform.
- Do not require cgo for the core CLI.

## Dependencies

- `0010-tdc-fs-data-plane.md`.

## Extensions

- `0011-ext01-fuse-cache-and-open-handle-correctness.md`

## Acceptance Criteria

- Unit tests cover argument validation and state file handling.
- Unit tests cover FUSE read cache eviction/TTL behavior.
- Unit tests cover FUSE read cache misses on `revision` or `resource_id`
  mismatch.
- Unit tests cover FUSE pending-write persistence and recovery.
- Unit tests cover FUSE pending-write mount identity validation.
- Unit tests cover open-handle retarget/delete state for rename and unlink.
- Unit tests cover WebDAV dead-property storage.
- Platform tests cover supported mount command construction.
- Smoke tests verify mount, file read/write/list, and unmount on supported
  platforms.
- Tests cover stale state cleanup and repeated unmount attempts.

## Out Of Scope

- Layer, snapshot, rollback, and checkpoint commands or APIs. This spec only
  implements the mount runtime needed to expose tdc fs locally.
- Mount support on unsupported operating systems.
- Importing or depending on the reference implementation.
