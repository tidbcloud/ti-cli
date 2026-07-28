# Agentic CLI - tdc

tdc is the command-line interface for TiDB Cloud Starter and TiDB Cloud Filesystem. It is designed for people, scripts, and AI agents that need deterministic resource management without terminal-specific assumptions.

tdc is currently in Preview. Its feature and command contracts can change before GA.

## Product Scope

- `tdc organization` reads TiDB Cloud organization and project context.
- `tdc db` manages TiDB Cloud Starter clusters and branches, prepares SQL users, formats connection strings, and executes one SQL statement per invocation.
- `tdc fs` manages TiDB Cloud Filesystem resources, files, layers, packs, and mounts.
- `tdc fs-git`, `tdc fs-journal`, and `tdc fs-vault` expose Filesystem-backed Git workspace, append-only journal, and secret-management workflows.
- `tdc configure` initializes a local profile.
- `tdc update` explicitly checks for or installs a release update.

tdc is Starter-only in the current Preview. `--db-cluster-type starter` remains explicit so the command contract can accommodate other TiDB Cloud plans later.

## Command Design

The command tree has at most two command levels:

```text
tdc <command> [subcommand]
```

`configure` and `update` are intentional top-level verb exceptions. Other top-level commands identify product domains: `organization`, `db`, `fs`, `fs-git`, `fs-journal`, and `fs-vault`.

- Commands and flags use complete, self-explanatory names.
- Flags are long-only. Do not add one-letter flags.
- Required flags appear before optional flags in help output.
- `tdc fs` Unix-style aliases shorten only the command name. Their flags stay long and match the canonical command.
- Only `tdc configure` may prompt.
- Help works through `tdc help`, `tdc <command> help`, and `tdc <command> <subcommand> help`.
- `--version` remains available at every command level.
- A command does not infer access mode, resource selection, or mutation intent from SQL or other user content.

## Output And Automation

- Successful structured control-plane commands output JSON by default.
- `--output json` and `--output text` are the supported structured output modes.
- `--query` applies a JMESPath expression after execution and before rendering.
- Commands that stream raw bytes reject `--query`.
- Mutating control-plane commands support `--dry-run`; read-only commands reject it.
- Dry run validates local input, credentials, placement, and request construction without sending a mutation.
- Errors use `tdc [ERROR]: <actionable message>` and stable exit categories.
- Commands do not silently retry through a different SQL role, transport, resource, or filesystem implementation.

## Profiles And Placement

All persistent tdc state belongs under `~/.tdc/`.

The local profile namespace is selected in this order:

1. Explicit `--profile`.
2. `TDC_PROFILE`.
3. `default`.

An explicit empty `--profile ""` is invalid. Omitting `--profile` selects `default`.

Users select placement with one canonical region code. They never provide separate provider and region fields, service endpoints, or server URLs.

Placement is selected in this order:

1. Explicit global `--region`.
2. `TDC_REGION_CODE`.
3. The selected profile's `region_code`.

The command-scoped override does not change the profile, credential source, or persisted configuration.

Supported TiDB Cloud Starter placement values are:

| Canonical region code | Cloud provider | Region |
| --- | --- | --- |
| `aws-us-east-1` | AWS | N. Virginia |
| `aws-us-west-2` | AWS | Oregon |
| `aws-eu-central-1` | AWS | Frankfurt |
| `aws-ap-northeast-1` | AWS | Tokyo |
| `aws-ap-southeast-1` | AWS | Singapore |
| `ali-ap-southeast-1` | Alibaba Cloud | Singapore |

`aws` maps to the internal provider `aws`; `ali` maps to `alibaba_cloud`. TiDB Cloud Filesystem availability is resolved from the hosted Drive9 region manifest and can be a subset of the Starter regions.

## TiDB Cloud Authentication

TiDB Cloud public/private API keys are selected independently from the profile namespace:

1. If either `TDC_PUBLIC_KEY` or `TDC_PRIVATE_KEY` is set, both must be set and the pair is used.
2. Otherwise use `tdc_public_key` and `tdc_private_key` from the selected profile in `~/.tdc/credentials`.

Environment credentials must not create or select a synthetic `[env]` profile. Any generated persistent state remains under the profile selected by `--profile`, `TDC_PROFILE`, or `default`.

TiDB Cloud control-plane requests use HTTP Digest authentication. API keys must not be used as SQL Basic Auth credentials or Filesystem data-plane credentials.

