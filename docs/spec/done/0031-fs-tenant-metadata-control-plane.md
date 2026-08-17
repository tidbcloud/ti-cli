# FS Tenant Metadata And Direct Control Plane

## Goal

Expose the Drive9 server's organization-scoped File System `display_name` and `label` metadata through `ti` without waiting for or modifying the Drive9 CLI companion. The four remote File System inventory operations move to a typed HTTP client owned by `ti`, while all data-plane, mount, Git, journal, vault, layer, and other filesystem runtime behavior remains delegated to the bundled `ti-drive9` companion.

This spec depends on the hosted File System backend behavior introduced by `tidbcloud/fs` PR #61. It changes only the `ti` repository. Code under `ref/` remains reference-only and must not become a source, build, test, or runtime dependency.

## Motivation

The backend now stores a user-facing display name and labels on each tenant and returns them from the admin tenant create, list, and get APIs. The current `ti` path cannot expose these fields:

1. `ti fs create-file-system` invokes `ti-drive9 create --json`, which calls `POST /v1/provision`. That endpoint does not accept or return tenant metadata.
2. `ti fs list-file-systems` and `ti fs describe-file-system` invoke the Drive9 admin tenant CLI. The current companion decodes the backend response into client models that do not contain `display_name` or `label`, so those fields are discarded before `ti` receives JSON.
3. The backend list API supports server-side display-name and label filters, but the current companion does not expose them.

Unknown response fields do not currently break existing commands, but silently discarding authoritative server metadata makes the new backend feature unusable. Updating only `ti` therefore requires bypassing the companion for the bounded organization control-plane surface instead of attempting to parse companion output that no longer contains the fields.

## API Choice

The two creation endpoints share the same underlying tenant provisioning, owner-token generation, free-plan accounting, and prewarmed-pool claim implementation, but they have different contracts.

| Behavior | `POST /v1/provision` | `POST /v1/admin/tenants` |
| --- | --- | --- |
| Purpose | Generic and legacy provisioning entry point | TiDB Cloud organization management entry point |
| Credentials | Can support empty-body, anonymous, or server-default modes depending on deployment | Requires valid TiDB Cloud API credentials |
| Providers | Can route through the deployment's default provider and local shims | Limited to TiDB Cloud native and native-shared tenants |
| Display name | Unsupported | Supported |
| Labels | Unsupported | Supported |
| Name conflict check | None | Best-effort organization-scoped conflict check |
| Owner token response | Yes | Yes |
| Metadata response | No | Yes |
| Successful status | `202 Accepted` | `202 Accepted` |

`ti fs create-file-system` already requires TiDB Cloud API credentials and does not expose anonymous provisioning. Moving it to `POST /v1/admin/tenants` therefore removes no supported `ti` workflow. It aligns create with the admin list, get, and delete APIs that already define the authoritative organization inventory.

## Scope

Move these operations from companion admin/provision commands to direct typed HTTPS requests:

```text
ti fs create-file-system
ti fs list-file-systems
ti fs describe-file-system
ti fs delete-file-system
```

Keep these operations and all related aliases delegated to `ti-drive9`:

- File reads, writes, copies, moves, searches, metadata operations, links, and directories.
- FUSE and WebDAV mount, drain, and unmount.
- Layers, pack, and unpack.
- `ti fs-git`, `ti fs-journal`, and `ti fs-vault`.
- File System token data-plane use and runtime context construction.
- `ti fs check-file-system`, including its companion and remote-root checks.

The direct control-plane client is not a replacement for the Drive9 filesystem client. It is a small organization inventory client for four backend endpoints.

## User-Facing Commands

Existing commands remain valid:

```bash
ti fs create-file-system
ti fs create-file-system --wait
ti fs list-file-systems
ti fs describe-file-system --file-system-id <file-system-id>
ti fs delete-file-system --file-system-id <file-system-id>
```

Add optional metadata to creation:

```bash
ti fs create-file-system --display-name agent-workspace
ti fs create-file-system \
  --display-name agent-workspace \
  --label environment=production \
  --label team=ai \
  --wait
```

Add server-side inventory filters:

```bash
ti fs list-file-systems --display-name workspace
ti fs list-file-systems --label environment=production
ti fs list-file-systems --display-name workspace --label environment=production
```

