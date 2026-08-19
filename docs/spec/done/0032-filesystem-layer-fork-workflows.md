# Filesystem Layer Fork Workflows

## Goal

Complete the public TiDB Cloud Filesystem layer workflow exposed by Drive9 so users and agents can create one base overlay, fork zero-copy child timelines, inspect ancestry, mount writable or historical views, abandon rejected timelines, and commit only the selected result to the base file system.

This spec extends the existing `ti fs` layer commands. It does not add a new top-level command, implement layer semantics inside `ti`, or call Drive9's internal APIs directly.

## User Outcome

After this spec, a user can seed a workspace once and fork independent child layers without copying the workspace:

```bash
ti fs create-directory --path /research/q3-market
ti fs create-layer --base-root-path /research/q3-market --layer-name research-base --actor-id hermes --tag topic=q3-market

SEED_MOUNT="$(mktemp -d)"
ti fs mount-file-system --mount-path "$SEED_MOUNT" --remote-path /research/q3-market --driver fuse --layer-ref research-base
tar -C . -cf - . | tar -C "$SEED_MOUNT" -xf -
ti fs drain-file-system --mount-path "$SEED_MOUNT"
ti fs unmount-file-system --mount-path "$SEED_MOUNT"
rmdir "$SEED_MOUNT"

SEED="$(ti fs create-layer-checkpoint --layer-id research-base --checkpoint-id seed --label workspace-seed --query checkpoint_id --output text)"

ti fs fork-layer --parent-layer-ref research-base --layer-name style-brief --actor-id hermes-brief --checkpoint-id "$SEED"
ti fs fork-layer --parent-layer-ref research-base --layer-name style-longform --actor-id hermes-longform --checkpoint-id "$SEED"
ti fs fork-layer --parent-layer-ref research-base --layer-name style-analyst --actor-id hermes-analyst --checkpoint-id "$SEED"

ti fs list-layers
ti fs list-layer-chain --layer-ref style-analyst
```

The seed tree is written through a writable FUSE layer mount and must not publish seed files to the live base file system. `ti` does not add support for combining `copy-file --recursive` with `--layer-id`; Drive9 rejects that combination. Users who need to seed a directory tree use the mounted POSIX view, then drain and unmount it before checkpointing.

Each child is a copy-on-write timeline pinned to the selected parent tip or checkpoint. Forking does not copy the file tree. Changes remain outside the base file system until `commit-layer` succeeds.

Users can mount all three writable children over the same remote root and compare their independent results:

```bash
mkdir -p ./runs/brief ./runs/longform ./runs/analyst
ti fs mount-file-system --mount-path ./runs/brief --remote-path /research/q3-market --driver fuse --layer-ref style-brief
ti fs mount-file-system --mount-path ./runs/longform --remote-path /research/q3-market --driver fuse --layer-ref style-longform
ti fs mount-file-system --mount-path ./runs/analyst --remote-path /research/q3-market --driver fuse --layer-ref style-analyst

diff -u ./runs/brief/reports/report.md ./runs/analyst/reports/report.md
ti fs find-files --path /research/q3-market --layer-id style-brief
ti fs search-file-content --path /research/q3-market --pattern TAM --layer-id style-analyst
ti fs diff-layer --layer-id style-analyst
```

Before creating a checkpoint for writes made through a mount, drain that mount so all local dirty state has reached the remote layer. A checkpoint is a server-side boundary and cannot discover pending writes in a local mount process:

```bash
ti fs drain-file-system --mount-path ./runs/analyst
ti fs create-layer-checkpoint --layer-id style-analyst --checkpoint-id v5 --label narrative-ok
```

Users can then compare the already-mounted current tip with a historical checkpoint. Do not mount the same writable layer a second time; reuse `./runs/analyst` as the tip view. The checkpoint mount is read-only. To continue from old history, fork a new writable child from that checkpoint instead of writing through the checkpoint mount:

