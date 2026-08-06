# Telemetry Environment Metadata

## Goal

Allow an explicitly configured process environment to attach one free-form tag and one bounded JSON value to eligible tdc telemetry events. This metadata supports attribution and segmented product analysis without adding CLI flags, profile fields, persistent settings, or automatically collected user data.

The two environment variables are:

```text
TDC_TELEMETRY_TAG
TDC_TELEMETRY_EXTRA
```

They affect telemetry only. They must never affect command behavior, output, exit status, local operation logs, or cloud requests.

## User-facing Behavior

Examples:

```bash
TDC_TELEMETRY_TAG="e2b-preview" tdc fs list-files --file-system-name workspace --path /
```

```bash
TDC_TELEMETRY_TAG="agent-demo" \
TDC_TELEMETRY_EXTRA='{"campaign":"launch","runtime":"e2b"}' \
tdc db list-db-clusters
```

When an environment variable is absent, its event field is absent. An empty value is treated as absent.

The variables are read only after all existing checks have determined that telemetry is enabled, the invocation is eligible, and a product-owned endpoint is configured. Excluded commands and telemetry-disabled processes do not read or send this metadata.

The values are process-scoped and are never persisted to `~/.tdc/.preferences`, `~/.tdc/config`, operation logs, credentials, or another local file. Do not add telemetry metadata flags or public telemetry management commands.

## Tag Contract

`TDC_TELEMETRY_TAG` is an opaque UTF-8 string supplied by the caller.

- Maximum transmitted size: 128 bytes.
- Preserve the caller's content, including spaces and punctuation.
- If longer than 128 bytes, truncate at a valid UTF-8 rune boundary.
- If empty, omit the field.
- If the environment value is not valid UTF-8, omit the field.
- Never print the value in normal output, errors, debug output, or local logs.

The wire field is optional:

```json
"tag": "e2b-preview"
```

## Extra JSON Contract

`TDC_TELEMETRY_EXTRA` contains one free-form valid JSON value. Objects are recommended for analytics, but valid arrays, strings, numbers, booleans, and `null` are accepted.

- Parse the environment value as exactly one JSON value.
- Compact the parsed JSON before measuring it.
- Maximum compact encoded size: 2048 bytes.
- If the value is invalid JSON, contains trailing non-whitespace data, exceeds 2048 bytes after compaction, exceeds the backend nesting limit, or contains a recursively prohibited field name, omit the entire `extra` field.
- Do not byte-truncate JSON and do not partially retain keys or array elements. Oversized `extra` is omitted as agreed, while the rest of the telemetry event remains eligible for delivery.
- An omitted `extra` can produce one generic debug diagnostic when `--debug` is active, but the diagnostic must never include any part of the environment value.

The wire field is optional and retains its JSON type:

```json
"extra": {
  "campaign": "launch",
  "runtime": "e2b"
}
```

The client must use `json.Decoder` with `UseNumber`, verify that there is exactly one JSON value, and re-encode the validated value. Do not transport `extra` as an escaped JSON string.

## Privacy And Prohibited Data

Tag and extra are explicit caller-supplied metadata, not information discovered by tdc. tdc must never auto-populate them from command arguments, flag values, profiles, credentials, environment variables other than the two exact variables, API payloads, command results, resource objects, or local machine identity.

Documentation must tell callers not to include credentials, tokens, SQL, file paths, file contents, personal information, profile names, project IDs, cluster IDs, branch IDs, tenant IDs, filesystem IDs, or other cloud resource identifiers.

The client and backend must recursively reject known prohibited extra object keys using the telemetry backend's existing prohibited-field policy. Matching is case-insensitive. At minimum this includes keys such as `password`, `token`, `credential`, `sql_text`, `file_path`, `profile_name`, `project_id`, `cluster_id`, `branch_id`, `tenant_id`, and `resource_id`.

This key check cannot prove that arbitrary caller text contains no secret. The privacy notice must distinguish automatically collected allowlisted telemetry from explicit metadata supplied through `TDC_TELEMETRY_TAG` and `TDC_TELEMETRY_EXTRA`.

## Wire Schema And Compatibility

New clients send telemetry schema version `2`:

```json
{
  "schema_version": 2,
  "sent_at": "2026-08-05T12:00:00Z",
  "events": [
    {
      "event_id": "018f7e67-8fe4-7cc2-9ca5-2d3536c7fb44",
      "event_name": "tdc.command.finished",
      "occurred_at": "2026-08-05T12:00:00Z",
      "anonymous_installation_id": "tdc_01j0a0n8m9f4q2x6cn0b9q3k3z",
      "command_path": "tdc fs list-files",
      "flag_names": ["file-system-name", "path"],
      "exit_code": 0,
      "error_code": "",
      "duration_ms": 182,
      "cloud_provider": "aws",
      "region_code": "aws-us-east-1",
      "cli_version": "0.2.0",
      "os": "linux",
      "arch": "amd64",
      "install_source": "github-release",
      "profile_source": "default",
      "tag": "e2b-preview",
      "extra": {"campaign":"launch","runtime":"e2b"}
    }
  ]
}
```

