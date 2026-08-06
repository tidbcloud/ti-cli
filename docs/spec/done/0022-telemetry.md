# Telemetry

## Goal

Collect minimal, privacy-preserving CLI telemetry that helps improve tdc reliability and command UX without capturing sensitive user data or adding telemetry management commands to the public CLI surface.

Telemetry is routed only through a product-owned HTTPS backend. The CLI never sends events directly to PostHog or another third-party analytics endpoint.

## Product Decisions

- Release builds enable telemetry by default only when a product-owned telemetry endpoint is configured in the build.
- Development, test, and CI executions do not send telemetry by default.
- Users control telemetry through `[telemetry]` in the optional global `~/.tdc/.preferences` file or the process-scoped `TDC_TELEMETRY` environment variable.
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

All `tdc update` modes remain outside telemetry because update promises not to read, modify, or upload tdc local state. Do not weaken this boundary to collect update events. The update path must not read `~/.tdc/.preferences`, `~/.tdc/.telemetry-installation-id`, `~/.tdc/config`, `~/.tdc/credentials`, operation logs, DB credentials, FS credentials, SQL text, or file contents.

## Local Telemetry Configuration

Telemetry preferences are global and not profile-scoped. They use the shared settings contract from `done/0021-global-settings.md`:

```toml
# ~/.tdc/.preferences
schema_version = 1

[telemetry]
enabled = false
```

The settings file is optional and is never created merely to record a default telemetry decision. Installer and configure flows do not create it. Do not store telemetry preferences under `[telemetry]` in the profile-scoped `~/.tdc/config`.

The machine-generated installation ID is independent internal state whose complete lifecycle is defined in the Installation ID section:

```text
~/.tdc/.telemetry-installation-id
```

The settings schema and legacy logging migration are defined by `done/0021-global-settings.md`. This spec owns installation ID generation, storage, permissions, validation, concurrency, reset behavior, and telemetry-specific failure handling.

## Resolution And Defaults

Resolve telemetry in this order:

1. Excluded command check. Excluded commands return disabled without reading telemetry state.
2. `TDC_TELEMETRY`, when explicitly set.
3. `[telemetry].enabled` in `~/.tdc/.preferences`.
4. Build and execution default.
5. Endpoint availability.

Accepted environment values:

- `off`, `false`, or `0`: disable telemetry for the current process and do not create or read telemetry state.
- `on`, `true`, or `1`: enable telemetry for the current process when the invocation is eligible and the build has a configured endpoint.

An invalid `TDC_TELEMETRY` value disables sending and produces only a debug diagnostic. Explicit environment enablement may override `enabled = false`, but it must not make an excluded command eligible.

Release builds default to enabled. Development builds, test binaries, and executions with a recognized CI environment default to disabled and must not create `.telemetry-installation-id` unless explicitly enabled. A build without a configured product-owned endpoint never sends or creates telemetry state, regardless of environment or settings.

## Installation ID

The installation ID stored in `~/.tdc/.telemetry-installation-id` is a random local pseudonymous identifier used to correlate reliability trends from one tdc installation. It must:

- start with `tdc_`;
- contain at least 128 bits of cryptographic randomness;
- contain no hostname, username, machine ID, MAC address, IP address, TiDB Cloud identity, profile name, project ID, cluster ID, tenant ID, or FS token material;
- never appear in command output, local operation logs, debug logs, error messages, or telemetry backend operational logs;
- be sent only as the documented telemetry event field.

The file contains only the validated identifier and an optional trailing newline:

```text
tdc_01j0a0n8m9f4q2x6cn0b9q3k3z
```

It is not TOML and is not user configuration. Use mode `0600` where POSIX mode bits are meaningful. Windows uses the same logical path with best-effort owner-private handling.

Create the ID lazily only after the invocation is known to be eligible, telemetry is effectively enabled, and a product-owned endpoint is available. Creation must be race-safe across concurrent first invocations, must not expose a partial file, and must make subsequent events converge on the ID stored in the completed file.

Disabling telemetry does not delete the ID. Users may delete the file to reset their anonymous installation identity. An unreadable, malformed, or invalid existing file disables telemetry without overwriting the file or failing the requested command.

CLI telemetry has not shipped with the previously proposed `~/.tdc/telemetry/config` or intermediate `~/.tdc/settings` layouts. Do not create those paths and do not add migrations from them. Persistent user choice belongs only in `~/.tdc/.preferences`; machine-generated identity belongs only in `.telemetry-installation-id`.

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