```bash
mkdir -p ./peek/v5 ./from-v5
ti fs mount-file-system --mount-path ./peek/v5 --remote-path /research/q3-market --driver fuse --layer-ref style-analyst --checkpoint-id v5

diff -ru ./peek/v5 ./runs/analyst

ti fs fork-layer --parent-layer-ref style-analyst --layer-name from-v5 --checkpoint-id v5 --actor-id hermes
ti fs list-layer-chain --layer-ref from-v5
ti fs mount-file-system --mount-path ./from-v5 --remote-path /research/q3-market --driver fuse --layer-ref from-v5
```

Rejected leaf timelines can be abandoned, and a selected timeline can be committed to the base file system:

```bash
ti fs unmount-file-system --mount-path ./runs/brief
ti fs unmount-file-system --mount-path ./runs/longform
ti fs delete-layer --layer-ref style-brief
ti fs delete-layer --layer-ref style-longform

ti fs drain-file-system --mount-path ./from-v5
ti fs create-layer-checkpoint --layer-id from-v5 --checkpoint-id v5b1 --label rewrite-from-v5
ti fs unmount-file-system --mount-path ./peek/v5
ti fs unmount-file-system --mount-path ./runs/analyst
ti fs rollback-layer --layer-id style-analyst
ti fs unmount-file-system --mount-path ./from-v5
ti fs commit-layer --layer-id from-v5
```

Rolling back the abandoned parent does not invalidate the active child pin. The child remains readable and writable and can commit its effective chain view to the live base file system.

## Product Semantics

- A root layer overlays the live base file system under `base_root_path`.
- Fork pins the parent overlay at its current tip, or at one explicit parent checkpoint. It does not snapshot the live base file system.
- Parent overlay writes after the pin are invisible to the child.
- Changes committed by another layer to the live base can still become visible through base fallback. Layer fork is copy-on-write history, not a fully isolated database snapshot.
- A child writes only to its own top layer. It never mutates its pinned parent.
- Recursive `copy-file` and `--layer-id` remain mutually exclusive. Directory-tree seeding uses a writable FUSE layer mount; `ti` must not implement a separate recursive layer data plane or silently fall back to the live base file system.
- A checkpoint mount is always read-only. A writable historical continuation requires `fork-layer --checkpoint-id`.
- A checkpoint includes only entries already accepted by the service. Call `drain-file-system` before checkpointing writes made through a live FUSE mount; checkpoint creation does not drain another local process.
- Do not mount one writable layer at multiple local paths concurrently. Reuse its existing mount or unmount it before mounting the same layer elsewhere. Historical checkpoint views are separate read-only mounts.
- `delete-layer` logically abandons a layer. It does not mean physical data erasure.
- Deleting a layer with live descendants fails unless `--cascade` is explicitly supplied. Cascade abandons descendants before the selected layer.
- `commit-layer` remains the only operation in this workflow that publishes the effective layer view into the live base file system.
- `rollback-layer` retains its existing Drive9 meaning. This spec does not invent rollback-to-checkpoint, merge, rebase, squash, or commit-into-parent behavior.

## Command Surface

### `ti fs fork-layer`

```text
ti fs fork-layer
    --parent-layer-ref <string>
    [--layer-id <string>]
    [--layer-name <string>]
    [--checkpoint-id <string>]
    [--actor-id <string>]
    [--dry-run]
    [global options]
```

- `--parent-layer-ref` is required and accepts any layer reference supported by the Drive9 public CLI: layer ID, unique layer name, or supported tag reference.
- `--layer-id` optionally requests a stable child ID. When omitted, the service generates one.
- `--layer-name` optionally assigns a human-readable child name.
- `--checkpoint-id` pins the child to a checkpoint owned by the parent. When omitted, the child pins the serialized parent tip.
- `--actor-id` records the child owner or agent identity.
- The command maps to `ti-drive9 fs layer fork --json [--id ...] [--name ...] [--checkpoint ...] [--actor ...] <parent-ref>`. Drive9 uses Go `flag.FlagSet`, so every companion flag must precede the positional parent reference.
- JSON output is the child layer object returned by Drive9, including ancestry fields. Text output renders the child layer ID, name, state, parent layer ID, origin sequence/checkpoint, depth, root layer ID, and base root path.
- The command requires `authz.FSFileWrite` and supports `--dry-run`.

