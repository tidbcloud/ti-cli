# Explicit File System Selection

> **Latest identity update:** `0026-remote-fs-resource-inventory.md` replaces explicit names with server-assigned IDs and permits token-derived ID selection. No default file system is inferred.

## Goal

Remove the persistent default-file-system experience from tdc. Every command that operates on a File System must receive its target explicitly from the current invocation. Data-plane and runtime commands resolve `--file-system-name` or `TDC_FS_FILE_SYSTEM_NAME`; control-plane commands that already require a resource name continue to require their explicit flag. tdc must not infer a target from profile state, registry cardinality, creation order, or deletion side effects.

This change keeps the existing profile-scoped 1:N local resource registry and the Drive9 companion boundary. It changes only how one resource is selected from that registry.

## Product Decisions

- A tdc profile does not have a default File System.
- For data-plane and runtime commands, `--file-system-name` is the highest-priority selector.
- For those commands, `TDC_FS_FILE_SYSTEM_NAME` is the second and final selector source.
- `TDC_FS_FILE_SYSTEM_NAME` is an explicit process-scoped input. It is not persisted and is not described as a default.
- If neither selector is available, commands that require a File System fail before loading credentials or calling Drive9, even when exactly one local resource is registered.
- Creating the first resource does not make it a default.
- Creating a resource does not accept `--set-default`.
- Deleting a resource does not promote another resource.
- tdc does not persist `fs_default_file_system_name` and does not expose default markers in list or describe output.
- A selected local resource can still supply its stored FS token and region. Removing the default does not require users to repeat credentials.
- Configuration-free agent environments continue to work with `TDC_FS_FILE_SYSTEM_NAME`, `TDC_FS_TOKEN`, and `TDC_REGION_CODE`.

## User-Facing Command Changes

Remove these commands:

```text
tdc fs set-default-file-system
tdc fs unset-default-file-system
```

Remove this flag:

```text
tdc fs create-file-system --set-default
```

Commands that operate on an FS accept the existing `--file-system-name` selector and resolve it using the precedence in this spec. Examples include:

```bash
tdc fs list-files --file-system-name workspace
tdc fs mount-file-system --file-system-name workspace --mount-path ./workspace
tdc fs-git clone-git-workspace --file-system-name workspace --repo-url https://github.com/pingcap/tidb.git --target-path ./workspace/tidb
tdc fs-journal create-journal --file-system-name workspace --journal-id jrn-demo --journal-kind agent --title "demo task" --actor agent:tdc
tdc fs-vault create-secret --file-system-name workspace --secret-name db-prod --field DB_URL=mysql://example
```

For repeated commands, the caller may provide the selector once in the process environment:

```bash
export TDC_FS_FILE_SYSTEM_NAME=workspace
tdc fs list-files
tdc fs mount-file-system --mount-path ./workspace
```

An explicit empty flag is invalid and must not fall back to the environment:

```bash
tdc fs list-files --file-system-name ""
```

The command fails with `fs.empty_file_system_name`.

## Commands Without A File System Selector

The following commands do not select an existing FS data-plane context:

- `tdc fs create-file-system` requires its existing `--file-system-name` creation input. The value names the new resource rather than selecting an existing one.
- `tdc fs list-file-systems` lists every locally registered resource in the selected profile namespace.
- `tdc fs drain-file-system` resolves the already-running mount through its required `--mount-path` and the persisted mount locator.
- `tdc fs unmount-file-system` resolves the already-running mount through its required `--mount-path` and the persisted mount locator.
- `tdc fs-vault unmount-vault` resolves the mount through its required mount path.

`describe-file-system` and `delete-file-system` continue to require an explicit `--file-system-name` flag because they identify control-plane resources and do not use `TDC_FS_FILE_SYSTEM_NAME` as a substitute for their required destructive or descriptive target. This preserves the existing explicit control-plane command contract.

## Selection And Credential Resolution

For FS data-plane, mount creation, layer, pack/unpack, Git, Journal, and owner Vault commands, resolve the resource name in this exact order:

1. Explicit non-empty `--file-system-name`.
2. Non-empty `TDC_FS_FILE_SYSTEM_NAME`.
3. Otherwise fail with `fs.missing_file_system_name`.

Do not consult `fs_default_file_system_name`. Do not select the only registry entry.

After resolving the name, credential and placement precedence remains:

1. Token: explicit `--fs-token`, then `TDC_FS_TOKEN`, then the selected local registry credential.
2. Region: explicit global `--region`, then `TDC_REGION_CODE`, then the selected local resource region, then profile `region_code`.

Inputs may be mixed. For example, a flag can select the FS name while the token comes from the environment and the region comes from the local resource. Selection must remain field-by-field rather than requiring every value to come from one source.

The selected resource name continues to determine the isolated Drive9 companion home:

```text
~/.tdc/drive9-home/<profile-key>/<resource-key>
```

tdc then invokes the bundled companion with the same resolved endpoint, FS token, region, and sanitized environment used before this spec. No Drive9 API or CLI change is required.

## Local State Migration

Remove `fs_default_file_system_name` from the supported profile schema:

```toml
# ~/.tdc/config
[default]
region_code = "aws-us-east-1"
project_id = "..."
```

Existing registry files remain unchanged:

```text
~/.tdc/fs_resources/<profile-key>/<resource-key>/config
~/.tdc/fs_resources/<profile-key>/<resource-key>/credentials
```

When an older config contains `fs_default_file_system_name`:

- never use its value to select a resource;
- remove it through an idempotent local schema migration;
- do not copy it to another config key or environment variable;
- do not delete or rewrite any resource registry credential;
- do not automatically select the referenced resource for the current command.

