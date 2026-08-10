# Telemetry Backend Design

## Purpose

The ti telemetry backend is a small product-owned HTTPS ingestion service between the ti CLI, TiDB, and PostHog.

```text
ti CLI -> telemetry backend -> in-memory batcher
  |-> TiDB
  |-> PostHog /batch/
```

The backend exists so the CLI never sends directly to PostHog, never embeds a PostHog project token, and never relies on PostHog as the only telemetry data store. The backend enforces the privacy schema, rate limits abuse, rejects unknown fields, batches valid events in memory, then best-effort writes the same sanitized batch to TiDB and PostHog.

TiDB is the ti-owned telemetry store and future migration/analysis base. PostHog is an analytics destination. TiDB is not an outbox queue in MVP, and the backend must not consume events from TiDB to forward them to PostHog.

## Non-goals

- Do not store raw telemetry request bodies.
- Do not capture command output, API payloads, SQL text, file paths, credentials, profile names, cloud resource IDs, or raw error messages.
- Do not identify users, create PostHog person profiles, call PostHog identify, alias, group, or feature flag APIs.
- Do not require a CLI-shipped API token. Anything shipped in the CLI is public.
- Do not add MQ, Kafka, SQS, Pub/Sub, durable outbox tables, or TiDB-to-PostHog consumer workflows for MVP.
- Do not add internal worker concurrency for PostHog forwarding. One process owns one in-memory batcher and one flush loop.

## Runtime Configuration

The application reads runtime configuration from process environment variables populated by a server-side `.env` file through Docker Compose `env_file`. The production `.env` file is created and maintained directly on the server, is excluded from git, survives application deploys, and is never copied from GitHub Actions. Other deployment systems may use an equivalent server-local secret source such as Kubernetes Secret, systemd `EnvironmentFile`, or a cloud secret manager.

```bash
TELEMETRY_BIND_ADDR=:8080
TELEMETRY_PUBLIC_HOST=telemetry.example.com
TELEMETRY_ENVIRONMENT=production
TELEMETRY_MAX_BODY_BYTES=65536
TELEMETRY_MAX_EVENTS_PER_REQUEST=20
TELEMETRY_BUFFER_MAX_EVENTS=10000
TELEMETRY_FLUSH_MAX_EVENTS=100
TELEMETRY_FLUSH_MAX_BYTES=262144
TELEMETRY_FLUSH_INTERVAL=5s
TELEMETRY_SHUTDOWN_DRAIN_TIMEOUT=5s
TELEMETRY_SINK_TIMEOUT=5s
TELEMETRY_RATE_LIMIT_PER_MINUTE=60
TELEMETRY_RATE_LIMIT_BURST=120
TELEMETRY_TRUSTED_PROXY_CIDRS=172.16.0.0/12
TIDB_DSN=tdc_telemetry:password@tcp(gateway01.us-east-1.prod.aws.tidbcloud.com:4000)/tdc_telemetry?tls=true&parseTime=true
POSTHOG_API_HOST=https://us.i.posthog.com
POSTHOG_PROJECT_TOKEN=phc_xxx
```

`TIDB_DSN` and `POSTHOG_PROJECT_TOKEN` are application credentials. They must remain only in the server-side `.env`, must never be stored in GitHub repository or Environment secrets, must never be committed to git, and must never be logged. GitHub may store only deployment transport credentials such as the SSH host, username, and key. For TiDB Cloud, the DSN must enable TLS with certificate and identity verification. For EU PostHog Cloud, use `https://eu.i.posthog.com`. For self-hosted PostHog, use the ingestion host for that instance.

## HTTP API

### `GET /healthz`

Liveness check.

Response:

```json
{
  "ok": true
}
```

### `GET /readyz`

Readiness check. This verifies that required environment variables are present, the TiDB connection can be opened, and the service can construct the PostHog batch URL. It does not need to send a test event to PostHog.

Response:

```json
{
  "ok": true,
  "tidb_configured": true,
  "posthog_configured": true
}
```

