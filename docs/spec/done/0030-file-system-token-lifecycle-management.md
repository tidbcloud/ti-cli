# File System Token Lifecycle Management

## Goal

Add explicit server-backed lifecycle management for TiDB Cloud Filesystem tokens. A single File System can have multiple owner and scoped tokens, while each local ti profile continues to select at most one token per File System for ordinary data-plane and mount commands.

This spec uses the Drive9 `/v1/tokens` API exactly as it exists after `tidbcloud/fs#49`. The backend will not add token introspection, current-token identification, token fingerprints, unique token names, mount inventory, or additional recovery APIs for this work. The client must not infer information that the backend does not return.

## Backend Contract And Constraints

The hosted FS backend exposes one `/v1/tokens` resource family:

| Method and path | Accepted identity | Behavior |
| --- | --- | --- |
| `POST /v1/tokens/generate` | TiDB Cloud API keys | Generate an owner token for one File System. |
| `GET /v1/tokens` | TiDB Cloud API keys or an owner token | List token metadata. |
| `POST /v1/tokens/{token_id}/activate` | TiDB Cloud API keys or an owner token | Change `disabled` to `active`. |
| `POST /v1/tokens/{token_id}/deactivate` | TiDB Cloud API keys or an owner token | Change `active` to `disabled`. |
| `DELETE /v1/tokens/{token_id}` | TiDB Cloud API keys or an owner token | Permanently change the token to `revoked`. |
| `POST /v1/tokens/refresh` | The current owner or `fs_scoped` bearer token | Rotate that same token and return the new plaintext once. |
| `POST /v1/tokens` | An owner token | Issue a path-and-operation-limited `fs_scoped` token. |

The backend model has these constraints:

- One File System can have multiple owner tokens and multiple `fs_scoped` tokens.
- The active/disabled non-expired token cap is 100 per File System. Expired and revoked tokens do not count against the cap under the backend rules.
- Stored states are `active`, `disabled`, and terminal `revoked`; `expired` is derived from `expires_at`.
- Revoked tokens are not returned by list. Single-FS list excludes expired tokens unless `include_expired` is requested.
- Token names are not unique. `token_id` is the only mutation identifier.
- Generate and refresh return plaintext only once. List never returns plaintext, ciphertext, or a token hash.
- The token JWT contains the File System ID as `tenant_id`, but it does not contain `token_id`, `key_name`, `scope_kind`, or path scopes.
- Disable, delete, and refresh can take up to the backend authentication-cache TTL, currently approximately 10 seconds, to become effective on every server process.
- Refresh is not idempotent. A committed refresh whose response is lost cannot be recovered with the old token. Recovery requires generating another owner token with TiDB Cloud API keys.
- The backend does not know where a token is mounted or which machines currently use it.

## Product Decisions

- `ti fs list-file-system-tokens` always requires `--file-system-id`. It does not implicitly list token metadata across the whole organization.
- All token mutations that identify a remote token by ID require both `--file-system-id` and `--token-id`.
- The CLI uses `file_system_id` in flags and output. It translates that value to the backend `tenant_id` field internally and does not expose a second tenant selector.
- The remote backend is the source of truth for token inventory and lifecycle state.
- Local state remains a selected operational credential, not a replica of all remote tokens and not a multi-token wallet.
- A profile may store one selected token for each File System. Different profiles may store different tokens for the same File System.
- Token management never changes which File System is selected implicitly. Existing explicit FS ID and token-derived ID rules remain in force.
- Control-plane token management uses TiDB Cloud public/private keys only. It must not also send the selected local FS token.
- Self-refresh uses one FS bearer token only. It must not also send TiDB Cloud public/private keys.
- ti never guesses a remote `token_id` from token name, issuance time, list ordering, profile, or the number of returned rows.
- An old local credential with no known `token_id` remains valid for data-plane use but cannot be correlated with one list row.
- No background refresh, automatic expiry renewal, token daemon, or automatic remote revocation is introduced.
- Owner token management is the first-phase creation surface. This spec does not add a ti command for issuing new path-level `fs_scoped` tokens. Existing scoped tokens can appear in list and can be managed by token ID through TiDB Cloud credentials. A separate spec can expose scoped issuance after its local import and scope-display contract is designed.