### `ti fs list-layer-chain`

```text
ti fs list-layer-chain
    --layer-ref <string>
    [global options]
```

- `--layer-ref` is required and accepts a Drive9 layer reference.
- The command maps to `ti-drive9 fs layer chain --json <layer-ref>`. The JSON flag must precede the positional layer reference.
- JSON output is `{ "chain": [...] }`, ordered from root to the selected tip.
- Text output uses stable columns for layer ID, name, state, depth, parent layer ID, origin sequence, limit sequence, origin checkpoint ID, and base root path.
- The command requires `authz.FSFileRead`, is read-only, and rejects `--dry-run`.

### `ti fs delete-layer`

```text
ti fs delete-layer
    --layer-ref <string>
    [--cascade]
    [--dry-run]
    [global options]
```

- `--layer-ref` is required and accepts a Drive9 layer reference.
- `--cascade` is false by default. There is no confirmation-name flag and no prompt.
- The command maps to `ti-drive9 fs layer delete [--cascade] <layer-ref>`. The cascade flag must precede the positional layer reference and is added only when explicitly set.
- Because Drive9 returns only `ok`, `ti` returns its own structured result with `operation`, `layer_ref`, `status: "abandoned"`, and `cascade`.
- The command requires `authz.FSFileWrite` and supports `--dry-run`.
- A descendant conflict must remain a non-zero actionable error. `ti` must not retry with cascade automatically.

### Layer-aware mount

Extend `ti fs mount-file-system` and its `mount` alias:

```text
ti fs mount-file-system
    --mount-path <string>
    [--layer-ref <string>]
    [--checkpoint-id <string>]
    [existing mount options]
```

- `--layer-ref` maps to Drive9 `mount --layer`.
- `--checkpoint-id` maps to Drive9 `mount --checkpoint` and requires `--layer-ref`.
- Any layer-aware mount requires FUSE. An explicit `--driver webdav` fails locally. On a platform where `--driver auto` resolves to WebDAV, fail with an actionable message telling the user to install/enable FUSE and pass `--driver fuse`; do not silently switch drivers.
- A checkpoint mount is forced read-only. Supplying an explicit contradictory writable setting fails locally rather than relying on Drive9 to reinterpret it.
- The mount result adds `layer_ref`, `checkpoint_id`, and `read_only` when applicable. Mount locator state remains non-secret and records enough routing information for drain and unmount; unmount still selects the runtime only by `--mount-path`.
- Existing flat mounts are unchanged when both new flags are absent.

## Reference And Identifier Rules

Existing commands keep their current `--layer-id` flags in this spec to avoid an unrelated breaking change. New operations use `--layer-ref` only where the Drive9 contract intentionally resolves an ID, name, or tag reference. Help text must not call a reference an ID.

`ti` passes references as opaque non-empty values after rejecting control characters and path separators that could alter companion argument or API-path interpretation. It does not resolve names or tags locally, cache an ID mapping, or infer a current layer.

Layer names are references, not unique durable identifiers. Logical deletion leaves an `abandoned` layer visible, and abandoned layers still participate in name resolution. The fixed names in examples are for readability; automation must capture and use returned layer IDs or generate run-unique names so a repeated workflow cannot become ambiguous.

Checkpoint IDs are explicit opaque identifiers and are unique within one File System, not merely within one layer. There is no checkpoint-list command in the current Drive9 public CLI, so examples use readable caller-assigned IDs. Tests and repeatable automation must namespace those IDs per run and retain them with their owning layer ID.

## Output Models

Extend the local Drive9-compatible layer DTOs with fields already returned by the public companion:

- `parent_layer_id`
- `origin_seq`
- `origin_checkpoint_id`
- `root_layer_id`
- `depth`
- `origin`

Add:

- `ForkLayerOptions` and the existing `LayerResult` child response;
- `ListLayerChainOptions`, `LayerChainResult`, and `LayerChainFrame`;
- `DeleteLayerOptions` and `DeleteLayerResult`;
- layer/checkpoint/read-only fields on `MountResult`.

All structured results support JSON/text rendering and JMESPath `--query` through the existing output pipeline. No service package prints directly to stdout.