Schema version 2 additionally permits opt-in caller metadata defined by `done/0025-telemetry-environment-metadata.md`. `TDC_TELEMETRY_TAG` and `TDC_TELEMETRY_EXTRA` are not automatically collected data: they are read only after eligibility and enablement are resolved, never persist under `~/.tdc/`, and remain subject to byte, JSON-depth, and prohibited-key limits. Schema version 1 remains accepted by the backend without either field during rollout.

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

To disable telemetry, create or edit ~/.tdc/.preferences:

  [telemetry]
  enabled = false

For one process:

  TDC_TELEMETRY=off tdc ...
```

`tdc configure` does not display a telemetry notice, ask a telemetry question, or create telemetry preferences. The release installers are the only CLI distribution surfaces that display the notice.

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
3. For an eligible invocation, `internal/telemetry` evaluates `TDC_TELEMETRY`, the global telemetry setting resolved by `internal/settings`, execution defaults, and endpoint availability.
4. If telemetry is effectively enabled, the package loads or creates the random installation ID before command execution timing begins.
5. The command executes normally.
6. The CLI boundary maps the result to stable exit and application error codes.
7. The telemetry package constructs one allowlisted event and posts it to `POST /v1/telemetry/batch`.
8. The backend validates the schema, enqueues accepted events, and returns `202 Accepted`.
9. The backend flush loop independently writes the same sanitized batch to TiDB and PostHog.
10. The CLI ignores the delivery result except for optional redacted debug diagnostics.

## Package Design

- `internal/telemetry` owns eligibility, defaults, `TDC_TELEMETRY` parsing, event models, field allowlists, installation ID generation and validation, race-safe state persistence, and best-effort delivery.
- `internal/settings` owns `~/.tdc/.preferences`, strict TOML validation, and the optional `[telemetry].enabled` value defined by `done/0021-global-settings.md`.
- `internal/telemetry` combines the environment override with the optional persistent value, command eligibility, execution defaults, and endpoint availability.
- `internal/cli` identifies excluded help/version/update invocations, starts timing for eligible commands, and finalizes events after exit-code mapping.
- `internal/apperr` exposes stable error codes without exposing raw errors.
- `internal/config` and `internal/config/store` do not parse, write, or preserve telemetry preferences or installation state.
- Install scripts display the static notice without prompting. `tdc configure` does not display it.
- `internal/update` remains telemetry-free and independent from all tdc local state.

Use the existing `github.com/pelletier/go-toml/v2` dependency through `internal/settings` and Go standard packages for HTTP, runtime metadata, time, context, filesystem operations, and cryptographic randomness. Do not add cgo or a platform-specific telemetry dependency.

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
- Tests verify every help/version/commandless form is excluded before the telemetry path reads `[telemetry]` or installation state; operation logging may independently read global settings.
- Tests verify every `tdc update` form is excluded before any `~/.tdc/` access.
- Tests verify `tdc update` never sends telemetry.
- Tests verify `TDC_TELEMETRY=off` short-circuits before telemetry reads settings or installation state; operation logging remains independent.
- Tests verify `TDC_TELEMETRY=on` enables only eligible commands when an endpoint is configured.
- Tests verify missing `settings` uses the build default without creating that file.
- Tests verify `[telemetry].enabled = false` is not modified and prevents installation ID creation.
- Tests verify an eligible, effectively enabled release execution atomically creates a valid `.telemetry-installation-id` with mode `0600` and a random ID.
- Tests cover concurrent first use without partial state or unstable persisted identity.
- Tests verify malformed or unreadable settings and malformed or unreadable installation ID state fail closed without being overwritten.
- Tests verify captured events contain flag names but never flag values.
- Tests verify credentials, SQL text, paths, file contents, command output, API payloads, raw errors, profile names, host identity, and cloud resource IDs are absent.
- Tests verify telemetry network failures and non-`202` responses do not alter command stdout, stderr, or exit status.
- Tests verify the installer notice names the settings path and process-scoped environment override.
- Black-box e2e tests use a temporary HOME and a local fake ingestion server; live cloud credentials are not required.

## Dependencies

- `0001-cli-foundation.md`
- `0002-local-config-and-credentials.md`
- `0003-output-error-query-dry-run.md`
- `0012-install-and-update-distribution.md`
- `done/0021-global-settings.md`

## Out Of Scope

- Telemetry management commands.
- Product analytics dashboards.
- Running the telemetry backend inside the `tdc` CLI process.
- User-configurable telemetry endpoints.
- Capturing command output, API response bodies, SQL text, paths, file contents, credentials, flag values, raw errors, host identity, or cloud resource IDs.
- Local durable telemetry queues.
- MQ, Kafka, SQS, Pub/Sub, durable outbox tables, or TiDB-to-PostHog consumer workflows.