## Configure And Default Project

`tdc configure` collects:

- a canonical `region_code`;
- a TiDB Cloud public API key;
- a TiDB Cloud private API key.

After validating the keys, configure lists accessible projects, requires exactly one project whose type is `tidbx_virtual`, and stores its ID as the profile's `project_id`. It commits the profile only after discovery succeeds.

`tdc configure --non-interactive` reads flags first, then `TDC_REGION_CODE`, `TDC_PUBLIC_KEY`, and `TDC_PRIVATE_KEY`, and fails instead of prompting for missing input. Interactive configure must handle Ctrl+C and exit with code 130.

Starter cluster creation resolves its project in this order:

1. Explicit non-empty `--project-id`.
2. The selected profile's discovered `project_id`.
3. Otherwise fail before making the create request.

Other DB operations identify existing resources by cluster or branch ID. Filesystem provisioning does not use the DB `project_id`.

## Local State And Credentials

Main profile files are:

```text
~/.tdc/config
~/.tdc/credentials
```

`config` contains non-sensitive profile values. `credentials` contains only profile-scoped TiDB Cloud API keys. Sensitive files use owner-only permissions where the platform supports POSIX modes.

Example:

```toml
# ~/.tdc/config
[default]
region_code = "aws-us-east-1"
project_id = "..."

# ~/.tdc/credentials
[default]
tdc_public_key = "..."
tdc_private_key = "..."
```

One profile can own multiple Filesystem resources. Each resource has isolated metadata and credentials:

```text
~/.tdc/fs_resources/<profile-key>/<resource-key>/config
~/.tdc/fs_resources/<profile-key>/<resource-key>/credentials
```

The resource config stores `file_system_name`, `tenant_id`, `cloud_provider`, `region_code`, and `created_at`. Its credentials file stores only the owner `api_key`. The main profile never stores a default resource name or resource API keys.

Legacy flat `fs_*` fields are migration input only. A complete legacy resource is migrated into the registry and the old fields are cleared. Incomplete legacy state fails explicitly.

DB SQL credentials are cluster-scoped because TiDB Cloud cluster IDs are globally unique:

```text
~/.tdc/db_users/<cluster-id>/credentials
```

The file contains `[read_only]`, `[read_write]`, and `[admin]` sections with generated username/password pairs. SQL credentials do not belong in the main profile credentials file.

Background Filesystem and Vault mounts store only non-secret routing state under `~/.tdc/mounts/`. Operation logs live under `~/.tdc/logs/`.

## Starter SQL Access

`tdc db create-db-sql-users` creates or repairs three stable managed users for a cluster:

- read-only;
- read-write;
- admin.

The command is idempotent and must not create a new set on every invocation.

`tdc db format-db-connection-string` formats existing credentials; it does not create a remote resource. It supports common connection-string formats and `.env` components. `tdc db execute-sql-statement` executes exactly one statement.

Both commands default to read-write. `--read-write`, `--read-only`, and `--admin` are mutually exclusive explicit choices. There is no automatic role classification.

SQL execution prefers the HTTPS SQL API and uses the selected SQL username/password as Basic Auth. The explicit `--transport mysql` mode opens one MySQL connection for one invocation and closes it afterward; it is not an automatic fallback.

## Filesystem Ownership Boundary

tdc does not implement filesystem runtime semantics itself. Installed `tdc fs`, `tdc fs-git`, `tdc fs-journal`, and `tdc fs-vault` commands route through the bundled `tdc-drive9` companion.

- tdc owns command naming, profile and region resolution, resource selection, credential storage, preflight validation, output/query behavior, errors, installation, and updates.
- Drive9 owns file data-plane semantics, layers, pack/unpack, FUSE and WebDAV mounts, drain, Git workspace behavior, journal behavior, and Vault behavior.
- There is no native tdc filesystem fallback.
- `ref/drive9` is context only and is never imported, built, packaged, or used by tests.
- tdc exposes only operations present in the Drive9 public CLI. It does not expose Drive9 internal APIs.

Each resource runs the companion with isolated state under:

```text
~/.tdc/drive9-home/<profile-key>/<resource-key>
```

tdc supplies a sanitized companion environment containing the resolved server, canonical region, and resource owner token. Inherited `DRIVE9_*` values must not override tdc's selection. Users do not edit `~/.drive9` for tdc workflows.