### `GET /metrics`

Prometheus text-format process counters for accepted, rejected, rate-limited, buffered, dropped, and flushed events plus per-sink success/failure totals. This endpoint contains no event values or identifiers. It is available only on the private API container network; the production Caddy configuration returns `404` for `/metrics`.

### `POST /v1/telemetry/batch`

Accepts one small request batch of sanitized ti CLI telemetry events, validates it, enqueues valid events into the bounded in-memory batcher, and returns immediately. The response means the backend accepted the events into memory; it does not mean TiDB and PostHog have already flushed the batch.

The only success status for this endpoint is `202 Accepted`. Do not return `200 OK` for an accepted batch because the asynchronous TiDB and PostHog sink writes are not complete.

Required request headers:

```http
Content-Type: application/json
User-Agent: ti/<version>
```

Request limits:

- Body size: default 64 KiB.
- Events per request: default 20.
- String fields: default max 256 bytes unless stated otherwise.
- `flag_names`: max 64 entries.
- Unknown top-level or event fields: reject with `400`.
- Disallowed field names such as `sql`, `path`, `password`, `token`, `credential`, `cluster_id`, `project_id`, or `profile_name`: reject with `400` even if they appear as unknown fields.
- If the in-memory buffer is full, return `503` with a generic retryable error. Do not block request handlers indefinitely.

Request body:

```json
{
  "schema_version": 2,
  "sent_at": "2026-07-08T12:00:00Z",
  "events": [
    {
      "event_id": "018f7e67-8fe4-7cc2-9ca5-2d3536c7fb44",
      "event_name": "ti.command.finished",
      "occurred_at": "2026-07-08T12:00:00Z",
      "anonymous_installation_id": "ti_01j0a0n8m9f4q2x6cn0b9q3k3z",
      "command_path": "ti fs create-file-system",
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
      "profile_source": "default",
      "tag": "e2b-preview",
      "extra": {"campaign":"launch","runtime":"e2b"}
    }
  ]
}
```

Successful response:

```http
HTTP/1.1 202 Accepted
Content-Type: application/json
```

```json
{
  "accepted": true,
  "accepted_events": 1,
  "schema_version": 2
}
```

Error responses:

```json
{
  "error": "invalid_request",
  "message": "schema validation failed"
}
```

Use these status codes:

- `400` invalid JSON, unsupported schema version, unknown fields, invalid enum, or disallowed field class.
- `405` unsupported method.
- `413` request body too large.
- `429` rate limit exceeded.
- `503` in-memory batcher is full.
- `500` unexpected backend bug.

Do not echo rejected field values in error messages.

## Event Schema

Allowed top-level fields:

| Field | Type | Required | Notes |
| --- | --- | --- | --- |
| `schema_version` | integer | yes | `1` without metadata or `2` with optional metadata. |
| `sent_at` | RFC3339 string | yes | CLI send time. |
| `events` | array | yes | 1 to `TELEMETRY_MAX_EVENTS_PER_REQUEST`. |

Allowed event fields:

| Field | Type | Required | Validation |
| --- | --- | --- | --- |
| `event_id` | string | yes | UUID/ULID-like opaque ID, max 64 bytes. |
| `event_name` | string | yes | New clients send `ti.command.finished`; the backend also accepts legacy `tdc.command.finished` events during the v0.2 migration window. |
| `occurred_at` | RFC3339 string | yes | Command completion time. |
| `anonymous_installation_id` | string | yes | New random IDs use the `ti_` prefix; migrated v0.1 identities retain `tdc_`. The backend accepts `^(ti|tdc)_[a-zA-Z0-9_-]{16,96}$`. |
| `command_path` | string | yes | `ti` plus at most two lowercase kebab-case command levels; the backend also accepts legacy `tdc` paths during the v0.2 migration window; max 128 bytes. |
| `flag_names` | string array | yes | Each entry regex `^[a-z][a-z0-9-]{0,63}$`. |
| `exit_code` | integer | yes | 0 to 255. |
| `error_code` | string | no | Stable code only, max 64 bytes; empty string allowed. |
| `duration_ms` | integer | yes | 0 to 86,400,000. |
| `cloud_provider` | string | no | `aws`, `alibaba_cloud`, `unknown`, or empty. |
| `region_code` | string | no | Known ti region code, `unknown`, or empty. |
| `cli_version` | string | yes | Version-safe characters only, max 64 bytes. |
| `os` | string | yes | Known Go `runtime.GOOS` value. |
| `arch` | string | yes | Known Go `runtime.GOARCH` value. |
| `install_source` | string | no | `github-release`, `homebrew`, `scoop`, `source`, `dev`, `unknown`, or empty. |
| `profile_source` | string | no | `default`, `explicit`, `env`, `unknown`, or empty. |
| `tag` | string | no, v2 only | Explicit caller metadata, valid UTF-8, at most 128 bytes. |
| `extra` | JSON value | no, v2 only | Explicit caller metadata, one compact JSON value at most 2048 bytes and eight levels deep. |

Schema version 1 retains the original exact contract: `tag` and `extra` are absent, TiDB stores an empty tag and null extra, and unknown fields remain rejected. Schema version 2 adds only the two optional metadata fields. The backend must reject any attempt to send profile names, flag values, SQL text, local paths, API payloads, command output, resource IDs, or credentials. It recursively rejects prohibited metadata object keys, including `password`, `token`, `credential`, `sql_text`, `file_path`, `profile_name`, `project_id`, `cluster_id`, `branch_id`, `tenant_id`, and `resource_id`. Prefer strict JSON decoding with unknown-field rejection over best-effort redaction.

## In-memory Batcher

Accepted events are appended to a bounded in-memory batcher. The batcher has exactly one flush loop per process. It flushes the current batch when any of these thresholds is reached:

- `TELEMETRY_FLUSH_MAX_EVENTS`, default 100.
- `TELEMETRY_FLUSH_MAX_BYTES`, default 256 KiB.
- `TELEMETRY_FLUSH_INTERVAL`, default 5 seconds.
- Shutdown drain, capped by `TELEMETRY_SHUTDOWN_DRAIN_TIMEOUT`.

The flush loop writes the same sanitized batch to TiDB and PostHog. These writes are independent best-effort sink writes. A TiDB failure must not prevent the PostHog attempt, and a PostHog failure must not prevent the TiDB attempt. Failures are logged as aggregate operational errors and exported through the private `/metrics` endpoint; they are not reported back to the CLI because the CLI already received `202 Accepted`.

The batcher may do a small in-memory retry for sink failures, but it must not write retry state to disk and must not replay from TiDB. A process crash can lose accepted-but-unflushed events. That is acceptable for MVP telemetry because the data is best-effort and lossy by design.

## TiDB Storage

TiDB stores sanitized telemetry events as ti-owned telemetry data. It is not a queue for PostHog forwarding in MVP.

Recommended schema:

```sql
CREATE TABLE IF NOT EXISTS telemetry_events (
  event_id VARCHAR(64) NOT NULL,
  received_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  occurred_at TIMESTAMP(6) NOT NULL,
  anonymous_installation_id VARCHAR(128) NOT NULL,
  event_name VARCHAR(64) NOT NULL,
  command_path VARCHAR(128) NOT NULL,
  flag_names_json JSON NOT NULL,
  exit_code TINYINT UNSIGNED NOT NULL,
  error_code VARCHAR(64) NOT NULL DEFAULT '',
  duration_ms INT UNSIGNED NOT NULL,
  cloud_provider VARCHAR(32) NOT NULL DEFAULT '',
  region_code VARCHAR(64) NOT NULL DEFAULT '',
  cli_version VARCHAR(64) NOT NULL,
  os VARCHAR(32) NOT NULL,
  arch VARCHAR(32) NOT NULL,
  install_source VARCHAR(32) NOT NULL DEFAULT '',
  profile_source VARCHAR(32) NOT NULL DEFAULT '',
  tag VARCHAR(128) NOT NULL DEFAULT '',
  extra_json JSON NULL,
  schema_version INT UNSIGNED NOT NULL,
  PRIMARY KEY (event_id),
  KEY idx_received_at (received_at),
  KEY idx_command_received (command_path, received_at),
  KEY idx_version_received (cli_version, received_at),
  KEY idx_region_received (cloud_provider, region_code, received_at),
  KEY idx_tag_received (tag, received_at)
);
```

