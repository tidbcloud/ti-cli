# Agentic CLI - ti

ti is the command-line interface for TiDB Cloud Starter and TiDB Cloud Filesystem. It is designed for people, scripts, and AI agents that need deterministic resource management without terminal-specific assumptions.

ti is currently in Preview. Its feature and command contracts can change before GA.

## Product Scope

- `ti db` manages TiDB Cloud Starter clusters and branches, prepares SQL users, formats connection strings, and executes one SQL statement per invocation.
- `ti fs` manages TiDB Cloud Filesystem resources, files, layers, packs, and mounts.
- `ti fs-git`, `ti fs-journal`, and `ti fs-vault` expose Filesystem-backed Git workspace, append-only journal, and secret-management workflows.
- `ti configure` initializes a local profile.
- `ti update` explicitly checks for or installs a release update.

ti implements only Starter database operations in the current Preview. Commands without a required cluster ID require the exact `--db-cluster-type starter` value and never default it. Commands with a cluster ID discover `servicePlan` from the resource and dispatch internally; they do not expose a type flag. Recognized but unimplemented products and unknown or conflicting plan metadata fail closed.

## Command Design

The command tree has at most two command levels:

```text
ti <command> [subcommand]
```

`configure` and `update` are intentional top-level verb exceptions. Other top-level commands identify product domains: `db`, `fs`, `fs-git`, `fs-journal`, and `fs-vault`.

- Commands and flags use complete, self-explanatory names.
- Flags are long-only. Do not add one-letter flags.
- Required flags appear before optional flags in help output.
- `ti fs` Unix-style aliases shorten only the command name. Their flags stay long and match the canonical command.
- Only `ti configure` may prompt.
- Help works through `ti help`, `ti <command> help`, and `ti <command> <subcommand> help`.
- `--version` remains available at every command level.
- A command does not infer access mode, resource selection, or mutation intent from SQL or other user content.

## Output And Automation

- Successful structured control-plane commands output JSON by default.
- `--output json` and `--output text` are the supported structured output modes.
- `--query` applies a JMESPath expression after execution and before rendering.
- Commands that stream raw bytes reject `--query`.
- Mutating control-plane commands support `--dry-run`; read-only commands reject it.
- Dry run validates local input, credentials, placement, and request construction without sending a mutation.
- Errors use `ti [ERROR]: <actionable message>` and stable exit categories.
- Commands do not silently retry through a different SQL role, transport, resource, or filesystem implementation.

## Profiles And Placement

All persistent ti state belongs under `~/.ti/`.

The local profile namespace is selected in this order:

1. Explicit `--profile`.
2. `TI_PROFILE`.
3. `default`.

An explicit empty `--profile ""` is invalid. Omitting `--profile` selects `default`.

Users select placement with one canonical region code. They never provide separate provider and region fields, service endpoints, or server URLs.

Placement is selected in this order:

1. Explicit global `--region`.
2. `TI_REGION_CODE`.
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
| `alicloud-ap-southeast-1` | Alibaba Cloud | Singapore |

`aws` maps to the internal provider `aws`; `alicloud` maps to `alibaba_cloud`. TiDB Cloud Filesystem supports `aws-us-east-1`, `aws-ap-southeast-1`, `aws-us-west-2`, and `alicloud-ap-southeast-1` through endpoint mappings built into `ti`.

## TiDB Cloud Authentication

TiDB Cloud public/private API keys are selected independently from the profile namespace:

1. If either `TIDB_CLOUD_PUBLIC_KEY` or `TIDB_CLOUD_PRIVATE_KEY` is set, both must be set and the pair is used.
2. Otherwise use `tidb_cloud_public_key` and `tidb_cloud_private_key` from the selected profile in `~/.ti/credentials`.

Environment credentials must not create or select a synthetic `[env]` profile. Any generated persistent state remains under the profile selected by `--profile`, `TI_PROFILE`, or `default`.

TiDB Cloud control-plane requests use HTTP Digest authentication. API keys must not be used as SQL Basic Auth credentials or Filesystem data-plane credentials.

## Configure

`ti configure` collects:

- a canonical `region_code`;
- a TiDB Cloud public API key;
- a TiDB Cloud private API key.

