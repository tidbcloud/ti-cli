# Global Settings

## Goal

Separate process-wide tdc settings from profile-scoped configuration. `~/.tdc/config` remains a profile document, while the optional `~/.tdc/.preferences` file owns global logging and telemetry preferences that apply regardless of `--profile` or `TDC_PROFILE`.

## Product Decisions

- `~/.tdc/config` contains profile-scoped non-sensitive values only.
- `~/.tdc/credentials` contains profile-scoped TiDB Cloud credentials only.
- `~/.tdc/.preferences` contains optional process-wide user preferences.
- The dot-prefixed file stays out of ordinary directory listings and requires users to opt into editing advanced global preferences.
- Installer and configure flows do not create `~/.tdc/.preferences`.
- Missing settings use in-memory defaults and do not cause a file write.
- No settings management command is added. Users edit the TOML file directly or use the supported process-scoped environment overrides.
- `tdc update` must not read or write settings, operation logs, profiles, credentials, or other `~/.tdc/` state.
- Settings and migration failures never change command stdout, structured output, or exit status. A redacted diagnostic is allowed only with `--debug`.

## Local State Layout

```text
~/.tdc/config                       profile-scoped non-secret configuration
~/.tdc/credentials                  profile-scoped TiDB Cloud credentials
~/.tdc/.preferences                 optional global user settings
~/.tdc/logs/tdc.jsonl               local operation log
```

`settings` is not selected by profile. Its logging and telemetry values are global inputs for `default`, named profiles, environment-backed command contexts, and configuration-free Filesystem access.

The global settings file must never contain credentials, FS tokens, DB passwords, Vault tokens, connection strings, SQL text, file paths, command output, API payloads, cloud resource IDs, or the telemetry installation ID.

## Settings Format

`~/.tdc/.preferences` is TOML:

```toml
schema_version = 1

[logging]
enabled = true
max_file_mb = 10
max_files = 5

[telemetry]
enabled = true
```

Supported fields:

| Field | Type | Required | Behavior |
| --- | --- | --- | --- |
| `schema_version` | integer | no | Defaults to `1`; unsupported versions make global settings unavailable. |
| `logging.enabled` | boolean | no | Defaults to `true`. |
| `logging.max_file_mb` | positive integer | no | Defaults to `10`. |
| `logging.max_files` | positive integer | no | Defaults to `5`, counting the current log file. |
| `telemetry.enabled` | boolean | no | Optional persistent preference consumed by `0022-telemetry.md`; this spec does not define its fallback. |

The file is optional. An absent file is different from an invalid file: absence exposes no user overrides and lets each consumer use its documented defaults; an unreadable, malformed, or unsupported file makes global settings unavailable without failing the user command.

The `.preferences` layout replaces the unshipped intermediate `~/.tdc/settings` design. Do not create or migrate that intermediate path.

Unknown top-level sections or fields are rejected for schema version `1` so misspelled opt-out settings cannot silently leave collection enabled. A future release must increment or explicitly extend the schema before accepting new fields.

Use mode `0600` for a tdc-created settings file where POSIX mode bits are meaningful. Windows uses the same logical path with best-effort owner-private handling. Reading an existing user-created file must not rewrite it merely to normalize permissions, ordering, comments, quoting, or formatting.

## Logging Resolution

Resolve operation logging in this order:

1. Excluded command check. Every `tdc update` form bypasses local operation logging and returns without reading `~/.tdc/.preferences`.
2. `TDC_LOGGING`, when explicitly set.
3. `[logging].enabled` in `~/.tdc/.preferences`.
4. Default enabled.

Accepted `TDC_LOGGING` values are `on`, `true`, `1`, `yes`, `off`, `false`, `0`, and `no`, case-insensitively. An invalid environment value disables operation logging for that process and may emit a redacted `--debug` diagnostic.

`logging.max_file_mb` and `logging.max_files` come only from `settings` or their defaults. Environment variables do not override rotation limits. Invalid or out-of-range limits disable operation logging rather than falling back to a value the user did not request.

Profile selection, region selection, command output mode, and credentials do not affect logging settings. The log path remains fixed at `~/.tdc/logs/tdc.jsonl`; users cannot configure an arbitrary write path.

## Telemetry Setting Boundary

This spec defines only the persistent `telemetry.enabled` field and exposes it as an optional boolean to consumers. It does not interpret telemetry command eligibility, `TDC_TELEMETRY`, release or CI defaults, endpoint availability, installation identity, event contents, or delivery.

`0022-telemetry.md` owns all telemetry-specific resolution and runtime state. In particular, telemetry must not write its installation ID or any other machine-generated state into `~/.tdc/.preferences`.

## Legacy Logging Migration

The previous layout stored global logging preferences in the profile document:

```toml
# ~/.tdc/config
[logging]
enabled = false
max_file_mb = 10
max_files = 5
```

`[logging]` was never a profile, but the old profile parser had to delete and preserve it specially. Implement one idempotent lazy migration for existing installations:

1. Read `~/.tdc/config` as raw TOML and detect the legacy top-level `[logging]` section.
2. If no legacy section exists, perform no migration and do not create `settings`.
3. If `settings` does not exist, atomically create it with the validated legacy `[logging]` values.
4. If `settings` already exists and is valid, it is authoritative and is never rewritten by migration. Its `[logging]` section wins when present; when absent, documented logging defaults apply instead of the legacy values.
5. Remove the legacy `[logging]` section from `~/.tdc/config` only after a required new settings file has been written successfully, or after an existing settings file has been validated.
6. Preserve every profile and credential unchanged. A crash may temporarily leave both copies, but the next run must converge using `settings` as the authoritative source.

Migration is the only reason an existing installation may get an automatically created `settings` file. Fresh installs, installer bootstrap, and `tdc configure` do not create it.

After migration, profile loading and profile persistence must not parse, delete, preserve, or otherwise special-case `[logging]`. The profile name `logging` remains reserved during this Preview contract so old global sections are unambiguous and `tdc configure --profile logging` fails before writing config or credentials.

## Error Behavior

Global settings are optional supporting behavior, so settings failures must not fail the requested command.

- Missing `settings`: use defaults.
- Invalid `settings`: disable operation logging and make global settings unavailable to other consumers for the process; preserve the file unchanged.
- Invalid `TDC_LOGGING`: disable operation logging for the process.
- Migration failure: preserve the legacy config, do not create a partial settings file, disable settings-backed logging for that process, and continue the command.

With `--debug`, tdc may identify the affected file and stable configuration category, but it must not print file contents, environment values, or raw TOML containing future sensitive fields.

## Package Design

- Add `internal/settings` to own the global settings schema, strict TOML parsing, legacy logging migration, and the settings path.
- `internal/oplog` receives an already resolved logging configuration. It no longer reads the profile document directly.
- The settings package exposes `Telemetry.Enabled *bool` without importing or depending on `internal/telemetry`.
- `internal/config/store` owns profile files only after migration. Remove `LoggingConfig`, `ReadLoggingConfig`, `delete(doc, "logging")`, and logging-preservation logic from profile writes.
- `internal/cli` identifies update invocations before initializing operation logging so `tdc update` retains its no-`~/.tdc` contract.

Use the existing `github.com/pelletier/go-toml/v2` dependency for `settings`. Use Go standard packages for filesystem operations, cryptographic randomness, synchronization primitives, and atomic replacement. Do not introduce cgo or platform-specific runtime requirements.

## API Call Chain

Reading settings and migrating legacy logging configuration make no remote API calls.

For an ordinary command:

1. Resolve whether the invocation is excluded from global supporting behavior.
2. Resolve the `TDC_LOGGING` process override.
3. Load and validate `~/.tdc/.preferences` only when required.
4. Perform legacy logging migration when legacy state exists.
5. Initialize operation logging from the resolved logging configuration.
6. Execute the command normally.

For every `tdc update` form, skip steps 2 through 5 and execute update without reading or mutating `~/.tdc/`.

## After This Spec

Users who accept defaults do not need a global settings file. A user can disable both optional data paths with:

```toml
# ~/.tdc/.preferences
schema_version = 1

[logging]
enabled = false

[telemetry]
enabled = false
```

Logging can be disabled for one process without persistent settings:

```bash
TDC_LOGGING=off tdc db list-db-clusters
```

Profile configuration remains unambiguous:

```toml
# ~/.tdc/config
[default]
region_code = "aws-us-east-1"
project_id = "..."

[stage]
region_code = "aws-us-west-2"
project_id = "..."
```

## Acceptance Criteria

- A fresh install and `tdc configure` do not create `~/.tdc/.preferences`.
- Missing settings preserve documented in-memory defaults without writing a file.
- Logging preferences in `settings` apply identically across all profiles.
- `TDC_LOGGING` overrides persistent logging settings only for the current process.
- Logging continues to rotate under `~/.tdc/logs/` using validated global limits.
- The settings model exposes `telemetry.enabled` as an optional value without interpreting or activating telemetry.
- Invalid global settings fail closed without changing command output or exit status.
- Legacy `[logging]` is migrated idempotently without losing profiles or credentials and without rewriting an existing authoritative `settings` file.
- Profile reads and writes no longer special-case global logging after migration.
- `tdc configure --profile logging` fails without writing either profile file.
- Every `tdc update` form runs without reading or writing settings, logs, profiles, credentials, or other `~/.tdc/` state.
- Unit tests cover defaults, both environment overrides, malformed settings, unknown keys, unsupported schema versions, migration precedence, migration interruption, settings permissions, and redaction.
- Black-box tests use a temporary HOME and require no live cloud credentials.

## Dependencies

- `0002-local-config-and-credentials.md`
- `0012-install-and-update-distribution.md`

## Out Of Scope

- Settings management commands.
- Profile-specific logging or telemetry preferences.
- User-configurable log paths or telemetry endpoints.
- Storing credentials, resource identifiers, command contents, installation IDs, or other machine-generated runtime state in `settings`.
- Telemetry environment resolution, installation identity, collection, delivery, or backend behavior; these belong to `0022-telemetry.md`.
- Changing the operation log event schema.
