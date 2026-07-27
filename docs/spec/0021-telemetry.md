# Telemetry

## Goal

Collect minimal, privacy-preserving CLI telemetry that helps improve tdc reliability and command UX without capturing sensitive user data or adding telemetry management commands to the public CLI surface.

Telemetry is routed only through a product-owned HTTPS backend. The CLI never sends events directly to PostHog or another third-party analytics endpoint.

## Product Decisions

- Release builds enable telemetry by default only when a product-owned telemetry endpoint is configured in the build.
- Development, test, and CI executions do not send telemetry by default.
- Users control telemetry through `~/.tdc/telemetry/config` or the process-scoped `TDC_TELEMETRY` environment variable.
- Do not add `tdc cli describe-telemetry`, `tdc cli enable-telemetry`, `tdc cli disable-telemetry`, or another telemetry command.
- `tdc update`, help, version, and commandless usage invocations never send telemetry.
- Telemetry is best-effort and lossy. Delivery must not change command stdout, stderr, output format, exit code, or user-visible result.
- The backend returns `202 Accepted` after validated events enter its bounded in-memory batcher. This does not guarantee that TiDB or PostHog has completed its sink write.
- No local durable queue, MQ, Kafka, SQS, Pub/Sub, or TiDB-to-PostHog consumer is required for MVP.

## Eligible Commands

One eligible command invocation emits at most one `tdc.command.finished` event after command execution and error-to-exit-code mapping.

An invocation that fails before Cobra resolves a registered canonical command is excluded. Do not derive telemetry command paths from unknown command text or other unparsed user input. Missing required flags and other validation errors remain eligible only after a registered canonical command has been resolved.

The following invocations are always excluded, even when `TDC_TELEMETRY=on`:

```text
tdc
tdc help
tdc --help
tdc --version
tdc <command> help
tdc <command> --help
tdc <command> --version
tdc <command> <subcommand> help
tdc <command> <subcommand> --help
tdc <command> <subcommand> --version
tdc update
tdc update --check
tdc update --dry-run
tdc update --target-version <version>
```

All `tdc update` modes remain outside telemetry because update promises not to read, modify, or upload tdc local configuration and credentials. Do not weaken this boundary to collect update events. The update path must not read `~/.tdc/config`, `~/.tdc/credentials`, `~/.tdc/telemetry/config`, DB credentials, FS credentials, SQL text, or file contents.

## Local Telemetry Configuration

Telemetry state is global and not profile-scoped:

```text
~/.tdc/telemetry/config
```

The file is TOML without a section header:

```toml
schema_version = 1
enabled = true
installation_id = "tdc_01j0a0n8m9f4q2x6cn0b9q3k3z"
```

The supported fields are:

| Field | Type | Required | Behavior |
| --- | --- | --- | --- |
| `schema_version` | integer | no | Defaults to `1`; unsupported versions disable sending. |
| `enabled` | boolean | yes | Persistent user decision. |
| `installation_id` | string | conditional | Required only when telemetry is enabled and an event is sent. |

Do not store telemetry state under `[telemetry]` in the main `~/.tdc/config`. The main profile parser, `tdc configure`, and profile persistence do not own telemetry state.

On the first telemetry-eligible invocation of a release build:

1. If `~/.tdc/telemetry/config` does not exist and the effective build default is enabled, generate a cryptographically random installation ID.
2. Atomically create the directory and config with `schema_version = 1`, `enabled = true`, and the generated ID.
3. Use the same ID for subsequent eligible events from that tdc home.

Users disable telemetry by editing the file:

```toml
schema_version = 1
enabled = false
installation_id = "tdc_01j0a0n8m9f4q2x6cn0b9q3k3z"
```

The installation ID may remain while telemetry is disabled. It is local pseudonymous state and must not be sent when disabled. Deleting `~/.tdc/telemetry/config` resets the local telemetry state; a later eligible release invocation applies the release default and generates a new ID.

Users may pre-create a minimal opt-out file:

```toml
enabled = false
```

When this file exists, tdc must not generate or write an installation ID.

If telemetry is enabled and the file has no installation ID, tdc generates one and atomically writes it while preserving supported user settings. Concurrent first invocations must not leave a partial TOML file. If two processes race, later events must converge on the installation ID stored in the completed config.

Use these permissions where POSIX mode bits are meaningful:

```text
~/.tdc/telemetry/          0700
~/.tdc/telemetry/config    0600
```

Windows uses the same logical path and best-effort owner-private file handling.

If the telemetry config is unreadable, malformed, has an unsupported schema version, or contains an invalid value, fail closed: do not send telemetry, do not overwrite the file, and emit only a redacted debug diagnostic when `--debug` is enabled. Telemetry configuration failures never fail the user command.