Configure validates the local inputs and stores them atomically. It does not call TiDB Cloud, validate the keys remotely, discover projects, or persist a project ID. Authentication and authorization failures are reported by the first remote command that uses the keys.

`ti configure --non-interactive` reads flags first, then `TI_REGION_CODE`, `TIDB_CLOUD_PUBLIC_KEY`, and `TIDB_CLOUD_PRIVATE_KEY`, and fails instead of prompting for missing input. Interactive configure must handle Ctrl+C and exit with code 130.

Starter cluster creation always omits project selection and lets TiDB Cloud select its server-side default project. Project-related fields and labels returned by TiDB Cloud remain unchanged in command output. Other DB operations identify existing resources by cluster or branch ID.

## Local State And Credentials

Main profile files are:

```text
~/.ti/config
~/.ti/credentials
```

`config` contains non-sensitive profile values. `credentials` contains only profile-scoped TiDB Cloud API keys. Sensitive files use owner-only permissions where the platform supports POSIX modes.

Example:

```toml
# ~/.ti/config
[default]
region_code = "aws-us-east-1"

# ~/.ti/credentials
[default]
tidb_cloud_public_key = "..."
tidb_cloud_private_key = "..."
```

One profile can store credentials for multiple Filesystem resources. Drive9 remains authoritative for remote inventory; ti stores only the one-time token and its routing hint, keyed by the server-assigned file system ID:

```text
~/.ti/fs_credentials/<profile-key>/<file-system-id-key>/credentials
```

The credential file stores `file_system_id`, canonical `region_code`, and the owner `api_key`. The main profile never stores a default resource ID or resource API keys.

Legacy flat `fs_*` fields and name-keyed `~/.ti/fs_resources` records are migration input only. Complete legacy records are copied into the ID-keyed credential store without deleting the source, preserving rollback safety. Incomplete or conflicting legacy state fails explicitly.

DB SQL credentials are cluster-scoped because TiDB Cloud cluster IDs are globally unique:

```text
~/.ti/db_users/<cluster-id>/credentials
```

The file contains `[read_only]`, `[read_write]`, and `[admin]` sections with generated username/password pairs. SQL credentials do not belong in the main profile credentials file.

Background Filesystem and Vault mounts store only non-secret routing state under `~/.ti/mounts/`. Operation logs live under `~/.ti/logs/`.

## Starter SQL Access

`ti db create-db-sql-users` creates or repairs three stable managed users for a cluster:

- read-only;
- read-write;
- admin.

The command is idempotent and must not create a new set on every invocation.

`ti db format-db-connection-string` formats existing credentials; it does not create a remote resource. It supports common connection-string formats and `.env` components. `ti db execute-sql-statement` executes exactly one statement.

Both commands default to read-write. `--read-write`, `--read-only`, and `--admin` are mutually exclusive explicit choices. There is no automatic role classification.

SQL execution prefers the HTTPS SQL API and uses the selected SQL username/password as Basic Auth. The explicit `--transport mysql` mode opens one MySQL connection for one invocation and closes it afterward; it is not an automatic fallback.

## Filesystem Ownership Boundary

ti does not implement filesystem runtime semantics itself. Installed `ti fs`, `ti fs-git`, `ti fs-journal`, and `ti fs-vault` commands route through the bundled `ti-drive9` companion.

- ti owns command naming, profile and region resolution, resource selection, credential storage, preflight validation, output/query behavior, errors, installation, and updates.
- Drive9 owns file data-plane semantics, layers, pack/unpack, FUSE and WebDAV mounts, drain, Git workspace behavior, journal behavior, and Vault behavior.
- There is no native ti filesystem fallback.
- `ref/drive9` is context only and is never imported, built, packaged, or used by tests.
- ti exposes only operations present in the Drive9 public CLI. It does not expose Drive9 internal APIs.

Each resource runs the companion with isolated state under:

```text
~/.ti/drive9-home/<profile-key>/<resource-key>
```

ti supplies a sanitized companion environment containing the resolved server, canonical region, and resource owner token. Inherited `DRIVE9_*` values must not override ti's selection. Users do not edit `~/.drive9` for ti workflows.

Filesystem resource selection is:

1. Explicit `--file-system-id` or `TI_FS_FILE_SYSTEM_ID`.
2. The file system ID embedded in an explicitly supplied FS token; any separate ID must match it.
3. Otherwise fail with `fs.missing_file_system_id` before endpoint resolution, companion startup, or a remote call.

