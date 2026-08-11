# Profile FS Resource Registry

> **Latest identity update:** `0028-remote-fs-resource-inventory.md` supersedes this name-keyed inventory. The old registry is retained only as rollback-safe migration input; new local credentials are keyed by server-assigned ID.

This spec supersedes the 1:1 profile storage and flat `fs_*` credential rules in completed specs 0009 and 0015.

The persistent default-resource and unique-resource fallback rules in this completed spec are superseded by `docs/spec/done/0020-explicit-file-system-selection.md`. The 1:N product relationship remains valid, but the name-keyed inventory and credential layout are superseded by `0028-remote-fs-resource-inventory.md`.

## Goal

Allow one tdc profile to manage multiple tdc fs resources. A profile represents TiDB Cloud identity and default placement, while a file system resource represents a workspace. The user should not need to create one tdc profile per workspace.

The new model is:

```text
tdc profile 1:N tdc fs resources
```

tdc remains the source of truth for resource selection. Drive9 companion contexts are an implementation detail and must not decide which tdc fs resource a command uses.

## Current Problem

The current implementation is effectively 1:1:

```toml
# ~/.tdc/config
[default]
region_code = "aws-us-east-1"
fs_resource_name = "workspace"
fs_tenant_id = "..."
fs_cloud_provider = "aws"
fs_region_code = "aws-us-east-1"

# ~/.tdc/credentials
[default]
fs_api_key = "drive9_..."
```

`config.Profile` contains only one `FSResourceName`, one `FSTenantID`, and one `FSAPIKey`. `tdc fs create-file-system` rejects creating another resource with a different name in the same profile. This forces users to create extra profiles for unrelated workspaces, which mixes identity selection with workspace selection.

Drive9 also writes a context file under the isolated companion home:

```text
~/.tdc/drive9-home/.drive9/config
```

That file is created by `tdc-drive9 create`, because Drive9's own `create` command registers the provisioned API key as a local context. In tdc this file is not user-facing configuration. It can contain `current_context`, `contexts`, server URLs, and API keys, but tdc must not rely on `current_context` for deterministic resource selection.

## User-Facing Behavior

Users can create multiple file systems under the same profile:

```bash
tdc fs create-file-system --file-system-name workspace
tdc fs create-file-system --file-system-name scratch
tdc fs create-file-system --file-system-name demo
tdc fs list-file-systems
tdc fs set-default-file-system --file-system-name workspace
tdc fs list-files --file-system-name scratch --path /
```

Commands that operate on a tdc fs resource select one resource by this precedence:

1. `--file-system-name <name>`
2. `TDC_FS_FILE_SYSTEM_NAME`
3. `fs_default_file_system_name` in the active profile
4. The only configured resource under the active profile
5. If multiple resources exist and none is selected, fail with a usage error that lists available names

Example ambiguous selection error:

```text
tdc [ERROR]: multiple tdc fs resources are configured for profile "default"; pass --file-system-name or run tdc fs set-default-file-system --file-system-name <name>. Available: workspace, scratch, demo
```

If no resources exist:

```text
tdc [ERROR]: tdc fs is not configured for profile "default"; run `tdc fs create-file-system --file-system-name <name>` first
```

`tdc fs create-file-system` behavior:

- `--file-system-name` remains required.
- If the selected profile has no fs resources, the created resource becomes the default.
- If the selected profile already has resources, the new resource is stored but does not become default unless `--set-default` is passed.
- Re-running with the same name is idempotent when the resource already exists locally and has complete credentials.
- Re-running with a name that exists locally but incomplete credentials returns an actionable repair error.
- `--wait` is optional. It waits up to ten minutes for the selected
  resource root to become readable through the public Drive9 CLI and returns
  `status: "ready"`; wait failure preserves the registry entry and credential
  file.

`tdc fs delete-file-system` behavior:

- Deletes only the named resource.
- Removes only that resource's local registry entry and credential file.
- Reports the accepted asynchronous remote deletion state as `deleting`.
- If the deleted resource was the default, clear `fs_default_file_system_name`. If exactly one resource remains, tdc may set that remaining resource as default; otherwise leave default empty and force explicit selection.
- Does not delete or mutate any other resource under the profile.