## User-Facing Commands

Add these commands:

```text
ti fs generate-file-system-token
ti fs list-file-system-tokens
ti fs enable-file-system-token
ti fs disable-file-system-token
ti fs delete-file-system-token
ti fs refresh-file-system-token
```

Generate an owner token with a finite TTL:

```bash
ti fs generate-file-system-token \
  --file-system-id <file-system-id> \
  --token-name ci-deploy \
  --ttl 24h
```

Generate an explicitly non-expiring owner token:

```bash
ti fs generate-file-system-token \
  --file-system-id <file-system-id> \
  --token-name local-owner \
  --no-expiration
```

Exactly one of `--ttl` and `--no-expiration` is required. The CLI does not silently choose a lifetime. `--ttl` accepts a positive Go duration that resolves to whole seconds and does not exceed the backend maximum of 365 days.

Store a newly generated token as the selected local credential:

```bash
ti fs generate-file-system-token \
  --file-system-id <file-system-id> \
  --token-name local-owner \
  --ttl 720h \
  --store-locally
```

If another local token already exists, `--store-locally` fails before the remote request. The user must explicitly add `--replace`. Replacing the local selection does not disable, delete, refresh, or otherwise change the previous remote token.

List token metadata for exactly one File System:

```bash
ti fs list-file-system-tokens --file-system-id <file-system-id>
ti fs list-file-system-tokens --file-system-id <file-system-id> --include-expired
```

The list command supports `--offset` and `--limit` using the backend single-tenant pagination contract. Offset defaults to 0, limit defaults to 50, and the maximum limit is 200. The response retains `next_offset` when another page might exist. It does not auto-fetch an unbounded expired-token history.

Manage a known token by immutable ID:

```bash
ti fs disable-file-system-token --file-system-id <file-system-id> --token-id <token-id>
ti fs enable-file-system-token --file-system-id <file-system-id> --token-id <token-id>
ti fs delete-file-system-token --file-system-id <file-system-id> --token-id <token-id>
```

These commands do not accept a token name as a selector and do not add confirm-name flags or prompts.

Refresh the token supplied for the current invocation:

```bash
ti fs refresh-file-system-token --file-system-id <file-system-id>
TI_FS_TOKEN=<current-token> TI_REGION_CODE=aws-us-east-1 ti fs refresh-file-system-token
```

For a supplied token, `--file-system-id` is an optional consistency assertion and must match the ID decoded from the token. When no flag or environment token is supplied, `--file-system-id` is required to load the selected local credential. An optional `--ttl` follows the backend refresh contract. Omitting it preserves the previous lifetime period; a non-expiring owner token remains non-expiring.

All remote mutations support `--dry-run`. Dry-run validates credential availability, region, File System ID, token ID, TTL rules, local storage preconditions, and known mount conflicts without sending a mutation or printing secret material.

## Authentication And Region Resolution

Generate, list, enable, disable, and delete use the selected profile's TiDB Cloud API keys. Request credentials are sent through `X-TiDBCloud-Public-Key` and `X-TiDBCloud-Private-Key` headers, not copied into request JSON. These commands fail before the request if either key is missing.

Refresh resolves its FS token in the existing order:

1. Explicit non-empty `--fs-token`.
2. Non-empty `TI_FS_TOKEN`.
3. The selected local File System credential.

The effective region remains:

1. Explicit global `--region`.
2. `TI_REGION_CODE`.
3. The selected local credential region.
4. The profile region.

The region selects one hosted FS backend deployment. Token management does not scan other regions, accept a server URL, or retry against another region.