ti never infers a target from profile state, the number of local credentials, creation order, or deletion side effects. Creating the first resource does not select it for later commands, and deleting a resource does not promote another resource. `TI_FS_FILE_SYSTEM_ID` is an explicit process-scoped selector, or a consistency assertion when a token is also supplied; it is not a persisted default.

Remote data-plane, mount, Git, journal, and owner Vault commands select their FS token in this order:

1. Explicit command-local token flag.
2. `TI_FS_TOKEN`.
3. The selected resource's credentials file.

A clean agent sandbox can access an existing Filesystem using only:

```text
TI_FS_TOKEN
TI_REGION_CODE
```

These environment values form an in-memory command context and are not persisted. The ID is derived from the token. TiDB Cloud API keys remain required for remote FS create, list, describe, and delete; deletion requires an ID but no local owner token.

Drive9 is authoritative for the region-scoped remote inventory. `create-file-system` accepts no user-defined name and returns the server-assigned `file_system_id` plus `fs_token` once in its structured result. This owner credential must never appear in logs, telemetry, debug output, errors, non-secret config, or list/describe output. A known token can be validated and persisted with `import-file-system-token`.

On macOS and Windows, automatic mounting selects WebDAV. On Linux, automatic mounting selects FUSE. macOS users can install macFUSE and explicitly request `--driver fuse` for the full mount behavior. Vault mount requires FUSE and is unavailable on Windows. `drain-file-system` is meaningful only for a FUSE mount that exposes a drain control socket.

## Install And Update

GitHub Releases and GoReleaser produce release archives and checksums. Supported shell and PowerShell installers place both `ti` and `ti-drive9` in the user-owned `~/.ti/bin` directory by default.

- Installation and update do not require sudo.
- Installers do not edit shell profiles automatically; they print the command that prepends `~/.ti/bin` to `PATH`.
- Installers support ti release version pinning and checksum verification. The companion is currently downloaded and checksum-verified from Drive9's unversioned release endpoint; ti does not yet negotiate a companion version range.
- `ti update --check` checks explicitly; there is no background update.
- `ti update` is itself explicit consent and does not require `--yes`.
- The updater stages and verifies ti and its companion before replacement.
- Self-update is allowed only for ti-owned installer/archive installations.
- Package-manager, local-build, and unknown installations fail with actionable guidance.
- Active mounts must be drained and unmounted before updating the companion.

## Logs, Telemetry, And Secret Safety

Local operation logs are enabled by default at `~/.ti/logs/ti.jsonl`. They may include command path, flag names, profile, region, duration, exit code, application error category, service, HTTP method/status, operation, and request ID.

Logs must never include flag values, SQL text or results, file contents, remote or local paths, request/response bodies, connection strings, API keys, FS tokens, DB passwords, Vault tokens, or secret values.

Disable local logging for one process with `TI_LOGGING=off`, or globally:

```toml
# ~/.ti/.preferences
schema_version = 1

[logging]
enabled = false
```

`~/.ti/.preferences` is optional, hidden from ordinary directory listings, global across profiles, and separate from the profile-only `~/.ti/config` and `~/.ti/credentials` files. Missing settings use in-memory defaults without creating the file. Existing legacy `[logging]` configuration in `~/.ti/config` is migrated atomically. `ti update` does not read or write any state under `~/.ti/`, including settings and operation logs.

Telemetry follows the same data-minimization rule and must be explicitly disclosed after installation. It can collect command/subcommand names, flag names, error codes, duration, region, CLI version, and OS type, but never credentials or user content. Telemetry must have documented settings-file and process-scoped environment opt-outs before collection is enabled.

## Security And Engineering Constraints

- Do not print, log, commit, or place real credentials in examples or fixtures.
- Prefer secret environment variables over command-line secret flags because flags can remain in shell history and process listings.
- Do not add cgo dependencies. The release remains cross-platform unless a feature has an explicit platform boundary.
- Do not depend on anything under `ref/`.
- Keep README, AGENTS, public docs, specs, help, and e2e coverage synchronized with every user-visible code change.
- Test real cloud lifecycle mutations through focused live e2e families and the complete `make live-e2e` release suite. Tests delete only resources they created.