## Implementation Design

### CLI wiring

`internal/cli/commands.go` registers the three commands under `ti fs`, parses long flags, declares exactly one permission per command, and routes normal and dry-run execution through the shared command path. The command tree remains two levels.

The same file extends mount option parsing and local validation. Unix-style aliases are not added for layer lifecycle commands.

### Filesystem service

`internal/fs/layer.go` owns option/result types and delegates normal execution to companion adapter methods:

- `drive9ForkLayer`
- `drive9ListLayerChain`
- `drive9DeleteLayer`

`internal/fs/drive9_companion.go` constructs arguments, invokes the isolated per-resource companion runtime, decodes JSON for fork/chain, and converts delete's `ok` into a structured `ti` result. It must not import or execute code under `ref/drive9`.

Mount argument construction appends `--layer` and `--checkpoint` before remote/local positional arguments. The existing runner continues to sanitize inherited `DRIVE9_*` variables and supplies the selected FS token, endpoint, region, and isolated Drive9 home.

### Resource and credential selection

The new commands use the same explicit Filesystem selection and token precedence as other data-plane commands:

1. explicit `--file-system-id` or `TI_FS_FILE_SYSTEM_ID`;
2. ID embedded in an explicit `--fs-token` or `TI_FS_TOKEN`;
3. otherwise fail before starting the companion.

When an ID is selected without an explicit token, the corresponding profile-scoped local credential may supply the token. No default Filesystem is introduced.

### Dry run

`fork-layer` and `delete-layer` support dry run without invoking a companion mutation. Dry run validates profile/environment selection, Filesystem ID/token agreement, endpoint resolution, companion availability/capability, layer/checkpoint input, and permission declaration. Its request summary describes the equivalent public Drive9 operation without including FS tokens or local paths.

Mount dry run includes the selected driver, layer ref, checkpoint ID, and effective read-only state. It never starts a mount process.

## Companion And Backend Contract

The implementation depends on public Drive9 behavior introduced by `mem9-ai/drive9` commit `99aceec47c949c1b6f74233109cbdf8e10fb9d56` or a later compatible release:

- `drive9 fs layer fork`
- `drive9 fs layer chain`
- `drive9 fs layer delete`
- `drive9 mount --layer`
- `drive9 mount --checkpoint`

Corresponding hosted endpoints are:

- `POST /v1/layers/{parentRef}/fork`
- `GET /v1/layers/{layerRef}/chain`
- `DELETE /v1/layers/{layerRef}?cascade=true|false`

The companion remains the production integration boundary. Endpoint paths are documented for contract verification and dry-run descriptions; `ti` must not add a native HTTP fallback.

At startup, affected commands must detect an older companion and fail with an actionable incompatibility error rather than surfacing `unknown fs layer command`, silently changing behavior, or attempting internal endpoints. Capability detection can use shared companion metadata plus a command-surface probe and should be cached only for the current process.

## Dependencies And Portability

- No new Go module dependency is required.
- No cgo dependency is added.
- Fork, chain, and delete are available on every platform supported by the companion.
- Layer mounts require FUSE. Flat WebDAV mounting remains available but cannot expose a layer or checkpoint view.
- The release archive continues to contain `ti` and `ti-drive9`. `ref/drive9` remains excluded from build, tests, packaging, and runtime.

## Tests

### Unit tests

Fake-companion tests must verify exact argument order and output decoding for:

- tip fork with a generated child ID;
- checkpoint fork with explicit child ID/name/actor;
- root-to-tip chain JSON and text output;
- leaf delete;
- cascade delete;
- descendant conflict propagation;
- layer mount and checkpoint mount argument mapping;
- checkpoint requires layer;
- checkpoint forces read-only;
- WebDAV rejects layer/checkpoint locally;
- old companion capability failure;
- secrets never appear in results, errors, logs, or dry-run output.
- `copy-file --recursive --layer-id` is rejected locally with an actionable message directing users to a writable FUSE layer mount.

Black-box `make e2e` tests must cover help/required flags, JSON/text/query behavior, dry-run behavior, config-free `TI_FS_TOKEN` selection, profile-stored token selection, aliases for mount only, and unchanged flat mount behavior.

