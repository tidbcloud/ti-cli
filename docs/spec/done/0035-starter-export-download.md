# Starter Export List And Download

## Goal

List Starter export tasks and download files from a completed local-target
export. The commands follow the existing `ticloud serverless export list` and
`ticloud serverless export download` API contracts without adopting ticloud's
interactive prompts, short flags, or TUI progress UI.

## User-facing Commands

```bash
ti db list-export-tasks --db-cluster-id <cluster-id>
ti db list-export-tasks --db-cluster-id <cluster-id> --page-size 10 --page-token <token>
ti db list-export-tasks --db-cluster-id <cluster-id> --query 'export_tasks[].id'
ti db list-export-tasks --db-cluster-id <cluster-id> --output text

ti db download-exported-data --db-cluster-id <cluster-id> --export-id <export-id>
ti db download-exported-data --db-cluster-id <cluster-id> --export-id <export-id> --output-path ./export
ti db download-exported-data --db-cluster-id <cluster-id> --export-id <export-id> --output-path ./export --dry-run
```

These are ID-based Starter commands. They do not accept `--db-cluster-type`.
They do not prompt or confirm.

### `ti db list-export-tasks`

Required flags:

- `--db-cluster-id`

Optional flags:

- `--page-size` (`0` uses the API default)
- `--page-token`
- `--order-by`

This command is read-only and rejects `--dry-run`.

### `ti db download-exported-data`

Required flags:

- `--db-cluster-id`
- `--export-id`

Optional flags:

- `--output-path` defaults to the current directory
- `--concurrency` defaults to `3` and must be between `1` and `32`
- `--dry-run`

## List Behavior

1. Discover the cluster, confirm it is Starter, and authorize
   `starter.export.read`.
2. Request one page through
   `GET /v1beta1/clusters/{clusterId}/exports`. Do not load every page into
   memory.
3. Return snake_case tasks with `id`, `display_name`, `state`, `target_type`,
   `file_type`, timestamps, and pagination fields. Derive `id` from
   `exportId` or the resource name.
4. Omit cloud-target credentials and other secret target fields from every
   output and diagnostic path.

## Download Behavior

1. Discover the cluster, confirm it is Starter, and authorize
   `starter.export.read`.
2. List export files through
   `GET /v1beta1/clusters/{clusterId}/exports/{exportId}/files` with bounded
   pagination. Skip the backend `metadata` file.
3. Reject file names that are empty, absolute, or escape the output directory.
4. `--dry-run` lists planned file names after that GET, then stops before
   `POST .../files:download` and before writing local files.
5. Normal execution requests download URLs in batches of `min(2 * concurrency,
   100)` via `POST /v1beta1/clusters/{clusterId}/exports/{exportId}/files:download`
   with body `{ "fileNames": [...] }`.
6. Files download concurrently over HTTPS without Digest auth. Presigned URLs
   never appear in JSON output, text output, errors, logs, or telemetry.
7. Existing local files are skipped. Nested relative names create parent
   directories under `--output-path`.
8. A structured result reports per-file `succeeded`, `skipped`, or `failed`.
   Any failed file returns `db.export_download_failed` after printing the
   structured result.

## Out of scope

- Creating, describing, or canceling export jobs
- Interactive confirmation or TUI progress
- Cloud-target exports (S3, GCS, Azure, OSS)
- Overwriting existing local files