The API client must use separate constructors or explicit credential modes for control-plane and bearer requests. A generic helper must not attach both credential classes because the backend correctly rejects ambiguous requests with HTTP 400.

## API Call Chains

Generate:

```text
ti fs generate-file-system-token
  -> validate explicit File System ID, token name, lifetime, and local-store preconditions
  -> resolve hosted FS endpoint from the effective ti region
  -> POST /v1/tokens/generate
       headers: X-TiDBCloud-Public-Key, X-TiDBCloud-Private-Key
       body: tenant_id, key_name, ttl_seconds when finite
  -> map tenant_id to file_system_id and token to fs_token
  -> optionally store the returned token locally
  -> render the one-time secret response
```

List:

```text
ti fs list-file-system-tokens --file-system-id <id>
  -> resolve TiDB Cloud credentials and hosted FS endpoint
  -> GET /v1/tokens?tenant_id=<id>&offset=<n>&limit=<n>[&include_expired=1]
  -> map backend metadata to the ti output model
  -> render without secret material
```

Enable and disable:

```text
ti fs enable-file-system-token | disable-file-system-token
  -> validate file_system_id and token_id
  -> inspect known local mount and credential metadata
  -> POST /v1/tokens/<token_id>/activate|deactivate?tenant_id=<id>
       headers: X-TiDBCloud-Public-Key, X-TiDBCloud-Private-Key
  -> keep local plaintext unchanged
  -> render the accepted remote state
```

Delete:

```text
ti fs delete-file-system-token
  -> validate file_system_id and token_id
  -> inspect known local mount and credential metadata
  -> DELETE /v1/tokens/<token_id>?tenant_id=<id>
       headers: X-TiDBCloud-Public-Key, X-TiDBCloud-Private-Key
  -> remove the selected local credential only when its stored token_id matches
  -> render revoked state and local cleanup result
```

Refresh:

```text
ti fs refresh-file-system-token
  -> resolve exactly one bearer token and assert its decoded File System ID
  -> reject a matching active local mount
  -> prepare local atomic-write recovery state when the source is local
  -> POST /v1/tokens/refresh
       Authorization: Bearer <current-token>
       body: ttl_seconds only when explicitly provided
  -> receive the rotated plaintext and token_id once
  -> atomically replace the selected local credential only when the source was local
  -> render the new token and storage outcome
```

`ti fs create-file-system` remains unchanged by this spec. It continues to invoke `ti-drive9 create --json` with a temporary companion HOME, capture the returned `tenant_id` and `api_key`, discard the temporary Drive9 context, and store the owner token in ti's credential store. Normal data-plane and mount commands continue to inject the selected ti credential as `DRIVE9_API_KEY`; they do not depend on a persistent Drive9 context.

The current provision response does not expose `token_id`. Therefore a token stored by create starts with unknown remote token metadata. This spec must not rotate it merely to discover its ID.

## Local Credential Model

Keep one credential file per profile and File System. Extend its schema with optional metadata:

```toml
file_system_id = "<file-system-id>"
region_code = "aws-us-east-1"
api_key = "drive9_..."

token_id = "<token-id>"
scope_kind = "owner"
token_name = "local-owner"
expires_at = "2026-09-11T00:00:00Z"
```

The existing `api_key` key remains unchanged for compatibility. `token_id`, `scope_kind`, `token_name`, and `expires_at` are optional because create and old imports cannot discover them with the available backend APIs.

Do not persist remote `status` as an authoritative local value. Another machine can enable, disable, delete, or refresh a token at any time, making such a cached status stale.

Local metadata is populated only from authoritative responses:

- Generate with `--store-locally` stores every returned field.
- Refresh of a local credential updates token plaintext, token ID, scope kind, and expiry from the refresh response. It preserves a known local token name because refresh does not return it.
- Existing create and import paths keep optional metadata empty unless the command already possesses an authoritative response containing it.
- List never guesses which row matches an unknown local token.
- Enable and disable do not change local plaintext.
- Delete removes local credentials only when the stored `token_id` exactly matches the deleted ID.