## Commands

New or changed commands:

```bash
tdc fs create-file-system --file-system-name <name> [--set-default] [--wait] [--dry-run]
tdc fs delete-file-system --file-system-name <name> [--dry-run]
tdc fs list-file-systems
tdc fs describe-file-system --file-system-name <name>
tdc fs check-file-system [--file-system-name <name>]
tdc fs set-default-file-system --file-system-name <name>
tdc fs unset-default-file-system
```

All existing fs data-plane and runtime commands accept optional `--file-system-name`:

```bash
tdc fs copy-file --file-system-name <name> ...
tdc fs read-file --file-system-name <name> ...
tdc fs list-files --file-system-name <name> ...
tdc fs mount-file-system --file-system-name <name> --mount-path <path>
tdc fs drain-file-system --file-system-name <name> --mount-path <path>
tdc fs unmount-file-system --file-system-name <name> --mount-path <path>
tdc fs-git ... --file-system-name <name>
tdc fs-journal ... --file-system-name <name>
tdc fs-vault ... --file-system-name <name>
```

`--file-system-name` is optional for commands that can resolve a default resource. It remains required for destructive resource deletion because the user should be explicit about which resource is being deleted.

## Local State Layout

Keep TiDB Cloud identity credentials in the main files:

```toml
# ~/.tdc/config
[default]
region_code = "aws-us-east-1"
fs_default_file_system_name = "workspace"

# ~/.tdc/credentials
[default]
tdc_public_key = "..."
tdc_private_key = "..."
```

Store fs resources in profile-scoped resource directories:

```text
~/.tdc/fs_resources/<profile-key>/<resource-key>/config
~/.tdc/fs_resources/<profile-key>/<resource-key>/credentials
```

`<profile-key>` and `<resource-key>` are URL-safe base64 encodings without padding. The file system name must still be stored inside the config file and must be used for user-facing output. Do not trust path segments as names without decoding and validation.

Example resource config:

```toml
# ~/.tdc/fs_resources/ZGVmYXVsdA/d29ya3NwYWNl/config
file_system_name = "workspace"
tenant_id = "6c1260bb-1b02-46b6-953e-08c325de821e"
cloud_provider = "aws"
region_code = "aws-us-east-1"
created_at = "2026-07-16T09:30:00Z"
```

Example resource credentials:

```toml
# ~/.tdc/fs_resources/ZGVmYXVsdA/d29ya3NwYWNl/credentials
api_key = "drive9_..."
```

File modes:

- resource config files: `0644`
- resource credentials files: `0600`
- `~/.tdc/fs_resources` and children: `0700`

Do not store Drive9 server URLs in the tdc fs registry. tdc resolves the server from the active resource's `cloud_provider` and canonical `region_code` through the existing fs endpoint resolver and manifest. Drive9's own isolated config may contain a server URL because Drive9 writes it, but tdc must treat that file as companion-local cache.

## Drive9 Companion Integration

tdc must not use a shared Drive9 companion HOME for all resources. Shared HOME causes context name collisions and a global `current_context` that can point at the wrong resource.

Use a resource-scoped companion home:

```text
~/.tdc/drive9-home/<profile-key>/<resource-key>
```

For every Drive9 invocation, tdc resolves one selected resource and invokes `tdc-drive9` with:

```text
HOME=~/.tdc/drive9-home/<profile-key>/<resource-key>
DRIVE9_API_KEY=<selected resource api_key>
DRIVE9_REGION_CODE=<selected resource region_code>
DRIVE9_SERVER=<resolved selected resource endpoint>
```

For `tdc fs create-file-system`, tdc invokes Drive9 with TiDB Cloud keys and the selected placement:

```text
HOME=~/.tdc/drive9-home/<profile-key>/<new-resource-key>
DRIVE9_PUBLIC_KEY=<tdc public key>
DRIVE9_PRIVATE_KEY=<tdc private key>
DRIVE9_REGION_CODE=<profile placement region code>
DRIVE9_SERVER=<resolved profile fs endpoint>
```