## Resolution And Defaults

Resolve telemetry in this order:

1. Excluded command check. Excluded commands return disabled without reading telemetry state.
2. `TDC_TELEMETRY`, when explicitly set.
3. `~/.tdc/telemetry/config`.
4. Build and execution default.
5. Endpoint availability.

Accepted environment values:

- `off`, `false`, or `0`: disable telemetry for the current process and do not create or read telemetry state.
- `on`, `true`, or `1`: enable telemetry for the current process when the invocation is eligible and the build has a configured endpoint.

An invalid `TDC_TELEMETRY` value disables sending and produces only a debug diagnostic. Explicit environment enablement may override `enabled = false`, but it must not make an excluded command eligible.

Release builds default to enabled. Development builds, test binaries, and executions with a recognized CI environment default to disabled and must not create `~/.tdc/telemetry/` unless explicitly enabled. A build without a configured product-owned endpoint never sends, regardless of environment or config.

## Installation ID

`installation_id` is a random local pseudonymous identifier used to correlate reliability trends from one tdc installation. It must:

- start with `tdc_`;
- contain at least 128 bits of cryptographic randomness;
- contain no hostname, username, machine ID, MAC address, IP address, TiDB Cloud identity, profile name, project ID, cluster ID, tenant ID, or FS token material;
- never appear in command output, local operation logs, debug logs, error messages, or telemetry backend operational logs;
- be sent only as the documented telemetry event field.

## Collected And Prohibited Data

Telemetry may collect:

- canonical command path;
- explicitly supplied flag names, never flag values;
- stable exit code;
- stable application error code;
- execution duration;
- cloud provider;
- canonical region code;
- CLI version;
- OS and architecture;
- install source;
- profile source category: `default`, `explicit`, `env`, or `unknown`.

Telemetry must not read or send:

- TiDB Cloud public or private API keys;
- FS owner tokens, scoped tokens, or vault tokens;
- generated DB SQL usernames or passwords;
- SQL text;
- FS paths, local paths, or file contents;
- command output or query output;
- raw API payloads or response bodies;
- flag values;
- raw error messages;
- profile names;
- project IDs, cluster IDs, branch IDs, tenant IDs, token IDs, journal IDs, layer IDs, or other cloud resource identifiers;
- hostnames, usernames, machine IDs, MAC addresses, or client IP addresses.

The CLI constructs events from an explicit allowlisted model. It must not serialize Cobra command objects, arbitrary error objects, config structs, API requests, API responses, or command results.

## User Notice

Installer scripts must explain telemetry after installation without prompting for a telemetry choice:

```text
tdc collects anonymous command usage and reliability telemetry in release builds.

Collected:
- command and flag names, never flag values
- exit and stable error codes
- duration, region, tdc version, OS, and architecture

Never collected:
- credentials or tokens
- SQL text
- file paths or contents
- command output or API response payloads
- cloud resource IDs

To disable telemetry, create or edit ~/.tdc/telemetry/config:

  enabled = false

For one process:

  TDC_TELEMETRY=off tdc ...
```

`tdc configure` may show the same notice for users who build or copy the binary without the installer. It must not ask a telemetry question and must not create the telemetry config.

The first release that enables telemetry by default must state this in its release notes. A successful update to that release may print the same static notice, but update must not read telemetry state or send an event.

## CLI Event And Delivery

Event model sent to the product-owned backend:

```json
{
  "schema_version": 1,
  "sent_at": "2026-07-24T12:00:00Z",
  "events": [
    {
      "event_id": "018f7e67-8fe4-7cc2-9ca5-2d3536c7fb44",
      "event_name": "tdc.command.finished",
      "occurred_at": "2026-07-24T12:00:00Z",
      "anonymous_installation_id": "tdc_01j0a0n8m9f4q2x6cn0b9q3k3z",
      "command_path": "tdc fs create-file-system",
      "flag_names": ["file-system-name", "output"],
      "exit_code": 0,
      "error_code": "",
      "duration_ms": 182,
      "cloud_provider": "aws",
      "region_code": "aws-us-east-1",
      "cli_version": "0.2.0",
      "os": "darwin",
      "arch": "arm64",
      "install_source": "github-release",
      "profile_source": "default"
    }
  ]
}
```

Delivery behavior:

- Post only to the build-configured product endpoint, for example `https://telemetry.tidbcloud.com/v1/telemetry/batch`.
- Do not add a user-facing telemetry endpoint or server URL setting.
- Use the Go standard HTTP client with a short hard timeout and one attempt.
- Send after command completion so the event includes duration and final stable exit/error codes.
- Accept `202 Accepted` as successful ingestion.
- Treat network errors, timeouts, `4xx`, and `5xx` as dropped telemetry.
- Never retry in the foreground command path.
- Never print a delivery result during normal execution.
- Delivery failures are redacted debug diagnostics only.
- Do not persist an unsent queue.