When a delete targets a token while the local credential has no `token_id`, ti leaves the local file unchanged and returns `local_credentials_updated: false` with reason `local_token_id_unknown`. A later data-plane 401 must explain that the selected token might be disabled, expired, refreshed elsewhere, or revoked and recommend generating or importing a valid token. It must not claim which state caused the 401.

Credential schema migration is read-compatible and lazy:

- Existing files continue to load without rewriting.
- Missing optional fields do not make a credential incomplete.
- A successful generate/store or local refresh writes the expanded schema atomically.
- No migration performs a remote request, refresh, token generation, or list-based guess.

## Generate And Local Replacement Safety

Generate does not store locally by default because additional tokens are commonly created for CI, sandboxes, or another machine. The plaintext remains available in the successful structured response exactly once.

When `--store-locally` is requested:

1. Validate the credential directory and requested replacement policy before the API call.
2. Prepare a mode-`0600` temporary file in the target directory where POSIX modes apply.
3. Generate the remote token.
4. Write the complete credential to the temporary file, flush it, and atomically rename it over the target.
5. If storage fails after generation, attempt to delete only the just-generated `token_id` with the same TiDB Cloud credentials.
6. If rollback succeeds, return a storage error and do not expose a token that has already been revoked.
7. If rollback fails, return the generated result with `credentials_stored: false`, a stable partial-success error, and actionable guidance. The plaintext must remain recoverable from structured stdout and must never be duplicated into logs or stderr.

Replacing the local token does not modify the old remote token. Output and documentation must state that the old token remains active until explicitly disabled or deleted.

## Refresh And Atomic Local Replacement

Refresh is an explicit destructive rotation of one credential, not an ordinary read or lease check. The backend commits the new token before the client can persist it, so the client must minimize but cannot eliminate the response-loss window.

For a locally sourced token:

1. Acquire a cross-process lock for the credential file.
2. Re-read the credential and verify that its token is the same token resolved before locking.
3. Validate that the directory is writable and prepare mode-`0600` recovery state in the same directory.
4. Confirm that no known local mount uses the same token fingerprint.
5. Call refresh once. Never retry automatically after an ambiguous network failure.
6. Write the new token and returned metadata to recovery state, flush it, and atomically rename it over the credential file.
7. If final persistence fails, preserve the recovery file when possible, return its path without token contents, and include the new plaintext in structured stdout with `credentials_stored: false`.

For a flag or environment token, refresh never changes a local credential. It returns `credentials_stored: false` and the new token so the caller can update its environment, CI secret, or sandbox input.

The new token is always rendered in the successful refresh JSON because it cannot be retrieved later. `--query fs_token --output text` remains the explicit way to print only the token. Refresh output, recovery paths, errors, operation logs, and telemetry must never print the old token.

## Mount Safety

A running Drive9 mount retains the credential with which it started. Updating ti's credential file cannot inject a new token into that process. Refreshing, disabling, or deleting that token can therefore turn an apparently healthy mount into delayed authentication failures.

Extend new mount locators with optional non-secret correlation metadata:

```json
{
  "file_system_id": "<file-system-id>",
  "token_id": "<token-id-when-known>",
  "token_fingerprint": "<local-non-authenticating-fingerprint>"
}
```

The fingerprint is derived locally from the selected token, truncated to at least 128 bits of a cryptographic hash, and used only to compare a refresh input with local mounts. It must not be sent to telemetry, operation logs, remote APIs, or user-facing output. It grants no authentication authority and never replaces the plaintext credential.

Mount behavior:

- New mounts store `token_id` when the selected local credential knows it and always store the local fingerprint.
- Existing locators without these fields remain valid for drain and unmount.
- Refresh computes the supplied token fingerprint and fails before the API call if a local mount locator for the same File System has the same fingerprint. The error provides exact drain and unmount commands. No force bypass is added in the MVP.
- Disable and delete fail before the API call when their `token_id` matches a known active local mount locator.
- A mount with unknown token ID cannot be correlated with an ID-based disable/delete request. ti must not guess or block every unrelated token mutation.
- Generate, list, and enable do not invalidate an active mount and are not blocked.

Remote mounts and processes on another machine cannot be detected. Documentation must recommend generate-distribute-disable-delete rotation for shared tokens:

1. Generate a new named token.
2. Distribute it to every consumer.
3. Verify each consumer with the new token.
4. Disable the old token.
5. Observe for unexpected failures during the rollback window.
6. Delete the old token after the transition is accepted.

Refresh is documented only for a single credential holder that can atomically replace its secret and has no active mount using it.

## Output Contracts

Generate JSON:

```json
{
  "file_system_id": "tnt_abc123",
  "token_id": "4b2d97e8-3e72-4ba6-8db1-1ca2d7370c20",
  "token_name": "ci-deploy",
  "scope_kind": "owner",
  "status": "active",
  "issued_at": "2026-08-12T00:00:00Z",
  "expires_at": "2026-08-13T00:00:00Z",
  "fs_token": "drive9_...",
  "credentials_stored": false
}
```

List JSON:

```json
{
  "file_system_id": "tnt_abc123",
  "tokens": [
    {
      "token_id": "4b2d97e8-3e72-4ba6-8db1-1ca2d7370c20",
      "token_name": "ci-deploy",
      "scope_kind": "owner",
      "status": "active",
      "expired": false,
      "issued_at": "2026-08-12T00:00:00Z",
      "expires_at": "2026-08-13T00:00:00Z",
      "created_at": "2026-08-12T00:00:00Z",
      "updated_at": "2026-08-12T00:00:00Z"
    }
  ],
  "next_offset": 50
}
```

List text output uses one row per token:

```text
TOKEN_ID                              NAME       SCOPE      STATUS    EXPIRES_AT
4b2d97e8-3e72-4ba6-8db1-1ca2d7370c20  ci-deploy  owner      active    2026-08-13T00:00:00Z
0d716939-f896-420c-a3f9-68310345f17d  agent-ro   fs_scoped  disabled  2026-08-13T00:00:00Z
```

If the backend returns `expired: true`, text output displays `expired` as the effective status while JSON keeps the separate stored `status` and derived `expired` fields. Text output omits issuer metadata by default; JSON may preserve non-secret `issued_by_*` metadata.

List output does not claim which row is locally selected when the local credential lacks `token_id`. Do not display a guessed `LOCAL` marker.

Disable, enable, and delete output includes `file_system_id`, `token_id`, accepted status, whether local credentials changed, and a reminder that backend cache convergence may take approximately 10 seconds. The reminder must not corrupt JSON stdout; structured fields carry the state, while optional human guidance follows the existing output-mode rules.

## Errors

Add stable client error codes for:

- `fs.token_name_required`: generate omitted an explicit token name.
- `fs.token_lifetime_required`: neither or both of `--ttl` and `--no-expiration` were supplied.
- `fs.token_ttl_invalid`: TTL is non-positive, sub-second, not whole-second representable, or over 365 days.
- `fs.token_id_required`: an ID-based mutation omitted `--token-id`.
- `fs.token_credentials_ambiguous`: client input would send both TiDB Cloud and FS bearer credentials.
- `fs.token_local_conflict`: local storage exists and `--replace` was not supplied.
- `fs.token_local_changed`: the selected local credential changed while refresh waited for its lock.
- `fs.token_mount_active`: refresh, disable, or delete would invalidate a known active local mount.
- `fs.token_refresh_ambiguous`: refresh may have committed but the response was lost; do not retry with the old token.
- `fs.token_partial_success`: remote generation or refresh succeeded but secure local persistence and automatic recovery did not complete.

Map backend responses without hiding their meaning:

- 400: invalid/ambiguous request.
- 401: missing, invalid, disabled, expired, refreshed, or revoked FS bearer; do not guess the exact state without control-plane metadata.
- 403: insufficient TiDB Cloud role, organization mismatch, or identity not allowed for the endpoint.
- 404: File System or token not found, or token management unavailable in that deployment.
- 409: terminal token, expired activation, token limit, concurrent refresh, or lifecycle conflict.

An expired disabled token cannot be re-enabled. The error must recommend generating a new token rather than refreshing or repeatedly enabling it.

## Package And Code Design

- `internal/api/fs` adds strict wire models and methods for generate, single-FS list, activate, deactivate, delete, and refresh. Wire structs retain backend field names; mapping to ti output happens above the client.
- `internal/fs/tokenmgmt` owns token lifecycle use cases, identity separation, TTL conversion, pagination, output models, partial-success handling, and local reconciliation.
- `internal/fs/fscred` extends the optional credential metadata, cross-process locking, recovery-file creation, atomic replacement, and lazy compatibility behavior.
- `internal/fs/mountlocator` adds optional token ID and local fingerprint fields without changing existing locator loading or unmount behavior.
- `internal/cli` registers the six commands, long flags, required annotations, dry-run handlers, and shared output/query behavior.
- `internal/authz` adds explicit permissions for token list, generate, enable, disable, delete, and refresh. Do not infer permission from the command name.
- Existing `internal/fswrap` and `ti-drive9` routing remain unchanged for create, data-plane, Git, Journal, Vault, layers, pack/unpack, and mount commands.

Do not import or depend on `ref/fs` or `ref/drive9`. They remain reference-only.

## Dependencies And Portability

- No new Go module is required. Use the standard library HTTP, JSON, duration, hashing, and file APIs plus existing ti config/output helpers.
- No cgo requirement is introduced.
- Control-plane token management is platform-neutral on macOS, Linux, and Windows.
- POSIX permission checks use mode `0600`; Windows uses the existing credential-store security behavior.
- Mount availability remains controlled by the bundled Drive9 companion and host FUSE/WebDAV support.

## Security Requirements

- Never write FS token plaintext to operation logs, telemetry, debug output, mount locators, non-secret config, errors, test failure dumps, or HTTP traces.
- The accepted flag name may be logged; its value may not.
- Redact `Authorization`, TiDB Cloud credential headers, request credential fields, `fs_token`, and backend `token` fields from debug output.
- Generate and refresh return plaintext only through successful or partial-success structured stdout. Do not duplicate it to stderr.
- `--dry-run`, list, enable, disable, and delete never output plaintext.
- Token files and recovery files are owner-only and are never committed.
- Token names are operational metadata, not secrets. Users must not put passwords or tokens in a token name.
- Local fingerprints are correlation metadata only and never leave the local mount locator boundary.
- Deleting one token never deletes the File System or any other token.
- Local replacement never revokes the previous remote token implicitly.

## Testing

Unit tests must cover:

- Exact method, path, query, headers, and JSON body for every backend endpoint.
- Control-plane requests never contain an FS bearer; refresh never contains TiDB Cloud credentials.
- Missing/both lifetime flags, TTL rounding, sub-second TTL, overflow, and 365-day boundary.
- Required explicit File System ID on list and ID-based mutations.
- Backend `tenant_id` to ti `file_system_id` and `key_name` to `token_name` mapping.
- List pagination, include-expired behavior, revoked omission as returned by the backend, and text effective-expired status.
- List and every non-secret command omit plaintext even when the fake server sends malicious unexpected fields.
- Generate default does not write local state.
- `--store-locally`, pre-existing conflict, explicit replace, secure atomic write, rollback success, rollback failure, and partial-success output.
- Old credential files with no optional metadata continue to load without rewrite.
- Generate/store and local refresh populate authoritative optional metadata.
- Delete removes a matching known local token and preserves unknown or non-matching local tokens.
- Disable preserves local plaintext.
- Flag/environment refresh never rewrites local credentials.
- Local refresh lock, concurrent local change detection, atomic replacement, recovery-file behavior, and no automatic network retry.
- Token-derived File System ID assertion and region precedence.
- Mount locator fingerprint matching, known token-ID matching, old locator compatibility, and exact drain/unmount guidance.
- Secret redaction in errors, debug, dry-run, operation logs, telemetry, and failed fake-server responses.