The companion may write:

```text
~/.tdc/drive9-home/<profile-key>/<resource-key>/.drive9/config
```

That is acceptable. tdc must not call `drive9 ctx use` to switch resources and must not read Drive9 `current_context` to choose resources. Resource selection is owned by tdc.

When calling Drive9 commands that support context-qualified remote paths, tdc should still prefer environment-selected current credentials and the ordinary `:/path` form. Do not expose Drive9 context names as tdc file system selectors.

## Migration

Support migration from the current flat 1:1 layout:

```toml
[default]
fs_resource_name = "workspace"
fs_tenant_id = "..."
fs_cloud_provider = "aws"
fs_region_code = "aws-us-east-1"

[default] # in ~/.tdc/credentials
fs_api_key = "drive9_..."
```

Migration rules:

- On the first non-dry-run fs command, if complete flat fs fields are present and the new registry does not already contain that resource, synthesize a registry resource from the flat fields. Profile loading itself remains read-only.
- After successful migration, clear the flat fs fields from `~/.tdc/config` and `~/.tdc/credentials`.
- Set `fs_default_file_system_name` to the migrated resource if no default exists.
- Do not trust `~/.tdc/drive9-home/.drive9/config` as migration source because it is Drive9-owned cache and may be stale.
- A legacy resource is complete only when its name, tenant ID, canonical fs region code, and API key are all present. Missing fields fail with `fs.resource_credentials_incomplete`; do not infer a missing resource region from the profile's current placement.
- If a registry entry with the same name has a different tenant ID or API key, fail with `fs.resource_migration_failed` and leave both sources unchanged.
- Dry-run commands validate legacy state in memory but never migrate, clear, or create local files.

Because this project is still in preview, migration can be simple and deterministic. Do not add interactive prompts.

## Package Design

- `internal/config/store` owns the main profile's `fs_default_file_system_name` and atomic cleanup of legacy flat fields. It does not own per-resource secrets.
- `internal/config` loads the profile default plus legacy flat fields as migration input. A selected copy of `config.Profile` carries one resolved resource snapshot to existing service methods.
- `internal/fs/fscred` owns registry `List`, `Get`, `Resolve`, `ResolveDryRun`, `Store`, `Delete`, `SetDefault`, legacy migration, path encoding, and resource-scoped companion HOME construction.
- `internal/fs` exposes list, describe, default management, create/delete integration, and passes the selected profile snapshot into existing Drive9-backed use cases.
- `internal/fswrap` derives HOME from the selected profile and resource and constructs a sanitized Drive9 environment.
- `internal/cli` adds `--file-system-name` consistently to fs, fs-git, fs-journal, and fs-vault operational commands and resolves the resource before invoking a use case.

Do not import or depend on `ref/drive9`. The Drive9 companion remains an external binary invoked through `internal/fswrap`.

## Dependencies And Platform

- No new Go module is required. Registry TOML uses the existing `github.com/pelletier/go-toml/v2` dependency, command wiring uses the existing Cobra dependency, and path encoding and file operations use the Go standard library.
- The feature does not use CGO and does not change tdc's cross-compilation model.
- Linux and macOS enforce generated registry directory mode `0700`, config mode `0644`, and credentials mode `0600`. Windows uses the same logical file split and path encoding, while POSIX mode bits are not treated as an access-control guarantee.
- Drive9 remains a separately installed companion artifact. This feature changes its HOME and environment selection only; it does not link Drive9 code into tdc or add a build-time dependency on `ref/`.

## Output

`tdc fs list-file-systems` JSON:

```json
{
  "profile": "default",
  "default_file_system_name": "workspace",
  "file_systems": [
    {
      "file_system_name": "workspace",
      "tenant_id": "6c1260bb-1b02-46b6-953e-08c325de821e",
      "cloud_provider": "aws",
      "region_code": "aws-us-east-1",
      "has_api_key": true,
      "is_default": true
    }
  ]
}
```