`--display-name` and `--label` are metadata inputs and filters. They never select a resource for describe, delete, data-plane, mount, token, Git, journal, or vault operations. Existing resources continue to be selected by `--file-system-id`, `TI_FS_FILE_SYSTEM_ID`, or a verified token-derived ID according to the existing command contract.

## Display Name Contract

Creation accepts an optional `--display-name <name>`.

- An omitted display name sends no explicit name. The backend presents the assigned tenant ID as the effective display name.
- A non-empty display name must contain 4–64 ASCII letters, digits, or hyphens, start and end with a letter or digit, and match `^[A-Za-z0-9][-A-Za-z0-9]{2,62}[A-Za-z0-9]$`.
- Leading or trailing whitespace is invalid and must not be silently removed.
- An explicitly supplied empty flag value is a usage error rather than an omitted value.
- `ti` performs the same validation locally so `--dry-run` and normal execution fail consistently before any request.
- The backend remains authoritative and can reject a value even after local validation.
- A backend HTTP 409 name collision maps to a stable `fs.display_name_conflict` error.

Backend display-name uniqueness is best-effort. The preflight duplicate query and tenant insert are not one atomic operation, and no database unique constraint exists. Two concurrent create requests can therefore produce duplicate display names. `ti` must not claim strong uniqueness, resolve a resource by display name, or retry a create under a different name automatically.

The list form `--display-name <substring>` applies a server-side contains filter to the backend's effective display name. `ti` rejects `%`, `_`, control characters, and empty values so users cannot accidentally invoke SQL `LIKE` wildcard behavior. The filter is not an exact lookup and does not change pagination semantics.

## Label Contract

Creation accepts repeatable `--label <key=value>` flags.

- At most 30 labels are allowed.
- Duplicate keys are rejected locally. Do not silently choose the first or last value.
- A key uses the backend's Kubernetes qualified-name contract: an optional lowercase DNS prefix of at most 253 bytes and `/`, followed by a 1–63 byte name using ASCII letters, digits, `-`, `_`, or `.`, starting and ending with a letter or digit.
- A value may be empty. A non-empty value is at most 63 bytes and uses ASCII letters, digits, `-`, `_`, or `.`, starting and ending with a letter or digit.
- `ti` validates labels locally for deterministic dry-run behavior; the backend remains authoritative.
- Labels are metadata visible through organization inventory. Documentation must tell users not to store passwords, tokens, connection strings, private paths, or other secrets in labels.

List accepts at most one `--label <key=value>` filter because the backend currently exposes one exact key/value query. `ti` converts it to the backend query syntax `label=<key>==<value>`. Multiple label filters are out of scope until the server defines their AND/OR semantics.

## Direct HTTP Contracts

Add typed requests and responses under `internal/api/fs`, preferably in `admin_tenant.go`. Do not reuse untyped maps or parse JSON with ad hoc string operations.

### Create

```http
POST /v1/admin/tenants
X-TiDBCloud-Public-Key: <public-key>
X-TiDBCloud-Private-Key: <private-key>
Content-Type: application/json
```

```json
{
  "display_name": "agent-workspace",
  "label": {
    "environment": "production",
    "team": "ai"
  }
}
```

Credentials are sent through the existing TiDB Cloud credential headers. Do not duplicate them in the JSON body. Empty optional metadata fields should be omitted when practical; the client must accept the backend's effective display name and normalized empty label object in the response.

Expected response:

```json
{
  "tenant_id": "<file-system-id>",
  "display_name": "agent-workspace",
  "label": {
    "environment": "production",
    "team": "ai"
  },
  "api_key": "<owner-token>",
  "status": "provisioning",
  "cloud_provider": "aws",
  "region": "ap-southeast-1"
}
```

`ti` maps `tenant_id` to `file_system_id`, `api_key` to `fs_token`, and backend `label` to the user-facing `labels` object.

### List

```http
GET /v1/admin/tenants?page_size=100&page=1&display_name=workspace&label=environment%3D%3Dproduction
X-TiDBCloud-Public-Key: <public-key>
X-TiDBCloud-Private-Key: <private-key>
```

