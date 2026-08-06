# FS Token Authentication And Configuration-Free Access

> **Latest identity update:** after `0026-remote-fs-resource-inventory.md`, a token-only sandbox needs only `TDC_FS_TOKEN` and `TDC_REGION_CODE`. The ID is derived from the token; `TDC_FS_FILE_SYSTEM_ID` is optional.

This spec refines `docs/requirements/mount-file-system-config-free-mount.md`. It keeps the configuration-free workflow but uses tdc's existing global `--region` contract, the profile-scoped FS resource registry introduced by `docs/spec/done/0016-profile-fs-resource-registry.md`, and the Drive9 companion ownership boundary from `docs/spec/done/0015-drive9-companion-wrapper-for-tdc-fs.md`.

The persistent default-resource and unique-resource fallback rules in this completed spec are superseded by `docs/spec/done/0020-explicit-file-system-selection.md`. Token precedence and configuration-free access remain valid; the old name selectors are superseded by the ID/token selection contract in `0026-remote-fs-resource-inventory.md`.

## Goal

Allow an existing tdc file system to be used in an ephemeral environment without running `tdc configure` or providing TiDB Cloud public/private API keys. A user or agent with a file-system name, canonical region code, and FS token must be able to mount the file system and use token-authenticated `tdc fs`, `tdc fs-git`, `tdc fs-journal`, and `tdc fs-vault` operations.

TiDB Cloud API keys must be required only for operations that actually call the TiDB Cloud control plane. The expected credential boundary is:

| Command class | TiDB Cloud public/private keys | FS token | Placement |
| --- | --- | --- | --- |
| `tdc fs create-file-system` | required | returned by provisioning | required |
| `tdc fs delete-file-system` | required | required | required |
| FS data plane, check, layer, pack/unpack, mount, git, and journal | not required | required | required |
| Owner vault management/read operations | not required | required | required |
| Delegated vault consumption | not required | delegated vault token | required |
| Local FS registry list and describe | not required | not required | not required |

This is an authentication-boundary correction in tdc. Drive9 already supports its data plane and mount runtime with `DRIVE9_API_KEY` plus `DRIVE9_SERVER`; no Drive9 backend change is required.

## Terminology And Credential Semantics

- `TDC_FS_TOKEN` is the public tdc environment-variable name for the credential returned by FS provisioning.
- `fs_token` is the JSON result field used by tdc.
- Internally the value is the opaque Drive9 `api_key` returned by `tdc-drive9 create --json` and is passed to the companion as `DRIVE9_API_KEY`.
- tdc must not rewrite the value or promise a `tdc_fs_v1_` prefix. Current values may retain a `drive9_` prefix.
- The provisioned credential is a Drive9 tenant owner API key. It grants access to the selected tdc FS resource and associated Drive9 capabilities such as data plane, mount, layers, git, journal, and owner vault operations. It is not a mount-only least-privilege token and must not be documented as one.
- `file-system-name` is tdc's local resource name and companion namespace. The FS token identifies the remote tenant. tdc cannot prove that a caller-provided name was the name originally used to provision that token, so it must not claim server-side name validation.

## User-Facing Commands

Provisioning returns the FS token while still storing the resource in the selected local profile:

```bash
tdc fs create-file-system --file-system-name my-workspace
```

Default JSON result:

```json
{
  "file_system_name": "my-workspace",
  "tenant_id": "tenant-...",
  "cloud_provider": "aws",
  "region_code": "aws-us-east-1",
  "fs_token": "drive9_...",
  "status": "provisioned",
  "credentials_stored": true
}
```

The environment export workflow is:

```bash
export TDC_FS_TOKEN="$(tdc fs create-file-system \
  --file-system-name my-workspace \
  --query fs_token \
  --output text)"
```

An ephemeral host can mount without `tdc configure` or pre-existing `~/.tdc/config`, `~/.tdc/credentials`, or `~/.tdc/fs_resources`:

```bash
TDC_FS_TOKEN="drive9_..." \
TDC_REGION_CODE="aws-us-east-1" \
tdc fs mount-file-system \
  --file-system-name my-workspace \
  --mount-path ~/my-workspace
```

Equivalent explicit flags use the existing global region flag:

```bash
tdc --region aws-us-east-1 fs mount-file-system \
  --file-system-name my-workspace \
  --mount-path ~/my-workspace \
  --fs-token "drive9_..."
```

Do not add `--region-code`. Canonical command-scope placement remains the global `--region <canonical-region-code>` flag. `TDC_REGION_CODE` remains the environment form.

All commands that authenticate with the FS owner credential accept `--fs-token`. The flag is command-local, long-form, optional, and must be listed after required flags in help. At minimum this includes remote commands under `tdc fs`, `tdc fs-git`, and `tdc fs-journal`, plus owner-authenticated `tdc fs-vault` commands. Environment variables are preferred for automation because a flag value may be retained in shell history or visible in process arguments.