Black-box e2e uses a fake FS server and fake companion to verify command help, required flags, output/query behavior, credential precedence, local file modes, and mount guards without live credentials.

Live e2e must use a uniquely generated token on the temporary test File System and must not mutate the provision token or any pre-existing token:

1. Generate a uniquely named finite-TTL owner token.
2. List the exact File System and verify the generated token metadata and absence of plaintext.
3. Perform a data-plane read using the generated token.
4. Disable the generated token, wait beyond the documented cache convergence window, and verify data-plane authentication is rejected.
5. Enable the same token and verify data-plane access returns.
6. Refresh the token with no active mount, verify the token ID is unchanged, verify the new token works, and verify the old token stops working after cache convergence.
7. Delete the refreshed token and verify it no longer authenticates or appears in default list.
8. Clean up only the token generated by the same test run, including failure cleanup through TiDB Cloud credentials.

The live test also exercises local store/replace in an isolated temporary `TI_HOME` and verifies that no plaintext reaches captured logs. It must tolerate the expected authentication-cache convergence delay without using an unbounded retry.

## Documentation Changes

When implemented, update README, PingCAP command references, examples, troubleshooting, security guidance, and AGENTS.md command inventory. Documentation must cover:

- One File System can have multiple tokens while one local profile selects one operational token per FS.
- The difference between an owner token and a path/operation-limited `fs_scoped` token.
- Token plaintext is visible only on generate/refresh.
- List is scoped to an explicit File System ID.
- Generate does not replace local credentials unless requested.
- Refresh cannot update an environment variable or remote secret store.
- Shared-token rotation uses generate, distribute, disable, then delete rather than refresh.
- Active mounts must be drained and unmounted before refreshing their token.
- Historical create/import credentials might not have a known remote token ID, and ti never guesses it.

## Dependencies

- `docs/spec/done/0018-fs-token-auth-and-config-free-access.md`
- `docs/spec/done/0020-explicit-file-system-selection.md`
- `docs/spec/done/0028-remote-fs-resource-inventory.md`
- Hosted FS backend deployment containing `tidbcloud/fs#49`

## Acceptance Criteria

- Users can generate, list, enable, disable, delete, and self-refresh FS tokens through ti using the existing backend API.
- Every list and ID-based mutation is explicitly scoped to one File System ID.
- Multiple remote tokens do not force ti to persist multiple local secrets.
- Existing create/import credentials remain usable without a token ID.
- ti does not guess local-to-remote token identity when metadata is unavailable.
- A local refresh securely replaces the selected credential and learns the returned token ID.
- Environment/flag refresh returns the new token without modifying local state.
- Known active local mounts are protected from token invalidation.
- Remote/shared consumer limitations and the safe staged rotation workflow are documented.
- No secret enters logs, telemetry, dry-run, non-secret config, mount locators, or list output.
- Unit, black-box e2e, and real live token lifecycle tests pass.

## Out Of Scope

- Backend changes, including `/v1/tokens/self`, current-token markers, list fingerprints, unique token names, mount inventory, or idempotent refresh.
- Local storage of every remote token.
- Automatic refresh, background renewal, token expiry notifications, or a credential daemon.
- Automatic distribution to CI, sandboxes, containers, remote hosts, or secret managers.
- Detecting mounts or processes on another machine.
- Generating new `fs_scoped` tokens through ti in this phase.
- Changing the existing `ti fs create-file-system` provisioning API or companion implementation.