The client follows `next_page` until zero using the existing repeated/regressing-page protections. Every page receives the same filters. It joins the remote inventory with the local ID-keyed credential store only to compute `has_local_token`; local state never adds, removes, or renames remote resources.

Quota and usage are returned by the backend for every list item. `ti` does not send or expose an `include_quota` request switch.

### Describe

```http
GET /v1/admin/tenants/<file-system-id>
X-TiDBCloud-Public-Key: <public-key>
X-TiDBCloud-Private-Key: <private-key>
```

The requested ID must equal the returned `tenant_id`. A mismatch is a response-contract error. `has_local_token` is derived locally without changing the remote result.

### Delete

```http
DELETE /v1/admin/tenants/<file-system-id>
X-TiDBCloud-Public-Key: <public-key>
X-TiDBCloud-Private-Key: <private-key>
```

Use credential headers and no credential body. Preserve the current behavior: remove matching local credentials only after the backend accepts deletion, and render the asynchronous status as `deleting`. Metadata does not change deletion selection or confirmation behavior.

## Creation Flow

The new creation flow is:

1. Load the selected profile and validate TiDB Cloud public/private keys.
2. Resolve the effective canonical region and hosted File System endpoint.
3. Parse and validate display name and labels.
4. Send `POST /v1/admin/tenants` directly from `ti`.
5. Validate the returned tenant ID and non-empty owner token.
6. Persist the owner token in the existing ID-keyed credential store with mode `0600`.
7. Return the one-time token in the command result exactly as today.
8. If `--wait` is present, invoke the existing resource-scoped `ti-drive9` root-stat readiness loop with the newly stored token.

Direct creation no longer needs a temporary Drive9 HOME or a temporary Drive9 context. A readiness failure retains the created resource and local credential and includes the file system ID in the error, matching the existing `--wait` safety contract.

The command must not expose backend quota or spending-limit inputs. Existing product policy that users cannot specify a TiDB Cloud spending limit for File System provisioning remains unchanged.

## Output

Create JSON adds metadata while retaining the existing token contract:

```json
{
  "file_system_id": "<file-system-id>",
  "display_name": "agent-workspace",
  "labels": {
    "environment": "production"
  },
  "region_code": "aws-ap-southeast-1",
  "fs_token": "<owner-token>",
  "status": "provisioning",
  "credentials_stored": true
}
```

List and describe always render a non-empty effective `display_name` and a non-null `labels` object. An old resource with no explicit metadata appears with `display_name` equal to its ID and `labels: {}`. No client-side migration or synthesized local name is needed.

Text list output becomes:

```text
FILE_SYSTEM_ID  DISPLAY_NAME     REGION                  STATUS  KIND  LOCAL_TOKEN
<id>            agent-workspace  aws-ap-southeast-1      active  live  true
```

Do not add labels as a list-table column because arbitrary key/value maps make the table unstable and excessively wide. Text describe output includes labels in deterministic key order, for example:

```text
File system ID: <id>
Display name: agent-workspace
Labels: environment=production, team=ai
Region: aws-ap-southeast-1
Status: active
Kind: live
Local token: true
```

An empty map renders as `Labels: none` in text output.

## Dry Run

`ti fs create-file-system --dry-run` changes its planned request from `/v1/provision` to `/v1/admin/tenants`. It validates credentials, endpoint resolution, display name, labels, and local credential-store readiness without sending a request or writing files.

The dry-run request body may show `display_name` and `label`, but it must not contain public/private key values. The result describes TiDB Cloud API-key authentication without rendering credential headers. `--wait` is reported as a post-create readiness action and does not run during dry-run.

Delete dry-run keeps its existing `/v1/admin/tenants/<id>` path and local credential-removal plan, but no longer describes companion execution.

Read-only list and describe continue to reject `--dry-run`.

## Error Handling

Map backend and local failures to stable `ti` errors without changing JSON success output:

| Condition | Expected behavior |
| --- | --- |
| Invalid display name | `fs.invalid_display_name`, usage exit code `2` |
| Invalid label syntax, key, value, count, or duplicate | `fs.invalid_label`, usage exit code `2` |
| Missing TiDB Cloud credentials | Existing authentication-required error |
| HTTP 401 | Existing TiDB Cloud authentication error |
| HTTP 403 | Existing FS authorization/quota error mapping with backend detail |
| HTTP 404 for admin API root | `fs.control_plane_unavailable` with region context |
| HTTP 404 for an item | Existing `fs.resource_not_found` behavior |
| HTTP 409 display-name conflict | `fs.display_name_conflict` |
| HTTP 429 | Retryable remote API error; do not retry mutation automatically |
| HTTP 5xx | Remote API error preserving request ID when available |
| Invalid or mismatched response ID | `fs.api_contract` |
| Missing create token | `fs.api_contract`; do not write local state |

Do not fall back to `/v1/provision` when the admin endpoint is unavailable. A fallback could create an unnamed and unlabeled resource after the user explicitly requested metadata, making the operation nondeterministic across regions. It could also turn an ambiguous network retry into a duplicate resource. Report the failure and let the user decide whether to retry.

Create remains non-idempotent unless the backend later adds an idempotency contract. `ti` must not automatically retry a request after it may have reached the backend.

## Authentication And Security

- Load TiDB Cloud keys through existing profile/environment precedence. Do not add new credential files or environment variables.
- Reuse the existing `X-TiDBCloud-Public-Key` and `X-TiDBCloud-Private-Key` header contract already used by FS token management.
- Ensure HTTP debug logging, operation logging, errors, telemetry, and dry-run output never contain credential header values or the returned owner token.
- Telemetry may record command path and flag names according to the existing policy. It must not record display names, label keys, label values, file system IDs, or filters.
- The create result intentionally returns the one-time owner token to stdout. This existing behavior remains the only supported plaintext delivery path in addition to mode-`0600` local credential persistence.
- Metadata is organization-visible and is not a secret store.

## Local State And Migration

Do not persist display names or labels in `~/.ti/config`, `~/.ti/credentials`, or the File System credential registry. The backend is authoritative and list/describe read current metadata remotely.

The existing credential layout remains unchanged:

```text
~/.ti/fs_credentials/<profile-key>/<file-system-id-key>/credentials
```

No migration is required:

- Existing local credentials remain keyed by immutable file system ID.
- Existing remote resources receive the backend's tenant-ID display fallback and empty labels.
- A clean machine can list and describe metadata using only TiDB Cloud credentials and region selection.
- Losing local state does not lose metadata.

## Package Design

### `internal/api/fs`

- Add typed admin tenant request, response, pagination, and filter models.
- Add methods for create, list, get, and delete.
- Centralize TiDB Cloud credential-header injection with the existing FS token-management header contract.
- Encode query parameters with `net/url`; never concatenate filter strings into URLs manually.
- Use the shared `internal/api.Client` request, response, request-ID, and error handling.

### `internal/fs`

- Extend `CreateFileSystemOptions` with display name and parsed labels.
- Introduce list options containing optional display-name and label filters.
- Extend `FileSystemResult` and `FileSystemSummary` with display name and labels.
- Move create/list/describe/delete orchestration from companion methods to direct API methods.
- Keep local credential joining, credential deletion, readiness waiting, endpoint resolution, and text formatting in the existing ownership boundaries.
- Remove unused companion-only create temporary-HOME code and admin inventory parsing after the direct path is covered.

### `internal/cli`

- Add optional `--display-name` and repeatable `--label` flags to create.
- Add optional `--display-name` and single `--label` filters to list.
- Parse flags through command context helpers and pass typed options to `internal/fs`.
- Keep permissions unchanged: create uses `FSVolumeCreate`, list/describe use `FSVolumeRead`, and delete uses `FSVolumeDelete`.

### Documentation

- Update `README.md` whenever code is implemented.
- Update PingCAP Preview command reference pages for create, list, and describe.
- Explain that display name is presentation metadata, not a resource selector.
- Document label validation, organization visibility, and the absence of metadata updates.
- Update completed FS inventory documentation only through an explicit superseding note if historical behavior would otherwise mislead current readers; do not rewrite historical implementation records mechanically.

## Dependencies And Portability

No new third-party Go package is required. Use the existing shared HTTP client, endpoint resolver, profile loader, API error model, and Go standard packages such as `net/http`, `net/url`, `regexp`, `sort`, and `encoding/json`.