## Resolution And Precedence

Resolve the local profile namespace independently from credentials:

1. Explicit `--profile <name>`.
2. `TDC_PROFILE`.
3. `default`.

The namespace does not need to exist in `~/.tdc/config` when all required ephemeral FS inputs are provided. It still determines runtime state paths and companion HOME isolation.

Resolve the file-system name in this order:

1. Explicit `--file-system-name <name>`.
2. `TDC_FS_FILE_SYSTEM_NAME`.
3. `fs_default_file_system_name` in the selected profile.
4. The only locally registered resource under the selected profile.

Resolve placement in this order:

1. Explicit global `--region <canonical-region-code>`.
2. `TDC_REGION_CODE`.
3. Region stored with the selected local FS resource.
4. Selected profile `region_code`.

Resolve the owner FS credential in this order:

1. Explicit `--fs-token <token>`.
2. `TDC_FS_TOKEN`.
3. API key in `~/.tdc/fs_resources/<profile-key>/<resource-key>/credentials` for the selected resource.

The three values may come from different sources. For example, a command may use `--file-system-name`, `TDC_FS_TOKEN`, and the selected profile's stored region. Resolution must not require all values to come from one source.

An explicitly present empty `--fs-token` or `--region` is a usage error and must not fall back. A set but empty `TDC_FS_TOKEN` or `TDC_REGION_CODE` is treated as missing and may fall back to local state. A malformed canonical region is an error and must not fall back. Credential shape is not used as an authorization decision; the Drive9 server remains authoritative.

When no local resource exists, configuration-free remote access requires a file-system name, canonical region, and FS token after resolution. `tenant_id` is not required because the owner API key identifies the remote tenant. Missing-input errors must identify the missing value and all supported sources.

## Create And Delete Behavior

`tdc fs create-file-system` remains a TiDB Cloud-authenticated provisioning command:

- It requires TiDB Cloud public/private keys from the normal tdc credential precedence.
- It invokes `tdc-drive9 create --json` with TiDB Cloud keys and canonical region.
- It validates the returned `api_key`, tenant identity, and placement before writing local state.
- It stores the API key only in the resource-scoped credentials file and returns the same opaque value as `fs_token` in the command result.
- Fresh provisioning and an idempotent locally existing result both return `fs_token` when complete local credentials exist. An incomplete local resource fails instead of returning an empty token or provisioning a second resource.
- `--dry-run` never returns a token and never invokes Drive9.

`tdc fs delete-file-system` remains a TiDB Cloud-authenticated deprovisioning command:

- It requires the selected resource owner FS token to authenticate the Drive9 tenant.
- It requires TiDB Cloud public/private keys so Drive9 can delete the backing Starter cluster for a TiDB Cloud tenant.
- Configuration-free token input does not make deletion configuration-free. Existing explicit name confirmation, dry-run, resource ownership, and cleanup rules remain in force.
- TiDB Cloud keys are passed to Drive9 only for create and delete. They must not be included in companion environments for data-plane, mount, git, journal, or vault commands.

## Authentication Loader Design

The current `fsBaseServiceAndProfile` calls `commandContext.LoadProfile`, which routes through `auth.LoadProfile` and validates TiDB Cloud keys before every FS command. Replace this single boundary with three explicit loaders:

1. **TiDB Cloud authenticated loader**: loads profile state and validates TiDB Cloud public/private keys. Keep it for DB, organization, `tdc fs create-file-system`, and `tdc fs delete-file-system`.
2. **FS authenticated loader**: resolves namespace, file-system name, placement, and FS/delegated token without validating TiDB Cloud keys. Use it for remote FS, mount, fs-git, fs-journal, and fs-vault operations.
3. **Local FS loader**: resolves only HOME and profile namespace, then reads the local registry without requiring placement or remote credentials. Use it for `list-file-systems`, `describe-file-system`, `set-default-file-system`, and `unset-default-file-system`.

Do not make TiDB Cloud credentials globally optional in `auth.LoadProfile`. Commands that call TiDB Cloud APIs must retain the existing strict auth behavior. Shared config parsing may be refactored so profile namespace, placement, and present credentials can be loaded separately, but the required-credential decision must remain explicit at each command boundary.

Recommended package ownership:

- `internal/config` loads profile namespace and non-secret placement without assuming a TiDB Cloud-authenticated command.
- `internal/auth` remains the TiDB Cloud key validator for TiDB Cloud control-plane commands.
- `internal/fs/fscred` resolves local resource metadata and credentials and accepts ephemeral FS overrides without persisting them.
- `internal/cli` selects the correct loader by command class and parses `--fs-token`.
- `internal/fswrap` receives a resolved in-memory profile and maps its placement/token to sanitized Drive9 environment variables.
- `internal/fs` keeps use cases unaware of where the FS token came from.