The migration may run during the existing local FS registry migration path. It must use the config store's atomic write behavior and preserve unrelated profile, logging, and project fields. Repeated execution must be a no-op.

## Output And Errors

`tdc fs list-file-systems` continues to return the selected profile namespace and locally registered resources, but removes `default_file_system_name` and each resource's `is_default` field.

When no selector is supplied:

```text
tdc [ERROR]: file system name is required; pass --file-system-name or set TDC_FS_FILE_SYSTEM_NAME
```

Use error code `fs.missing_file_system_name`, category `usage`, and exit code `2`. Do not vary the error according to whether zero, one, or multiple resources exist. The message must not recommend `set-default-file-system`.

When the selected local resource does not exist and no explicit FS token provides configuration-free access, preserve `fs.resource_not_found` and include the selected name. When a token is supplied, the selected name remains a local namespace label for companion isolation and need not exist in the registry.

List output must not label one resource as preferred, current, active, or default.

## Package And Code Design

- `internal/config/store` removes `FSDefaultFileSystemName` from the supported profile model and owns the idempotent removal of the legacy TOML key.
- `internal/config` removes `FSDefaultFileSystemName` from the runtime profile.
- `internal/fs/fscred` keeps registry `List`, `Get`, authenticated resolution, legacy flat-resource migration, and companion-home construction, but removes `SetDefault`, default annotations, unique-resource fallback, create-time default assignment, delete-time promotion, and default-aware result models.
- `internal/fs` removes default management use cases and default-related create options.
- `internal/cli` removes the two default commands and `--set-default`, and applies the selector contract consistently across `fs`, `fs-git`, `fs-journal`, and `fs-vault`.
- Mount locators remain the source of truth for drain and unmount, so those operations do not re-resolve an FS selector.
- `internal/authz` removes command-permission entries for the deleted commands.
- `e2e` and live e2e helpers must pass the resource name explicitly or set `TDC_FS_FILE_SYSTEM_NAME` for the test process. Tests must not establish a default as setup.

Do not add a replacement command such as `select-file-system`, `use-file-system`, or `current-file-system`. Do not rename `TDC_FS_FILE_SYSTEM_NAME` to a variable containing `DEFAULT`.

## Dependencies And Portability

This change introduces no third-party package, service API, CGO requirement, daemon, or platform-specific dependency. It uses existing TOML persistence, Cobra flags, environment lookup, local registry, and Drive9 companion invocation.

Behavior must be identical on macOS, Linux, and Windows. Environment-variable syntax differs by shell, but resolution semantics do not. Removing the default must not change FUSE/WebDAV selection or mount runtime behavior.

## API And Companion Call Chains

No TiDB Cloud or Drive9 backend API changes are required.

Locally registered data-plane flow:

```text
CLI flag/environment selector
  -> fscred.Get(profile, file-system-name)
  -> resource credentials and region
  -> hosted Drive9 region manifest resolution
  -> resource-scoped companion HOME
  -> tdc-drive9 public CLI command
  -> existing Drive9 data-plane API
```

Configuration-free flow:

```text
TDC_FS_FILE_SYSTEM_NAME + TDC_FS_TOKEN + TDC_REGION_CODE
  -> in-memory profile namespace
  -> hosted Drive9 region manifest resolution
  -> resource-scoped companion HOME
  -> tdc-drive9 public CLI command
  -> existing Drive9 data-plane API
```

Missing selection fails before endpoint resolution, companion startup, or any remote call.

## Documentation Changes

When this spec is implemented, update all command inventories, examples, generated/manual command references, configuration examples, troubleshooting guidance, and agent instructions:

- remove `set-default-file-system`, `unset-default-file-system`, and `--set-default`;
- remove `fs_default_file_system_name`, `default_file_system_name`, and `is_default`;
- show `--file-system-name` in one-off examples;
- show `TDC_FS_FILE_SYSTEM_NAME` in repeated-command and clean-sandbox examples;
- explain that tdc never infers a File System from local registry state.

The default-selection sections of completed specs `0016-profile-fs-resource-registry.md` and `0018-fs-token-auth-and-config-free-access.md` are superseded by this spec. Their 1:N registry, credential isolation, token precedence, and configuration-free access designs remain valid.

## Tests

Unit tests must cover:

- flag selector overrides `TDC_FS_FILE_SYSTEM_NAME`;
- explicit empty `--file-system-name` fails without environment fallback;
- environment-only selection works;
- zero, one, and multiple registered resources all fail with `fs.missing_file_system_name` when no selector is supplied;
- selection never reads legacy `fs_default_file_system_name`;
- old config migration removes only the legacy default key and is idempotent;
- creating the first or later resource never changes the main profile config;
- deleting a resource never promotes another resource;
- list results contain no default fields;
- configuration-free token mode works without `~/.tdc/` state;
- local registry name, environment token, and stored resource region can be mixed;
- drain and unmount continue to resolve through mount locators without an FS selector;
- removed commands and `--set-default` are rejected as unknown CLI input;
- every affected help page describes the selector without calling it a default.

Black-box e2e must cover both supported workflows:

```bash
tdc fs list-files --file-system-name workspace
```

```bash
TDC_FS_FILE_SYSTEM_NAME=workspace tdc fs list-files
```

Live e2e must use its uniquely created FS by explicit name for create, check, data-plane, mount, Git, Journal, Vault, and delete flows. It must verify that omitting both selector sources fails locally and does not issue a companion or remote request.

## After This Spec

Users and agents always know which File System a command targets. A configured machine can reuse locally stored credentials by naming the resource, while an ephemeral sandbox can provide the same name, token, and region entirely through environment variables. Profile state never silently redirects an operation to a different FS.