Schema changes are managed only by embedded [Goose](https://github.com/pressly/goose) SQL migrations under `internal/telemetrybackend/migrations/sql/`. The one-shot `ti-telemetry-migrate` process owns the `tdc_telemetry_schema_migrations` history table and runs before the API starts. The API process never runs DDL. Migration `00001` creates the historical v1 table with `CREATE TABLE IF NOT EXISTS`; migration `00002` only adds `tag`, `extra_json`, and `idx_tag_received` with TiDB's `IF NOT EXISTS` syntax. These migrations are additive and contain no `DROP`, `TRUNCATE`, `DELETE`, rename, or data rewrite operation. Existing telemetry rows remain intact. `extra_json` is intentionally not indexed.

TiDB write behavior:

- Use batch `INSERT` for each flush.
- Use an idempotent write strategy such as `INSERT IGNORE` on `event_id` to handle rare duplicate flush attempts.
- Set a bounded sink timeout, default `TELEMETRY_SINK_TIMEOUT=5s`.
- Do not write raw request bodies.
- Do not add triggers, procedures, events, UDFs, geometry/spatial types, or other unsupported/non-portable MySQL features.
- If the future workload requires large analytical scans, add TiFlash or downstream OLAP later; do not add that complexity to MVP.

Before the first deployment, create the database and a dedicated least-privilege service user from an administrative TiDB session:

```sql
CREATE DATABASE IF NOT EXISTS `tdc_telemetry`;
CREATE USER IF NOT EXISTS 'tdc_telemetry'@'%' IDENTIFIED BY 'replace-with-a-generated-password';
GRANT CREATE, ALTER, INSERT ON `tdc_telemetry`.* TO 'tdc_telemetry'@'%';
```

The same deployment identity runs the one-shot migrations and the API, so it needs `CREATE`, `ALTER`, and `INSERT` on the telemetry database. After migration completes, API writes use only batch `INSERT IGNORE` statements. Operators and analytics jobs should use separate read-only identities; do not grant analytics permissions to the ingestion identity.

`make telemetry-e2e` is intentionally isolated from this production database. Its ignored `e2e/.env.telemetry` must point to a test-only TiDB identity with `CREATE` and `DROP` database privileges. The test creates a unique `tdc_telemetry_e2e_*` database, validates initial migration and an additive upgrade with a preserved legacy row, verifies a real event write through a local backend, then drops that temporary database. It never queries, deletes, or migrates production telemetry rows.

## PostHog Forwarding

Forward accepted batches to PostHog's `/batch/` endpoint during the same flush cycle as the TiDB write:

```http
POST {POSTHOG_API_HOST}/batch/
Content-Type: application/json
```

PostHog request body:

```json
{
  "api_key": "<POSTHOG_PROJECT_TOKEN>",
  "historical_migration": false,
  "batch": [
    {
      "event": "ti.command.finished",
      "timestamp": "2026-07-08T12:00:00Z",
      "properties": {
        "distinct_id": "ti_01j0a0n8m9f4q2x6cn0b9q3k3z",
        "$process_person_profile": false,
        "schema_version": 2,
        "event_id": "018f7e67-8fe4-7cc2-9ca5-2d3536c7fb44",
        "command_path": "ti fs create-file-system",
        "flag_names": ["file-system-name", "output"],
        "exit_code": 0,
        "error_code": "",
        "duration_ms": 182,
        "cloud_provider": "aws",
        "region_code": "aws-us-east-1",
        "cli_version": "0.1.0",
        "os": "darwin",
        "arch": "arm64",
        "install_source": "github-release",
        "profile_source": "default",
        "ti_environment": "production",
        "tag": "e2b-preview",
        "extra": {"campaign":"launch","runtime":"e2b"}
      }
    }
  ]
}
```

Important:

- Set `$process_person_profile` to `false` for every event.
- Do not send `$identify`, `$create_alias`, `$groupidentify`, or feature flag events.
- Do not add person properties.
- Do not add IP-derived location fields in the backend.
- Use `anonymous_installation_id` only as `distinct_id`.
- Use `TELEMETRY_SINK_TIMEOUT` for the PostHog request.
- Do not log the full PostHog request body in production.

PostHog's capture docs state that `/i/v0/e` and `/batch` are the primary event ingestion endpoints, that the API uses a project token, and that API-captured events should set `$process_person_profile: false` to remain anonymous.

## Rate Limiting And Abuse Controls

The endpoint is public because the CLI cannot safely hold a secret. Protect it with cheap server-side controls:

- Per-IP token bucket, default 60 requests/minute with burst 120.
- Bounded rate-limit bucket registry; reject new source buckets when the registry is full until stale buckets expire.
- Max body size 64 KiB.
- Max events per request 20.
- Bounded in-memory buffer, default 10,000 events.
- Max string length validation.
- Reject unknown fields.
- Reject suspicious field names.
- Optional CDN/WAF rule for obvious non-CLI abuse.

When behind Caddy or another reverse proxy, set `TELEMETRY_TRUSTED_PROXY_CIDRS` to the private network ranges from which the proxy reaches the API. The backend trusts `X-Forwarded-For` only when the direct peer matches one of those CIDRs. The default trusts loopback only. When the peer is not trusted, the backend ignores forwarding headers and rate limits by the direct remote address.

## Logging And Metrics

Safe logs:

- request ID
- status code
- accepted event count
- validation error category
- rate limit decision
- batch flush size
- TiDB sink success/failure category
- PostHog sink success/failure category
- latency bucket

Never log:

- request body
- `TIDB_DSN`
- `POSTHOG_PROJECT_TOKEN`
- `anonymous_installation_id`
- raw client IP beyond normal reverse proxy access logs, unless required for abuse handling
- rejected field values

Exported backend metrics:

- `telemetry_events_accepted_total`
- `telemetry_requests_rejected_total`
- `telemetry_buffer_events`
- `telemetry_buffer_dropped_total`
- `telemetry_flush_events_total`
- `telemetry_sink_total{sink,result}`
- `telemetry_rate_limited_total`

## Docker Deployment

Implemented production layout:

```text
cmd/ti-telemetry-backend/
cmd/ti-telemetry-migrate/
internal/telemetrybackend/
deploy/telemetry/
  .env
  .env.example
  Dockerfile
  docker-compose.yml
  Caddyfile
```

Create the server-local file from `deploy/telemetry/.env.example`, then replace the placeholder credentials:

```bash
cd /srv/tdc
cp deploy/telemetry/.env.example deploy/telemetry/.env
chmod 600 deploy/telemetry/.env
```

The resulting `deploy/telemetry/.env` contains:

```bash
TELEMETRY_BIND_ADDR=:8080
TELEMETRY_PUBLIC_HOST=telemetry.example.com
TELEMETRY_ENVIRONMENT=production
TELEMETRY_MAX_BODY_BYTES=65536
TELEMETRY_MAX_EVENTS_PER_REQUEST=20
TELEMETRY_BUFFER_MAX_EVENTS=10000
TELEMETRY_FLUSH_MAX_EVENTS=100
TELEMETRY_FLUSH_MAX_BYTES=262144
TELEMETRY_FLUSH_INTERVAL=5s
TELEMETRY_SHUTDOWN_DRAIN_TIMEOUT=5s
TELEMETRY_SINK_TIMEOUT=5s
TELEMETRY_RATE_LIMIT_PER_MINUTE=60
TELEMETRY_RATE_LIMIT_BURST=120
TELEMETRY_TRUSTED_PROXY_CIDRS=172.16.0.0/12
TIDB_DSN=tdc_telemetry:password@tcp(gateway01.us-east-1.prod.aws.tidbcloud.com:4000)/tdc_telemetry?tls=true&parseTime=true
POSTHOG_API_HOST=https://us.i.posthog.com
POSTHOG_PROJECT_TOKEN=phc_xxx
```

The checked-in Compose definition is `deploy/telemetry/docker-compose.yml`. It first runs the non-root one-shot `migrate` service, then starts `api` only after migration completes successfully. Both use the same embedded Go migrations and server-local `.env`. The API has a read-only root filesystem, is exposed only to the private Compose network, and only Caddy publishes ports 80 and 443. The checked-in Caddy configuration does not enable access logging, so client IP addresses are not persisted by default.

Manual deploy from the server:

```bash
set -euo pipefail
cd /srv/tdc
git fetch --prune origin
git checkout main
git pull --ff-only origin main
test -f deploy/telemetry/.env
docker compose --env-file deploy/telemetry/.env -f deploy/telemetry/docker-compose.yml build migrate api
docker compose --env-file deploy/telemetry/.env -f deploy/telemetry/docker-compose.yml up -d --no-build --remove-orphans
docker compose --env-file deploy/telemetry/.env -f deploy/telemetry/docker-compose.yml restart caddy
docker compose --env-file deploy/telemetry/.env -f deploy/telemetry/docker-compose.yml ps
```

## GitHub Actions SSH Deploy

Create a GitHub Environment named `telemetry-production` and configure required reviewers or the applicable production deployment protection rules. The deploy job must bind to that Environment so a manual workflow dispatch cannot reach the server until the deployment is approved.

Deployment secrets available to the `telemetry-production` job:

- `DEPLOY_HOST`
- `DEPLOY_USERNAME`
- `DEPLOY_SSH_KEY`
- `DEPLOY_PATH`

These are deployment transport credentials only. Keep `TIDB_DSN`, `POSTHOG_PROJECT_TOKEN`, and all other application credentials exclusively in the server-side `.env`; do not duplicate them in GitHub repository secrets, GitHub Environment secrets, workflow inputs, artifacts, or SSH script arguments.

Example workflow:

```yaml
name: Deploy Telemetry Backend

on:
  workflow_dispatch:

jobs:
  deploy:
    name: Deploy
    runs-on: ubuntu-latest
    timeout-minutes: 30
    environment:
      name: telemetry-production
    steps:
      - name: Deploy via SSH
        uses: appleboy/ssh-action@v1.0.3
        with:
          host: ${{ secrets.DEPLOY_HOST }}
          username: ${{ secrets.DEPLOY_USERNAME }}
          key: ${{ secrets.DEPLOY_SSH_KEY }}
          command_timeout: 30m
          script: |
            set -euo pipefail
            cd "${{ secrets.DEPLOY_PATH }}"
            git fetch --prune origin
            git checkout main
            git pull --ff-only origin main
            test -f deploy/telemetry/.env
            test ! -L deploy/telemetry/.env
            docker compose --env-file deploy/telemetry/.env -f deploy/telemetry/docker-compose.yml config --quiet
            docker compose --env-file deploy/telemetry/.env -f deploy/telemetry/docker-compose.yml build migrate api
            docker compose --env-file deploy/telemetry/.env -f deploy/telemetry/docker-compose.yml up -d --no-build --remove-orphans
            docker compose --env-file deploy/telemetry/.env -f deploy/telemetry/docker-compose.yml restart caddy
            docker compose --env-file deploy/telemetry/.env -f deploy/telemetry/docker-compose.yml ps
```

The repository ignores `deploy/telemetry/.env`. Deployment fails before replacing containers when the server file is missing. The workflow must not print the `.env` file, interpolate its values into the GitHub Actions log, or upload it as an artifact.

## Smoke Test

After deploy:

```bash
curl -fsS https://telemetry.example.com/healthz
curl -fsS https://telemetry.example.com/readyz
curl -fsS -X POST https://telemetry.example.com/v1/telemetry/batch \
  -H 'Content-Type: application/json' \
  -H 'User-Agent: ti/0.2.0' \
  --data '{
    "schema_version": 2,
    "sent_at": "2026-07-24T12:00:00Z",
    "events": [
      {
        "event_id": "018f7e67-8fe4-7cc2-9ca5-2d3536c7fb44",
        "event_name": "ti.command.finished",
        "occurred_at": "2026-07-24T12:00:00Z",
        "anonymous_installation_id": "ti_01j0a0n8m9f4q2x6cn0b9q3k3z",
        "command_path": "ti db list-db-clusters",
        "flag_names": [],
        "exit_code": 0,
        "error_code": "",
        "duration_ms": 12,
        "cloud_provider": "aws",
        "region_code": "aws-us-east-1",
        "cli_version": "0.2.0",
        "os": "linux",
        "arch": "amd64",
        "install_source": "github-release",
        "profile_source": "unknown",
        "tag": "e2b-preview",
        "extra": {"campaign":"launch"}
      }
    ]
  }'
```

Expected response:

```json
{
  "accepted": true,
  "accepted_events": 1,
  "schema_version": 2
}
```

Then verify the event appears in TiDB after the next flush and appears in PostHog as `ti.command.finished` without creating a person profile.

## When To Add MQ Or Durable Queues

Do not add MQ or durable queues for MVP. Add SQS, Pub/Sub, Redpanda, Kafka, durable outbox tables, or another queue only when at least one of these becomes true:

- accepted-but-unflushed event loss becomes unacceptable
- PostHog or TiDB downtime causes unacceptable event loss
- replay/backfill becomes a product requirement
- multiple destinations need fan-out with delivery guarantees
- strict traffic smoothing is required across multiple backend instances

Until then, in-memory batching plus independent best-effort TiDB/PostHog sink writes is simpler and matches the lossy nature of CLI telemetry.

## Backend Acceptance Checklist

- `POST /v1/telemetry/batch` accepts the documented valid request, enqueues it without synchronously writing TiDB or PostHog, and returns `202 Accepted`.
- The ingestion endpoint never returns `200 OK` for a successfully enqueued batch.
- Unknown fields are rejected.
- Disallowed field names are rejected.
- Request bodies over the configured limit are rejected.
- More than the configured max events per request is rejected.
- Invalid enum values are rejected.
- Full in-memory buffer returns `503` without blocking indefinitely.
- Batcher flushes on max events, max bytes, interval, and shutdown drain.
- TiDB sink performs batch insert into `telemetry_events` using sanitized fields only.
- PostHog sink sends batches to `/batch/` and sets `$process_person_profile: false`.
- TiDB sink failure does not skip the PostHog sink attempt.
- PostHog sink failure does not skip the TiDB sink attempt.
- No component consumes events from TiDB to forward them to PostHog.
- PostHog token and TiDB DSN are not logged.
- Full request bodies are not logged.
- Sink failures do not crash the service.
- `GET /healthz` and `GET /readyz` work behind Caddy.
- Private `GET /metrics` exposes aggregate counters without event values, and Caddy does not expose it publicly.
- Docker Compose deploy runs `migrate` successfully before starting `api` and `caddy`.
- GitHub Actions SSH deploy can rebuild and restart the service with one manual workflow dispatch after approval through the `telemetry-production` Environment.
- `TIDB_DSN`, `POSTHOG_PROJECT_TOKEN`, and other application credentials exist only in the server-side `.env` and are absent from GitHub secrets, workflow inputs, logs, and artifacts.