`tdc fs describe-file-system --file-system-name workspace` JSON:

```json
{
  "profile": "default",
  "file_system_name": "workspace",
  "tenant_id": "6c1260bb-1b02-46b6-953e-08c325de821e",
  "cloud_provider": "aws",
  "region_code": "aws-us-east-1",
  "has_api_key": true,
  "is_default": true,
  "drive9_home": "/Users/example/.tdc/drive9-home/ZGVmYXVsdA/d29ya3NwYWNl"
}
```

Do not print `api_key` by default. If debug output shows Drive9 command invocation, redact API keys and TiDB Cloud keys.

## Dry Run

Mutating commands must keep dry-run support:

- `create-file-system --dry-run` validates profile identity, placement, selected new resource name, and endpoint resolution. It must not create registry files or run Drive9.
- `delete-file-system --dry-run` validates that the named resource exists and reports which registry files would be removed. It must not call Drive9.
- `set-default-file-system` and `unset-default-file-system` may support `--dry-run` if wired through the shared mutating command path; if not, they must be simple local commands with predictable JSON output.

Dry-run output must never include API keys.

## Errors

Use stable error codes:

- `fs.resource_not_configured`: no resource exists for the selected profile.
- `fs.resource_ambiguous`: multiple resources exist and no selector/default is available.
- `fs.resource_not_found`: selected resource name does not exist under the profile.
- `fs.resource_credentials_incomplete`: config exists without credentials or credentials exist without config.
- `fs.resource_name_conflict`: create attempted for a locally registered name with mismatched tenant/API key state.
- `fs.resource_migration_failed`: flat legacy fields could not be migrated safely.

All messages must include the active profile name and the relevant file system name when available.

## Testing

Unit tests:

- Registry store writes config and credentials with correct file modes.
- Resource names are encoded safely and cannot escape `~/.tdc/fs_resources`.
- Selection precedence covers flag, environment, default, unique resource, ambiguous resources, and missing resources.
- Legacy flat config migrates into the registry and clears old flat fields.
- Incomplete legacy flat config produces `fs.resource_credentials_incomplete`.
- Drive9 runner receives a resource-scoped HOME and selected `DRIVE9_API_KEY`, `DRIVE9_REGION_CODE`, and `DRIVE9_SERVER`.
- Creating a second resource under the same profile no longer fails with resource mismatch.
- Deleting one resource does not remove another resource or its credentials.

E2E tests:

- Create two fake resources under one temp profile and verify `list-file-systems`, `set-default-file-system`, and selection behavior.
- Verify all fs adjunct commands fail with `fs.resource_ambiguous` when multiple resources exist and no default is set.
- Verify `--file-system-name` selects the intended resource for representative data-plane, mount, fs-git, fs-journal, and fs-vault commands using a fake companion.

Live e2e:

- Create two uniquely named tdc fs resources in the `live-e2e` profile only if quota allows.
- If quota does not allow two resources, run a single-resource live flow plus a local fake-companion multi-resource selection flow.
- Live cleanup must delete only resources created by the test run.

## Acceptance Criteria

- One profile can store, list, select, and delete multiple tdc fs resources.
- Existing fs commands work without `--file-system-name` when exactly one resource exists or a default is configured.
- Existing fs commands return a clear ambiguity error when multiple resources exist and no default/selector is available.
- Drive9 companion HOME is isolated per profile and file system resource.
- tdc never uses Drive9 `current_context` as the source of truth for resource selection.
- `~/.tdc/credentials` no longer stores `fs_api_key` for the active resource after migration; per-resource credentials files own those secrets.
- README and AGENTS are updated when implementation changes command behavior, local state layout, or setup instructions.

## Out Of Scope

- Cross-resource copy using Drive9 context-qualified paths.
- Sharing one Drive9 context between multiple tdc profiles.
- User-facing Drive9 `ctx` commands.
- Importing existing standalone `~/.drive9/config` into tdc.
- Changing Drive9 backend APIs.