### Live e2e

`make live-e2e-fs` adds one real lifecycle using only resources created by or explicitly selected for that test:

1. create an empty live base root and a root layer over it;
2. mount the root layer through FUSE, copy a local tree through the POSIX view, drain and unmount it, and verify the files remain absent from the live base;
3. checkpoint it with a stable unique ID and fork three children from that checkpoint;
4. verify all three chains have the same pinned ancestry and that later parent writes are invisible;
5. mount all three children through FUSE at the same explicit remote root, write different reports, drain the mounts, and verify isolation through POSIX reads, layer-aware find/grep, and layer diff;
6. delete two leaf children, verify each remains queryable with state `abandoned`, and verify layer listing exposes their state;
7. continue writing through the selected child, drain before each stable checkpoint, and create at least three checkpoints including `v5` and a later tip;
8. keep one writable mount for the selected child tip, mount `v5` read-only at the same remote root, verify the historical view differs and rejects writes, and never create a second writable mount for the same layer;
9. fork `from-v5`, verify its root-to-tip chain, mount it writable, write and drain new content, and checkpoint it;
10. unmount the old selected parent, roll it back while `from-v5` remains active, and verify the child can still read its pinned parent state and accept a new write;
11. verify deleting a parent with a live descendant fails, then validate explicit cascade separately on test-owned layers;
12. drain and unmount `from-v5`, commit it, and verify only the selected effective result appears in the live base file system;
13. clean up every test-owned mount and remaining layer without touching pre-existing layers.

If FUSE is unavailable on the CI host, non-mount fork/chain/delete tests still run and the mount portion reports an explicit platform skip. A release is not accepted solely on fake-companion tests.

## Release Gates

This spec is complete only when all of the following are true:

1. The official Drive9 release endpoint used by the ti installer publishes a compatible companion with fork, chain, delete, and layer/checkpoint mount support.
2. Installer and updater tests verify the downloaded companion command surface, not only its checksum.
3. An authenticated hosted test passes FUSE-mounted layer seed, fork, chain, delete, writable layer mount, read-only checkpoint mount, parent rollback with a live pinned child, and child commit.
4. `make test`, `make e2e`, and `make live-e2e-fs` pass with the release companion.
5. README, AGENTS, and PingCAP command/example documentation are updated in the same product change.

As of 2026-08-19, the official artifact at `https://drive9.ai/releases/drive9-darwin-arm64` reports Drive9 commit `99aceec47c949c1b6f74233109cbdf8e10fb9d56` and exposes fork, chain, delete, and layer/checkpoint mount. It explicitly rejects recursive copy combined with `--layer`, which this spec intentionally leaves unsupported. The ti installer and updater execute the staged companion before replacing any installed binary and reject an artifact that does not expose the complete required surface.

An authenticated `make live-e2e-fs` run against `aws-ap-southeast-1`, forced to use that downloaded official artifact, passed the complete layer workflow without skips or client-side fallback semantics. It created and FUSE-seeded a root layer, checkpointed and forked three isolated children, inspected pinned ancestry, abandoned rejected leaves, compared writable tip and read-only historical mounts, forked from an older checkpoint, validated descendant deletion safeguards, rolled back a parent with a live pinned child, and committed only the selected child to the base file system. The same run also passed remote inventory, command surface, token lifecycle, data-plane, ordinary FUSE mount, configuration-free access, and WebDAV coverage. `make test`, `make e2e`, `go vet ./...`, installer shell validation, and repository diff checks pass.

## Out Of Scope

- Implementing or copying Drive9 layer semantics into `ti`.
- Supporting `copy-file --recursive` together with `--layer-id`; seed directory trees through a writable FUSE layer mount instead.
- Native HTTP fallback for layer operations.
- Tenant-level Drive9 fork.
- Snapshotting the live base file system at fork time.
- Layer merge, rebase, squash, commit-into-parent, or rollback-to-checkpoint.
- A local current-layer context or default layer.
- Automatic cascade deletion.
- WebDAV layer mounts.
