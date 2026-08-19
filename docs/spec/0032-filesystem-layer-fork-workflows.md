# Filesystem Layer Fork Workflows

## Goal

Complete the public TiDB Cloud Filesystem layer workflow exposed by Drive9 so users and agents can create one base overlay, fork zero-copy child timelines, inspect ancestry, mount writable or historical views, abandon rejected timelines, and commit only the selected result to the base file system.

This spec extends the existing `ti fs` layer commands. It does not add a new top-level command, implement layer semantics inside `ti`, or call Drive9's internal APIs directly.

## User Outcome

After this spec, a user can seed a workspace once and fork independent child layers without copying the workspace:

```bash
ti fs create-directory --path /research/q3-market
ti fs create-layer --base-root-path /research/q3-market --layer-name research-base --actor-id hermes --tag topic=q3-market
ti fs copy-file --from-local ./ --to-remote /research/q3-market/ --recursive --layer-id research-base
ti fs create-layer-checkpoint --layer-id research-base --checkpoint-id seed --label workspace-seed

ti fs fork-layer --parent-layer-ref research-base --layer-name style-brief --actor-id hermes-brief --checkpoint-id seed
ti fs fork-layer --parent-layer-ref research-base --layer-name style-longform --actor-id hermes-longform --checkpoint-id seed
ti fs fork-layer --parent-layer-ref research-base --layer-name style-analyst --actor-id hermes-analyst --checkpoint-id seed

ti fs list-layer-chain --layer-ref style-analyst
```

Each child is a copy-on-write timeline pinned to the selected parent tip or checkpoint. Forking does not copy the file tree. Changes remain outside the base file system until `commit-layer` succeeds.

Users can mount one writable child and one historical checkpoint view:

```bash
ti fs mount-file-system --mount-path ./runs/analyst --driver fuse --layer-ref style-analyst
ti fs mount-file-system --mount-path ./peek/v5 --driver fuse --layer-ref style-analyst --checkpoint-id v5
```

The first mount writes to the active layer. The checkpoint mount is read-only. To continue from old history, fork a new writable child from that checkpoint instead of writing through the checkpoint mount:

```bash
ti fs fork-layer --parent-layer-ref style-analyst --layer-name from-v5 --checkpoint-id v5 --actor-id hermes
ti fs mount-file-system --mount-path ./from-v5 --driver fuse --layer-ref from-v5
```

Rejected leaf timelines can be abandoned, and a selected timeline can be committed to the base file system:

```bash
ti fs delete-layer --layer-ref style-brief
ti fs delete-layer --layer-ref style-longform
ti fs commit-layer --layer-id from-v5
```

## Product Semantics

- A root layer overlays the live base file system under `base_root_path`.
- Fork pins the parent overlay at its current tip, or at one explicit parent checkpoint. It does not snapshot the live base file system.
- Parent overlay writes after the pin are invisible to the child.
- Changes committed by another layer to the live base can still become visible through base fallback. Layer fork is copy-on-write history, not a fully isolated database snapshot.
- A child writes only to its own top layer. It never mutates its pinned parent.
- A checkpoint mount is always read-only. A writable historical continuation requires `fork-layer --checkpoint-id`.
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

Checkpoint IDs are explicit opaque identifiers. There is no checkpoint-list command in the current Drive9 public CLI, so examples use stable caller-assigned checkpoint IDs.

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

Black-box `make e2e` tests must cover help/required flags, JSON/text/query behavior, dry-run behavior, config-free `TI_FS_TOKEN` selection, profile-stored token selection, aliases for mount only, and unchanged flat mount behavior.

### Live e2e

`make live-e2e-fs` adds one real lifecycle using only resources created by or explicitly selected for that test:

1. create a root layer and write seed content into its overlay;
2. checkpoint it with a stable unique ID;
3. fork two children from the same checkpoint;
4. verify both chains and pinned ancestry;
5. write different content through each child and verify isolation;
6. mount one child through FUSE and verify POSIX writes appear in that child;
7. mount the parent checkpoint read-only and verify writes fail;
8. delete one leaf and verify its state is abandoned;
9. verify deleting a parent with a live descendant fails, then validate explicit cascade on test-owned layers;
10. commit the selected child and verify content appears in the base file system;
11. clean up every test-owned mount and remaining layer without touching pre-existing layers.

If FUSE is unavailable on the CI host, non-mount fork/chain/delete tests still run and the mount portion reports an explicit platform skip. A release is not accepted solely on fake-companion tests.

## Release Gates

This spec is complete only when all of the following are true:

1. The official Drive9 release endpoint used by the ti installer publishes a companion at `99aceec4` or later with the required public commands.
2. Installer and updater tests verify the downloaded companion command surface, not only its checksum.
3. An authenticated hosted test passes fork, chain, delete, writable layer mount, and read-only checkpoint mount.
4. `make test`, `make e2e`, and `make live-e2e-fs` pass with the release companion.
5. README, AGENTS, and PingCAP command/example documentation are updated in the same product change.

As of 2026-08-19, source implementation is available, but `https://drive9.ai/releases/drive9-darwin-arm64` reports commit `dac2d626` and does not yet expose `fork|chain|delete`. Coding can proceed against the confirmed public contract, but release completion remains blocked until the companion artifact is published and hosted behavior passes authenticated live verification.

## Out Of Scope

- Implementing or copying Drive9 layer semantics into `ti`.
- Native HTTP fallback for layer operations.
- Tenant-level Drive9 fork.
- Snapshotting the live base file system at fork time.
- Layer merge, rebase, squash, commit-into-parent, or rollback-to-checkpoint.
- A local current-layer context or default layer.
- Automatic cascade deletion.
- WebDAV layer mounts.