## API Call Chain

1. Cobra resolves the canonical command and whether the invocation is excluded.
2. Excluded invocations execute without reading telemetry environment or files.
3. For an eligible invocation, `internal/telemetry` evaluates `TDC_TELEMETRY`, the independent telemetry config, execution defaults, and endpoint availability.
4. If telemetry is effectively enabled, the package loads or creates the random installation ID before command execution timing begins.
5. The command executes normally.
6. The CLI boundary maps the result to stable exit and application error codes.
7. The telemetry package constructs one allowlisted event and posts it to `POST /v1/telemetry/batch`.
8. The backend validates the schema, enqueues accepted events, and returns `202 Accepted`.
9. The backend flush loop independently writes the same sanitized batch to TiDB and PostHog.
10. The CLI ignores the delivery result except for optional redacted debug diagnostics.

## Package Design

- `internal/telemetry` owns eligibility, defaults, event models, field allowlists, installation ID generation, independent TOML state, atomic writes, and best-effort delivery.
- `internal/cli` identifies excluded help/version/update invocations, starts timing for eligible commands, and finalizes events after exit-code mapping.
- `internal/apperr` exposes stable error codes without exposing raw errors.
- `internal/config` and `internal/config/store` do not parse, write, or preserve telemetry state.
- Install scripts and `tdc configure` display the static notice without prompting.
- `internal/update` remains telemetry-free and independent from all tdc local state.

Use the existing `github.com/pelletier/go-toml/v2` dependency for the telemetry config and Go standard packages for HTTP, runtime metadata, time, context, filesystem operations, and cryptographic randomness. Do not add cgo or a platform-specific telemetry dependency.

## Backend Contract

The backend contract, implementation layout, and deployment model are documented in `docs/telemetry-backend-design.md`. The backend is built as the independent `tdc-telemetry-backend` process. It shares the repository and Go module for versioned schema changes and CI coverage, but it is not imported by or run inside the `tdc` CLI process.

The CLI depends on these guarantees:

- valid event batches are acknowledged with `202 Accepted` after entering a bounded in-memory buffer;
- unknown or prohibited fields are rejected;
- accepted events are best-effort and may be lost before sink flush;
- TiDB and PostHog receive the same sanitized event batch through independent sink attempts;
- PostHog person profiles are disabled with `$process_person_profile = false`;
- no CLI-shipped backend credential is required.

## Acceptance Criteria

- Release builds with a configured endpoint default to enabled for eligible commands.
- Development, test, and CI executions default to disabled and do not create telemetry state.
- No telemetry management command is registered.
- Tests verify every help/version/commandless/update form is excluded before telemetry config access.
- Tests verify `tdc update` does not read or write telemetry state and never sends telemetry.
- Tests verify `TDC_TELEMETRY=off` short-circuits before filesystem access.
- Tests verify `TDC_TELEMETRY=on` enables only eligible commands when an endpoint is configured.
- Tests verify a missing config in an eligible release execution creates a valid TOML file with mode `0600`, a parent directory with mode `0700`, and a random installation ID.
- Tests verify a pre-created `enabled = false` file is not modified and does not gain an installation ID.
- Tests verify enabled config without an ID gains one through an atomic write.
- Tests cover concurrent first use without partial TOML or unstable persisted identity.
- Tests verify malformed, unreadable, or unsupported telemetry config fails closed without being overwritten.
- Tests verify captured events contain flag names but never flag values.
- Tests verify credentials, SQL text, paths, file contents, command output, API payloads, raw errors, profile names, host identity, and cloud resource IDs are absent.
- Tests verify telemetry network failures and non-`202` responses do not alter command stdout, stderr, or exit status.
- Tests verify installer and configure notice text names the config path and process-scoped environment override.
- Black-box e2e tests use a temporary HOME and a local fake ingestion server; live cloud credentials are not required.

## Dependencies

- `0001-cli-foundation.md`
- `0002-local-config-and-credentials.md`
- `0003-output-error-query-dry-run.md`
- `0012-install-and-update-distribution.md`

## Out Of Scope

- Telemetry management commands.
- Product analytics dashboards.
- Running the telemetry backend inside the `tdc` CLI process.
- User-configurable telemetry endpoints.
- Capturing command output, API response bodies, SQL text, paths, file contents, credentials, flag values, raw errors, host identity, or cloud resource IDs.
- Local durable telemetry queues.
- MQ, Kafka, SQS, Pub/Sub, durable outbox tables, or TiDB-to-PostHog consumer workflows.