The change adds no cgo dependency, daemon, mount requirement, or platform-specific implementation. Direct control-plane behavior must work identically on macOS, Linux, and Windows. The Drive9 companion remains required for data-plane and mount workflows but is no longer required merely to create, list, describe, or delete a remote File System.

`ref/fs` and `ref/drive9` remain reference-only. Do not import their packages or copy them into the module dependency graph.

## Testing

### API Client Tests

- Create uses `POST /v1/admin/tenants`, credential headers, metadata-only JSON, and decodes display name, labels, token, provider, region, and status.
- List encodes page, page size, display-name substring, and exact label query correctly.
- Get and delete encode escaped item paths and credential headers.
- Credential values never appear in request summaries or returned errors.
- Unknown response fields remain forward-compatible; required identity/token fields are validated by the service layer.

### Service Tests

- Create without metadata stores the returned token and accepts ID fallback display names.
- Create with metadata preserves normalized response metadata.
- Duplicate label keys, invalid keys/values, too many labels, invalid names, and explicit empty flags fail before network access.
- A failed credential write reports the created ID/token result according to the existing partial-success contract and never deletes the remote resource.
- `--wait` uses the created token and preserves credentials after timeout.
- List exhausts pagination, applies filters to every page, rejects repeated pages/IDs, sorts results deterministically, and joins local token state by ID.
- Describe rejects a mismatched response ID and renders empty labels deterministically.
- Delete removes only the selected local credential after HTTP 202 and preserves it on failure.
- HTTP 409, item 404, admin-root 404, auth, quota, and 5xx errors map to stable codes.
- No test imports or executes code under `ref/`.

### CLI And Black-Box Tests

- Help shows optional metadata/filter flags with the repository's required/optional usage formatting.
- JSON, text, and JMESPath query output include display name and labels correctly.
- Dry-run uses the admin path, validates metadata, and contains no credentials.
- Existing create/list/describe/delete invocations without new flags remain valid.
- Fake-server tests verify the direct API path and prove `ti-drive9` is not invoked for the four migrated control-plane commands.
- Existing data-plane and mount tests prove companion routing is unchanged.

### Live E2E

Extend the existing `make live-e2e-fs` lifecycle without deleting pre-existing resources:

1. Create one uniquely named `ti-e2e-fs-*` resource with labels identifying the test run and use `--wait`.
2. Verify the create response returns the same effective display name and labels plus a non-empty one-time token.
3. List by display-name substring and exact label and find only the created resource among matching results.
4. Describe by ID and verify metadata, status, quota presence, and local-token state.
5. Exercise the existing data-plane/mount flow through the companion to prove the directly created owner token is compatible.
6. Delete only that resource and verify local credentials are removed after acceptance.

If a regional backend has not deployed the admin metadata contract, the live test must fail with the explicit control-plane-unavailable error. It must not silently fall back or skip metadata assertions.

## Acceptance Criteria

- `ti fs create-file-system` uses `POST /v1/admin/tenants` directly and no longer runs `ti-drive9 create`.
- `ti fs list-file-systems`, `describe-file-system`, and `delete-file-system` use the direct admin tenant API.
- Create accepts optional validated display name and repeated labels.
- List supports server-side display-name and exact single-label filters.
- JSON and text output expose backend display metadata without persisting a second local inventory.
- Existing resources render with ID fallback display names and empty labels without migration.
- Resource selection remains ID/token based; display name is never accepted as identity.
- No create fallback can silently discard metadata.
- Data-plane, mount, layers, Git, journal, vault, pack, and unpack remain delegated to `ti-drive9`.
- Tests cover API contracts, validation, pagination, output, credential safety, no-companion control-plane routing, and a real create-to-delete lifecycle.

## Out Of Scope

- Updating display name or labels after creation; the backend exposes no PATCH contract.
- Strong concurrent display-name uniqueness.
- Selecting, mounting, deleting, or issuing tokens by display name.
- Multiple label predicates or arbitrary label expressions in one list request.
- Client-side metadata caching or persistence.
- Adding quota or spending-limit flags to File System creation.
- Anonymous File System provisioning.
- Modifying or releasing Drive9 CLI binaries.
- Reimplementing any filesystem data-plane or mount behavior in `ti`.