The backend must accept both schema version `1` and `2`:

- Version 1 retains its exact existing contract and stores empty tag plus null extra.
- Version 2 accepts the two optional fields and applies all independent validation limits.
- Unknown fields remain rejected. Adding metadata does not turn the event contract into arbitrary object passthrough.
- The backend must be deployed with v2 support before a CLI release starts sending v2.

## Client Call Chain And Package Design

1. `internal/cli` resolves an eligible canonical command exactly as defined by the completed telemetry spec.
2. `internal/telemetry.Start` resolves effective telemetry enablement and endpoint availability.
3. Only after telemetry is enabled, `internal/telemetry` reads the two exact environment variables into the in-memory session.
4. The tag normalizer applies UTF-8 validation and rune-safe byte truncation.
5. The extra normalizer parses, validates, compacts, and either retains the complete bounded JSON value or omits it completely.
6. `Session.Finish` adds the normalized optional fields to the single allowlisted event and delivers it through the existing best-effort path.

`internal/telemetry` owns environment resolution and client-side normalization. `internal/cli` must not parse or copy the values into command context. Use `json.RawMessage` or an equivalent validated representation so the wire payload preserves the JSON type.

No new third-party dependency is needed. Use Go standard UTF-8 and JSON packages. The change remains cross-platform and adds no cgo.

## Backend Validation And Storage

The backend independently enforces:

- tag is absent or valid UTF-8 no longer than 128 bytes;
- extra is absent or one valid compact JSON value no longer than 2048 bytes;
- a fixed maximum nesting depth of 8;
- recursive prohibited-field detection;
- the existing request-body, event-count, and bounded-batcher limits.

Existing deployments need an idempotent schema migration in addition to updating the create-table definition:

```sql
ALTER TABLE telemetry_events
  ADD COLUMN tag VARCHAR(128) NOT NULL DEFAULT '',
  ADD COLUMN extra_json JSON NULL,
  ADD INDEX idx_tag_received (tag, received_at);
```

The implementation must account for repeated startup and partially applied migration state rather than assuming a new database. Do not index `extra_json`; ad hoc JSON inspection is allowed, but a repeatedly queried key should become a separately designed first-class field later.

TiDB receives `tag` and complete `extra_json`. PostHog receives `tag` and `extra` as nested event properties. The backend must not flatten arbitrary extra keys into top-level PostHog properties or use either field as `distinct_id` or person properties.

## Failure Behavior

- Invalid, prohibited, or oversized extra omits only extra; it does not disable telemetry, fail the requested command, or remove a valid tag.
- Invalid tag omits only tag.
- Client normalization failures are generic debug diagnostics only.
- A malicious or nonconforming request rejected by backend validation receives the existing generic `4xx` response and is not enqueued.
- Delivery remains best-effort with one foreground attempt, no durable local queue, and no user-visible status changes.

## Tests

Client tests must cover:

- both variables absent;
- tag present and exact within the limit;
- ASCII and multibyte UTF-8 tag truncation at 128 bytes;
- invalid UTF-8 tag omission;
- valid extra object, array, scalar, and null values;
- whitespace compaction before the 2048-byte check;
- malformed, multi-value, prohibited-key, too-deep, and oversized extra omission;
- valid tag retained when extra is omitted;
- fields not read or emitted when telemetry is disabled or the command is excluded;
- no metadata value in normal output, debug output, errors, or operation logs;
- schema v2 payload and absent-field omission.

Backend tests must cover:

- backward-compatible schema v1 ingestion;
- schema v2 with absent and present optional fields;
- strict UTF-8, size, depth, prohibited-key, and unknown-field rejection;
- TiDB migration behavior for a new and existing schema;
- TiDB batch insertion preserving JSON type;
- PostHog nested properties with person profiles disabled;
- independent TiDB and PostHog sink failure behavior remaining unchanged.

Black-box `make e2e` uses a local telemetry receiver to inspect schema v2 payloads without contacting production. A separate opt-in `make telemetry-e2e` loads the ignored `e2e/.env.telemetry` file and requires a test-only `TDC_TEST_TELEMETRY_TIDB_DSN` with database create/drop privileges. It creates a unique empty database, migrates it through legacy schema version 1, inserts a legacy event, migrates to the latest version, proves that event is preserved, then starts a local telemetry backend and fake PostHog receiver. It executes a no-side-effect CLI dry run against that local backend, verifies the stored schema v2 event and extra JSON, and drops only its temporary database. Ordinary `make test`, `make e2e`, and all live-e2e targets must not read the dotenv file or require a live TiDB instance.

## Documentation Updates

Update the completed telemetry spec, telemetry backend design, installer disclosure where necessary, `README.md`, `AGENTS.md`, and relevant English PingCAP telemetry/security documentation. Document that the variables are optional explicit metadata and repeat the prohibited-data guidance.

## After This Spec

An integration owner can segment anonymous command telemetry by a bounded process tag and attach a small complete JSON context value. Unconfigured users send no new metadata, oversized extra is omitted rather than partially rewritten, and existing schema v1 clients continue to ingest during rollout.