Filesystem resource selection is:

1. Explicit `--file-system-name`.
2. `TDC_FS_FILE_SYSTEM_NAME`.
3. Otherwise fail with `fs.missing_file_system_name` before endpoint resolution, companion startup, or a remote call.

tdc never infers a target from profile state, the number of registered resources, creation order, or deletion side effects. Creating the first resource does not select it for later commands, and deleting a resource does not promote another resource. `TDC_FS_FILE_SYSTEM_NAME` is an explicit process-scoped selector, not a persisted default.

Remote data-plane, mount, Git, journal, and owner Vault commands select their FS token in this order:

1. Explicit command-local token flag.
2. `TDC_FS_TOKEN`.
3. The selected resource's credentials file.

A clean agent sandbox can access an existing Filesystem using only:

```text
TDC_FS_TOKEN
TDC_REGION_CODE
TDC_FS_FILE_SYSTEM_NAME
```

These environment values form an in-memory command context and are not persisted. TiDB Cloud API keys remain required for `create-file-system` and `delete-file-system`; deletion also requires the resource to be registered locally.

`create-file-system` returns `fs_token` once in its structured result. This owner credential must never appear in logs, telemetry, debug output, errors, non-secret config, or list/describe output.

On macOS and Windows, automatic mounting selects WebDAV. On Linux, automatic mounting selects FUSE. macOS users can install macFUSE and explicitly request `--driver fuse` for the full mount behavior. Vault mount requires FUSE and is unavailable on Windows. `drain-file-system` is meaningful only for a FUSE mount that exposes a drain control socket.

## Install And Update

GitHub Releases and GoReleaser produce release archives and checksums. Supported shell and PowerShell installers place both `tdc` and `tdc-drive9` in the user-owned `~/.tdc/bin` directory by default.

- Installation and update do not require sudo.
- Installers do not edit shell profiles automatically; they print the command that prepends `~/.tdc/bin` to `PATH`.
- Installers support tdc release version pinning and checksum verification. The companion is currently downloaded and checksum-verified from Drive9's unversioned release endpoint; tdc does not yet negotiate a companion version range.
- `tdc update --check` checks explicitly; there is no background update.
- `tdc update` is itself explicit consent and does not require `--yes`.
- The updater stages and verifies tdc and its companion before replacement.
- Self-update is allowed only for tdc-owned installer/archive installations.
- Package-manager, local-build, and unknown installations fail with actionable guidance.
- Active mounts must be drained and unmounted before updating the companion.

## Logs, Telemetry, And Secret Safety

Local operation logs are enabled by default at `~/.tdc/logs/tdc.jsonl`. They may include command path, flag names, profile, region, duration, exit code, application error category, service, HTTP method/status, operation, and request ID.

Logs must never include flag values, SQL text or results, file contents, remote or local paths, request/response bodies, connection strings, API keys, FS tokens, DB passwords, Vault tokens, or secret values.

Disable local logging for one process with `TDC_LOGGING=off`, or globally:

```toml
# ~/.tdc/.preferences
schema_version = 1

[logging]
enabled = false
```

`~/.tdc/.preferences` is optional, hidden from ordinary directory listings, global across profiles, and separate from the profile-only `~/.tdc/config` and `~/.tdc/credentials` files. Missing settings use in-memory defaults without creating the file. Existing legacy `[logging]` configuration in `~/.tdc/config` is migrated atomically. `tdc update` does not read or write any state under `~/.tdc/`, including settings and operation logs.

Telemetry follows the same data-minimization rule and must be explicitly disclosed after installation. It can collect command/subcommand names, flag names, error codes, duration, region, CLI version, and OS type, but never credentials or user content. Telemetry must have documented settings-file and process-scoped environment opt-outs before collection is enabled.

## Security And Engineering Constraints

- Do not print, log, commit, or place real credentials in examples or fixtures.
- Prefer secret environment variables over command-line secret flags because flags can remain in shell history and process listings.
- Do not add cgo dependencies. The release remains cross-platform unless a feature has an explicit platform boundary.
- Do not depend on anything under `ref/`.
- Keep README, AGENTS, public docs, specs, help, and e2e coverage synchronized with every user-visible code change.
- Test real cloud lifecycle mutations through focused live e2e families and the complete `make live-e2e` release suite. Tests delete only resources they created.