Do not add a fake `[env]` profile or persist flag/environment values. Environment credentials select a credential source, not a profile namespace.

## Companion Invocation

For a configuration-free remote command, tdc constructs an in-memory selected profile equivalent to:

```text
Name                    = <selected profile namespace, default "default">
FSResourceName          = <resolved file-system-name>
FSPlacementRegionCode   = <resolved canonical region>
FSCloudProvider         = <parsed provider>
FSRegionCode            = <parsed native region>
FSAPIKey                = <resolved TDC_FS_TOKEN>
```

The existing companion runner maps this to:

```text
HOME=~/.tdc/drive9-home/<profile-key>/<resource-key>
DRIVE9_REGION_CODE=<canonical-region>
DRIVE9_SERVER=<manifest-resolved-endpoint>
DRIVE9_API_KEY=<fs-token>
```

tdc must continue resolving the server internally from the hosted region manifest. Do not add a user-facing server URL, base URL, or endpoint override.

The companion may create runtime/cache files under its resource-scoped HOME. Configuration-free means no configuration is required before execution and no supplied credential is persisted by tdc; it does not mean the mount runtime is forbidden from writing local state needed for operation and cleanup.

## Mount, Drain, And Unmount Lifecycle

Configuration-free mount must remain a thin wrapper around `tdc-drive9 mount`. tdc must not add its own mount daemon, readiness supervisor, retry loop, FUSE implementation, or WebDAV implementation.

For a background mount:

1. tdc resolves the ephemeral inputs in memory.
2. `internal/fswrap` starts `tdc-drive9 mount` with `DRIVE9_SERVER`, `DRIVE9_REGION_CODE`, and `DRIVE9_API_KEY` in the sanitized child environment.
3. Drive9 owns foreground/background behavior, FUSE/WebDAV selection, its 30-second readiness timeout, mount state, caches, and credential handoff to its child process.
4. tdc records only the non-secret locator needed to route later drain/unmount commands to the same resource-scoped companion HOME.

The locator may live under `~/.tdc/mounts/` and contain only values such as canonical mount path, profile namespace, file-system name, canonical region, companion HOME, driver, and companion PID. It must not contain the FS token, TiDB Cloud keys, vault tokens, server response bodies, or file paths inside the remote filesystem.

After configuration-free mount, these commands must work without TiDB Cloud keys and without requiring the user to resupply the FS token:

```bash
tdc fs drain-file-system --mount-path ~/my-workspace
tdc fs unmount-file-system --mount-path ~/my-workspace
```

They resolve the companion HOME from the non-secret mount locator and delegate to `tdc-drive9 mount drain` or `tdc-drive9 umount`. If the locator is unavailable, the command may accept the same explicit FS inputs as recovery, but it must not silently select another configured resource. Successful unmount removes the tdc locator; failed unmount preserves it for retry.

## Security And Output

- `fs_token` is a secret even though create returns it. Never write it to operation logs, telemetry, debug output, generated error messages, command traces, test fixtures, mount locators, or non-secret config.
- Local resource credentials remain mode `0600` where POSIX mode bits apply.
- `--fs-token` must be redacted from debug argument rendering and companion errors.
- `TDC_FS_TOKEN` must be removed from any child environment that does not require it. The companion receives only `DRIVE9_API_KEY`, not the public tdc variable name.
- Create output is the only ordinary structured output that reveals the owner token. List, describe, check, mount, and all other FS commands must not return it.
- `--query fs_token --output text` intentionally writes the secret to stdout. tdc must not duplicate it to stderr.
- No token or key value may appear in `--dry-run` output.
- Local operation logs may record the `fs-token` flag name but never its value.

## Output And Errors

Successful configuration-free commands keep their existing output contracts. Mount output may report `profile: "default"` as the local namespace even when no profile file exists.

Add stable errors for the new boundary:

- `fs.missing_token`: no `--fs-token`, `TDC_FS_TOKEN`, or selected local resource credential is available.
- `fs.empty_token`: explicit `--fs-token ""` was supplied.
- `fs.missing_region`: no `--region`, `TDC_REGION_CODE`, selected resource region, or profile region is available.
- `fs.missing_file_system_name`: no explicit, environment, default, or unique local resource name is available.
- `fs.mount_locator_not_found`: drain/unmount cannot identify the companion runtime for the mount path.

Authentication failures returned by Drive9 retain the normal tdc authentication category and must not print the token. A token/region mismatch should return the remote authentication or endpoint error; tdc must not retry other regions or fall back to a broader local credential.

## API And Command Call Chains

Provision and export:

1. Cobra parses `tdc fs create-file-system`.
2. The TiDB Cloud authenticated loader validates profile placement and TiDB Cloud keys.
3. tdc invokes `tdc-drive9 create --json` with TiDB Cloud keys in a sanitized environment.
4. Drive9 calls `POST /v1/provision`.
5. The response includes `tenant_id`, placement, and `api_key`.
6. tdc atomically stores resource metadata/API key and returns the key as `fs_token`.

Configuration-free data-plane command:

1. Cobra parses name, global region, and `--fs-token` plus environment/local fallbacks.
2. The FS authenticated loader constructs an in-memory selected profile without TiDB Cloud key validation.
3. The endpoint resolver selects the FS server from canonical region.
4. `internal/fswrap` invokes the mapped Drive9 command with `DRIVE9_SERVER` and `DRIVE9_API_KEY`.
5. Drive9 authenticates the token and performs the remote operation.
6. tdc renders the existing result without persisting ephemeral inputs.

Configuration-free mount lifecycle:

1. The FS authenticated loader resolves the ephemeral selected profile.
2. tdc invokes `tdc-drive9 mount` in the resource-scoped companion HOME.
3. Drive9 starts and owns the mount runtime.
4. tdc stores a non-secret mount locator after successful startup.
5. Drain/unmount load the locator and invoke the same companion HOME without TiDB Cloud keys.
6. Successful unmount removes the locator.

## Dependencies And Platform

- No new Go module is required. Use the existing Cobra, TOML, endpoint resolver, FS registry, and companion runner code.
- No cgo is added to tdc.
- The change is platform-neutral at the credential resolver layer.
- Actual FUSE/WebDAV support remains determined by the bundled Drive9 companion and host prerequisites.
- The configuration-free path must work on macOS and Linux where the selected Drive9 mount mode is supported. Windows follows the existing companion-supported mount path.
- `ref/drive9` remains reference-only and must not become a source, test, build, or runtime dependency.

## Testing

Unit tests must cover:

- TiDB Cloud-authenticated, FS-authenticated, and local loader boundaries.
- Mount/data-plane success with no `~/.tdc/config`, no `~/.tdc/credentials`, and no TiDB Cloud environment keys.
- Independent mixing of flag, environment, registry, profile, and default sources.
- Exact precedence for name, region, and token.
- Explicit empty flag errors and malformed region errors without fallback.
- No `[env]` profile or resource registry writes from ephemeral credentials.
- `create-file-system` returns the exact API key as `fs_token` and stores it with mode `0600`.
- Idempotent local create returns the existing token; dry-run never returns it.
- Only create/delete companion calls include `DRIVE9_PUBLIC_KEY` and `DRIVE9_PRIVATE_KEY`.
- Data-plane, mount, git, journal, and vault companion calls omit TiDB Cloud keys.
- Token redaction in debug output, errors, operation logs, and fake-companion call records.
- Mount locator contains no secret and routes drain/unmount to the same companion HOME.
- Failed unmount preserves its locator; successful unmount removes it.

Black-box `make e2e` must use a temporary HOME and fake companion to verify:

- configuration-free mount with flags only;
- configuration-free mount with environment values only;
- mixed-source mount;
- configuration-free FS data-plane, fs-git, fs-journal, and owner fs-vault command routing;
- existing configured profile behavior remains unchanged;
- create/delete still reject missing TiDB Cloud keys;
- local registry commands run without remote credentials.

`make live-e2e-fs` must:

1. Provision a uniquely named FS resource through a configured `live-e2e` profile.
2. Capture the returned `fs_token` without logging it.
3. Use a clean temporary HOME with only file-system name, canonical region, and FS token to mount the resource.
4. Verify bidirectional visibility between mount writes and a token-authenticated data-plane read/write command.
5. Drain when the actual driver supports it, unmount without TiDB Cloud keys, and verify locator cleanup.
6. Delete the resource through the original configured profile and verify only the test resource is removed.

Focused `make live-e2e-fs-git`, `make live-e2e-fs-journal`, and `make live-e2e-fs-vault` should include at least one execution using the FS-authenticated loader without TiDB Cloud keys. Do not print captured tokens in test output.

## After This Spec

A configured machine can provision and hand a resource to an ephemeral agent:

```bash
FS_TOKEN="$(tdc fs create-file-system \
  --file-system-name agent-workspace \
  --query fs_token \
  --output text)"
```

The ephemeral agent needs no TiDB Cloud API keys:

```bash
TDC_FS_TOKEN="$FS_TOKEN" \
TDC_REGION_CODE="aws-us-east-1" \
tdc fs mount-file-system \
  --file-system-name agent-workspace \
  --mount-path /workspace
```

The same inputs can drive direct FS operations, git, journal, and owner vault operations. TiDB Cloud public/private keys are used again only when the owning environment deletes the complete FS resource and its backing Starter cluster.
