# Remote File System Resource Inventory

> **Superseding note:** `docs/spec/done/0031-fs-tenant-metadata-control-plane.md` later moved create, list, describe, and delete from companion commands to the typed admin tenant HTTP API and added organization-visible display names and labels. This document remains authoritative for ID-based selection, remote inventory ownership, local credential migration, and configuration-free access; its no-name and companion control-plane statements are historical.

## Goal

Make the Drive9 backend the source of truth for TiDB Cloud Filesystem resource inventory. Replace locally assigned file system names with one stable public identifier, `file_system_id`, whose value is the Drive9 tenant ID returned by provisioning.

The local machine must no longer decide whether a file system exists. Local state is limited to the one-time owner token returned by `ti fs create-file-system`, the region routing hint required to use that token, and derived Drive9 companion runtime state. Losing local state must not prevent a user with valid TiDB Cloud API keys from listing, describing, or deleting remote file systems.

This spec does not require Drive9 to return existing token plaintext or add token lifecycle APIs. Token generation, metadata listing, disable, enable, rotation, and revocation remain deferred until Drive9 exposes the required public API.

## Product Decisions

- A file system has no user-defined remote name.
- `file_system_id` is the public ti field and flag name. Its value is exactly the Drive9 tenant ID, for example `tnt_abc123`.
- Do not expose `tenant_id` as a second user-facing selector. Drive9 tenant terminology remains an implementation detail of TiDB Cloud Filesystem.
- Drive9 owns remote inventory, lifecycle status, and authorization binding.
- ti stores the owner token locally because it is returned only once and cannot be recovered through list or get APIs.
- `org:owner` and `project:owner` TiDB Cloud API keys are both accepted as organization-scoped operators, matching the current Drive9 authorization policy.
- Remote list, get, and delete are region-scoped. The effective ti region selects one Drive9 deployment. `--region` keeps its existing highest-priority override behavior.
- Do not add a server URL flag. Endpoint resolution continues to use the hosted Drive9 region manifest.

## User-facing Commands

The control-plane command surface becomes:

```bash
ti fs create-file-system
ti fs create-file-system --wait
ti fs list-file-systems
ti fs describe-file-system --file-system-id <file-system-id>
ti fs delete-file-system --file-system-id <file-system-id>
ti fs import-file-system-token
```

`ti fs create-file-system` removes `--file-system-name`. The result includes the server-selected identifier and the one-time token:

```json
{
  "file_system_id": "tnt_abc123",
  "region_code": "aws-us-east-1",
  "status": "provisioning",
  "fs_token": "drive9_...",
  "credentials_stored": true
}
```

All commands that select an existing file system replace `--file-system-name` with `--file-system-id`. This includes data-plane, mount, layer, `fs-git`, `fs-journal`, and `fs-vault` commands. The ID flag is optional only when an explicitly supplied FS token can identify the file system. Commands whose target is already identified by a mount path, such as drain and unmount, do not add a file system ID requirement.

Configuration-free access becomes:

```bash
TI_FS_TOKEN=drive9_... \
TI_REGION_CODE=aws-us-east-1 \
ti fs list-files --path /
```

The selector precedence for commands that require a file system is:

1. `--file-system-id`
2. `TI_FS_FILE_SYSTEM_ID`
3. The `tenant_id` claim derived from an explicitly supplied `--fs-token` or `TI_FS_TOKEN`, after Drive9 accepts that exact token
4. No implicit default; fail with `fs.missing_file_system_id`

When a flag or environment file system ID and an explicit token are both present, the supplied ID must match the verified token-derived ID. A mismatch fails before any data-plane operation. A token loaded from the local credential store cannot select its own record because an ID is required to locate that token in the 1:N store.

`TI_FS_FILE_SYSTEM_NAME` and `--file-system-name` are removed after the migration behavior in this spec is implemented and tested. They must not silently select a different resource.

## Import An Existing File System Token

`ti fs import-file-system-token` imports a known Drive9 owner or filesystem-scoped token into the selected ti profile namespace. It is a local credential operation: it does not create a remote file system, issue a new token, change token status, or require TiDB Cloud public/private keys.

The recommended invocation keeps the token out of command arguments:

```bash
TI_FS_TOKEN='drive9_...' \
ti fs import-file-system-token --region aws-us-east-1
```

Automation may instead read the token from an owner-only file:

```bash
ti fs import-file-system-token \
  --from-file ./fs-token \
  --region aws-us-east-1
```

On POSIX systems, `--from-file` accepts only a regular file with mode `0600` or stricter. `--from-file -` reads one token from stdin. The command also accepts the existing `--fs-token` flag for consistency, but documentation must prefer `TI_FS_TOKEN`, stdin, or an owner-only file because command arguments can be exposed through shell history and process inspection. Supplying more than one token source is a usage error.

The effective region follows the existing precedence of global `--region`, `TI_REGION_CODE`, and profile `region_code`. A region is required because Drive9 tokens are used only against the regional endpoint selected by ti. The command does not search other regions.

A Drive9 API key looks opaque because `drive9_` is an outer wrapper, not the JWT itself. Its structure is:

```text
drive9_<base64url-encoded-JWT>
```

After removing `drive9_` and Base64URL-decoding the remainder, the result is a standard signed JWT. Its payload includes `tenant_id`; ti maps that claim to the public `file_system_id`. Local decoding alone is not authentication because the payload is not yet signature-verified.

Import therefore uses this validation chain:

1. Parse the wrapper and JWT structure without logging or displaying any token bytes.
2. Extract the unverified `tenant_id` candidate and map it to `file_system_id`.
3. Run the bundled companion equivalent of `ti-drive9 fs stat --output json :/` with the exact token and selected regional endpoint in its sanitized environment.
4. Require Drive9 to accept the authenticated root metadata request. The Drive9 server verifies the signature, token version and status, and the token claim's binding to the authenticated tenant. ti does not depend on or reimplement the companion's underlying HTTP route.
5. Only after successful remote verification, atomically save the token under the derived `file_system_id`.

The status response does not need to echo the tenant ID. A successful response proves that Drive9 accepted the exact wrapped JWT whose payload ti decoded. A malformed, expired, revoked, disabled, wrong-region, or otherwise rejected token is not written locally.

The optional `--file-system-id <id>` is a caller assertion, not a second identity source. When provided, it must exactly equal the ID derived from the verified token or the command fails without writing. This is useful when an external system distributes the ID and token separately.

Import is idempotent and fail-closed:

- Importing the same ID, region, and token again succeeds without rewriting the credential.
- An existing entry for the same ID with a different token or region returns `fs.credential_import_conflict`.
- `--replace` explicitly permits replacement after the new token has passed remote verification. It never bypasses ID or region validation.
- `--dry-run` parses and remotely verifies the token and reports the derived ID and destination profile namespace without writing credentials or Drive9 context state.

Successful structured output never returns the token:

```json
{
  "file_system_id": "tnt_abc123",
  "region_code": "aws-us-east-1",
  "credentials_stored": true,
  "status": "imported"
}
```

After import, commands still require explicit file system selection. They resolve the token from the ID-keyed credential store, so users no longer need to pass or export the token repeatedly:

```bash
ti fs list-files --file-system-id tnt_abc123 --path /
ti fs mount-file-system --file-system-id tnt_abc123 --mount-path ./workspace
```

## Remote Inventory Behavior

`ti fs list-file-systems` requires TiDB Cloud API keys from the selected profile or supported environment variables. It invokes the bundled Drive9 companion equivalent of:

```bash
ti-drive9 admin tenant list \
  --region-code aws-us-east-1 \
  --tidbcloud-public-key <public-key> \
  --tidbcloud-private-key <private-key> \
  --json
```

The command follows Drive9 pagination until `next_page` is absent, uses the maximum supported page size, rejects repeated or regressing page values, and returns one aggregated deterministic result. Results are sorted by `file_system_id` so output does not depend on local directory order or backend page boundaries.

Example JSON:

```json
{
  "region_code": "aws-us-east-1",
  "file_systems": [
    {
      "file_system_id": "tnt_abc123",
      "status": "active",
      "kind": "live",
      "has_local_token": true
    },
    {
      "file_system_id": "tnt_def456",
      "status": "active",
      "kind": "live",
      "has_local_token": false
    }
  ]
}
```

`has_local_token` is a local capability hint, not remote resource state. It must never expose the token, token fingerprint, token length, local path, profile name, or Drive9 context name.

A resource returned by Drive9 must appear even when the machine has no local token. A local credential absent from the remote result must not appear as a remote file system. The command may return a separate warning count for unmatched legacy credentials, but it must not merge stale local entries into `file_systems`.

Token-only sandboxes cannot list the organization inventory because they do not have TiDB Cloud API keys. They can use only `TI_FS_TOKEN` and `TI_REGION_CODE` without a profile, local state, or separate file system ID. ti derives the ID from the token and Drive9 validates the binding on the first authenticated request. `TI_FS_FILE_SYSTEM_ID` remains an optional assertion when the sandbox receives the ID separately.

## Describe And Delete Behavior

`ti fs describe-file-system --file-system-id <id>` calls Drive9 tenant get with TiDB Cloud credentials. It does not require a local FS token. The output includes the canonical ti region, ID, status, kind, optional quota data returned by Drive9, and `has_local_token`.

`ti fs delete-file-system --file-system-id <id>` calls Drive9 admin tenant delete with TiDB Cloud credentials. It must not require the owner token. This allows a user to delete a remote file system after changing machines or losing local ti state.

Deletion remains asynchronous. After Drive9 accepts deletion, ti returns `status: "deleting"`. Only then may ti remove the new local credential entry for that ID and stop exposing it to new data-plane commands. Legacy registry files are not removed by this spec because they provide rollback safety.

Delete dry-run validates the profile, TiDB Cloud credentials, region, endpoint, ID syntax, and required permission without calling Drive9 or deleting local credentials. It reports the Drive9 admin tenant delete operation and whether matching local credentials would be deactivated after acceptance.

## Create And One-time Token Handling

`ti fs create-file-system` invokes the bundled Drive9 public `create --json` provisioning command with the effective region and TiDB Cloud keys. ti does not send a name or spending limit. Drive9 returns the tenant ID, owner API key, status, provider, and region. Creation does not use the admin tenant API: `/v1/provision` remains the public provisioning contract, while the admin tenant API provides organization-scoped inventory, describe, and deletion. Because Drive9 `create` also writes a local context, ti runs it with an owner-only temporary companion Home, reads the structured response, and removes that Home. The returned token is persisted only in the ti ID-keyed credential store; create must not accumulate a second persistent inventory of Drive9 owner contexts.

Before sending the create request, ti validates that the local credential directory is writable and that the Drive9 companion is available. After a successful response, ti atomically stores the owner token keyed by `file_system_id` and includes `fs_token` in the structured create result, preserving the existing ability to inject the token into a sandbox in one command.

If the remote create succeeds but local persistence fails, ti must not hide the accepted resource or discard the one-time token. It returns a successful structured result with `credentials_stored: false`, includes the `file_system_id` and `fs_token`, writes an actionable warning to stderr in text mode, and does not retry creation. This exceptional success result is necessary because the token cannot currently be regenerated.

Re-running create always requests a new remote resource. Local state must never make create return `exists`; idempotency cannot be inferred without a caller-supplied idempotency key or a server-side naming contract.

`--wait` retains its current behavior: after creation, ti uses the returned token against the selected Drive9 endpoint until the root is readable or the timeout expires. Timeout and cancellation retain the remote resource and local token.

## Local Credential Model

New local credentials live under:

```text
~/.ti/fs_credentials/<profile-key>/<file-system-id-key>/credentials
```

The credential file is mode `0600` where POSIX permissions are available. Parent directories are mode `0700`. Directory keys remain encoded and path-safe even though current tenant IDs are safe strings.

Example logical content:

```toml
file_system_id = "tnt_abc123"
region_code = "aws-us-east-1"
api_key = "drive9_..."
```

`region_code` is an immutable routing hint captured with the token, not inventory state. Remote list/get remains authoritative for status and existence. An explicit global `--region` that conflicts with a locally stored routing hint fails before invoking Drive9; ti must not send a token to a different regional endpoint.

The Drive9 companion may keep derived context and mount state under its isolated ti-owned home. That state is runtime material and must be reconstructible from file system ID, token, and region. It must not become a second resource inventory.

## Migration From The Name-keyed Registry

Existing installations store resources under:

```text
~/.ti/fs_resources/<profile-key>/<resource-name-key>/config
~/.ti/fs_resources/<profile-key>/<resource-name-key>/credentials
```

The migration is automatic, lazy, idempotent, rollback-safe, and never creates or deletes a remote resource. It runs before the first FS command that loads local credentials in a profile. A process-scoped lock prevents concurrent migration within one process, and atomic file creation prevents partial destination records across processes.

For every complete legacy resource:

1. Read the old config and credentials without modifying them.
2. Validate the legacy file system name, tenant ID, canonical region, and non-empty API key using the existing strict parsers.
3. Treat the legacy tenant ID as the new `file_system_id`; the old file system name is migration metadata only and is not a remote identity.
4. Load a profile-scoped, non-secret migration completion list from the new credential directory. An ID already recorded as migrated is never recreated from the rollback source after its new credential is explicitly removed.
5. Preflight every not-yet-migrated legacy entry and every existing ID-keyed destination before writing any new destination. Migration must not call Drive9 or depend on TiDB Cloud keys, network availability, backend authorization rollout, or current remote existence.
6. Write each new ID-keyed credential file atomically with mode `0600`, then read it back and compare ID, region, and token before considering that entry migrated.
7. After all pending entries are stored, atomically update the owner-only migration completion list. A crash before this update is safe because matching credential writes are idempotent.
8. Reconstruct the new ID-keyed Drive9 companion context on demand. Do not move or delete an old companion home while a mount may still reference it.
9. Leave the original `~/.ti/fs_resources` entry untouched. A previous ti release can therefore still use it if the user rolls back.

Migration establishes only a new local credential layout. It does not claim that a legacy resource still exists remotely. Subsequent remote list/get remains authoritative for existence and status, while a data-plane request remains authoritative for token validity. Keeping migration independent of remote inventory is required so an existing token remains usable while the admin inventory API is unavailable, denied, or temporarily failing.

Conflict handling is fail-closed:

- Existing destination with the same ID, region, and token is an idempotent success.
- Existing destination with a different token or region returns `fs.credential_migration_conflict`; do not choose one, overwrite either file, or call Drive9.
- Missing or malformed legacy config/credentials returns the existing incomplete-credential error and leaves all files unchanged.
- Two old names that map to the same tenant ID and same token collapse to one new credential entry and produce one non-fatal alias warning.
- Two old names that map to the same tenant ID with different tokens produce a conflict and retain every source file.

The old name is not retained as a selector after successful migration. Users discover the stable ID through `ti fs list-file-systems`. Migration diagnostics may show a safe mapping from the old local name to the new file system ID, but must never include the token.

This spec intentionally does not automatically delete legacy registry files. A later cleanup spec may remove them only after at least one release has used the new format, rollback support is no longer required, and active mount-state compatibility is proven.

The migration completion list contains only schema version and file system IDs, never names, regions, tokens, token fingerprints, or paths. It is internal migration state rather than resource inventory. Remote deletion removes the active ID-keyed credential but retains this completion state, preventing a preserved rollback source from silently restoring access on the next command.

## Resolution Rules After Migration

For data-plane commands, ti resolves inputs in this order:

1. Read an optional file system ID from `--file-system-id` or `TI_FS_FILE_SYSTEM_ID`.
2. Read an optional explicit token from `--fs-token` or `TI_FS_TOKEN`.
3. If no ID was supplied but an explicit token exists, decode its `tenant_id` claim as the candidate file system ID. If both exist, require them to match after remote token verification.
4. If an ID exists but no explicit token exists, load the matching token from the ID-keyed local credential file. Do not scan credential entries or select the only local entry implicitly.
5. Resolve the effective region from `--region`, `TI_REGION_CODE`, profile, or the matching local credential routing hint, while rejecting conflicts. Token-only use requires an explicit flag or environment region because there is no local routing hint.
6. Build or refresh the isolated Drive9 context from ID, token, and endpoint.
7. Invoke the bundled Drive9 public command. Drive9 remains responsible for cryptographically validating an explicitly supplied token before authorizing the requested operation.

Inputs may be mixed across sources. For example, an explicit ID can use a token from the environment and region from the profile. No source creates an `[env]` profile or writes environment credentials to disk.

## API And Companion Call Chain

Remote list:

1. Load profile TiDB Cloud public/private keys and effective region.
2. Resolve the hosted Drive9 endpoint from the bundled/hosted manifest.
3. Run `ti-drive9 admin tenant list --region-code <code> --json` with credentials passed through the companion environment or explicit internal arguments without logging values.
4. Follow every Drive9 page and map `tenant_id` to `file_system_id`.
5. Join only `has_local_token` from the local ID-keyed credential store.
6. Apply JMESPath and render JSON or text through the existing shared output path.

Remote describe and delete use `ti-drive9 admin tenant get/delete`. Create uses `ti-drive9 create --json` and the public `/v1/provision` endpoint. Data-plane commands continue to use the Drive9 owner or filesystem-scoped token interfaces.

Local token import resolves the regional endpoint, validates the Drive9 wrapper and JWT payload using structured parsing, and invokes the bundled companion's public `fs stat` command against the remote root. ti must not import Drive9 Go packages to decode the token or reimplement the companion's HTTP request. The implementation may use the Go standard library for Base64URL and JSON parsing, while the Drive9 server remains the authority that cryptographically validates the token.

Do not import Drive9 Go packages or code from `ref/`. ti integrates only through the bundled `ti-drive9` executable and its public command/output contract.

## Authentication And Errors

- List, describe, create, and delete require TiDB Cloud API keys and the existing ti FS control-plane permissions.
- Data-plane, mount, layer, Git, journal, and vault commands require an FS token but do not require TiDB Cloud API keys when ID and region are otherwise available.
- Drive9 `org:owner` and `project:owner` authorization is accepted without additional ti-side project filtering.
- A missing remote resource returns `fs.resource_not_found`, even if stale local credentials exist.
- A remotely visible resource without a local token is listable, describable, and deletable; data-plane use returns an actionable `auth.missing_fs_api_key` error and explains that token regeneration is not yet available.
- A user who possesses an existing token can run `ti fs import-file-system-token` to restore local data-plane access without recreating the file system or supplying TiDB Cloud API keys.
- A wrong-region ID returns the Drive9 not-found response mapped to `fs.resource_not_found`; ti must not search other regions implicitly.
- A token rejected during import returns an authentication error and leaves existing and destination credentials unchanged.
- API key values must never appear in logs, debug output, dry-run output, telemetry, or errors.

## Package Design

- `internal/fs`: remote control-plane orchestration and result mapping.
- `internal/fs/fscred`: new ID-keyed credential store, selector precedence, token import parsing and persistence, legacy migration, conflict handling, and `has_local_token` lookup.
- `internal/fswrap`: public Drive9 companion invocation for provisioning, admin tenant inventory, and data-plane commands.
- `internal/api/endpoints`: unchanged manifest-based region routing.
- `internal/cli`: flag migration from name to ID and shared output/dry-run wiring.
- `internal/config`: profile and TiDB Cloud credential loading only; do not put FS inventory into `~/.ti/config` or `~/.ti/credentials`.

Keep one package per directory. Do not add a second filesystem inventory cache package or direct Drive9 HTTP client while the companion exposes the required public commands.

## Dependencies And Platform

- Requires a bundled Drive9 version that exposes public `create --json` plus `admin tenant list/get/delete` with stable JSON output in every supported ti FS region.
- Adds no Go runtime dependency and no cgo requirement.
- Retains the existing FUSE/WebDAV platform behavior because only control-plane discovery and credential selection change.
- Requires no Drive9 backend schema change for this phase because tenant ID is accepted as the sole resource identifier.
- Future token lifecycle support will require a separate Drive9 API and follow-up ti spec.

The deployed Drive9 service is expected to enable `admin tenant list/get/delete` for ordinary TiDB Cloud organizations whose API keys have the accepted owner role, including organizations using free Starter capacity. The hosted region manifest determines which ti FS regions are currently available. Regional backend rollout is an external deployment concern and does not block completion of the ti client implementation.

The server reference in `ref/fs/` confirms that ti uses the intended contract: `GET /v1/admin/tenants` with `X-TiDBCloud-Public-Key` and `X-TiDBCloud-Private-Key` headers, followed by organization-scoped tenant lookup. That reference revision rejects Free organizations in `authorizeTiDBCloudAdminAccess`, but the deployed service is newer and must be verified independently in each region.

### Deployment Acceptance Status

Verified on 2026-08-11 with the `live-e2e` TiDB Cloud credentials:

| Region | Hosted manifest | Admin inventory | Lifecycle acceptance |
| --- | --- | --- | --- |
| `aws-us-east-1` | Published | List and describe pass | Create with `--wait`, list, describe, data-plane access, delete, and post-delete absence all pass for an isolated test resource. |
| `aws-ap-southeast-1` | Published | List and describe pass | Create with `--wait`, list, describe, data-plane access, delete, and post-delete absence all pass for an isolated test resource. |
| `aws-us-west-2` | Not published | A direct companion request to the known regional host returns `403 admin API is not available for free TiDB Cloud organizations`. | Not runnable until the deployment and manifest are updated. |
| `ali-ap-southeast-1` | Not published | No authoritative endpoint is available from the hosted manifest. | Not runnable until the deployment and manifest are updated. |

The ti client implementation and offline acceptance suite are complete. The two regions published by the hosted manifest pass the isolated lifecycle. `aws-us-west-2` and `ali-ap-southeast-1` remain external Drive9 deployment work and require regional acceptance after publication.

Five historical `aws-ap-southeast-1` tenants in the test organization remain visible through inventory but return a TiDB Cloud quota permission `403` when deleted with the current project-scoped API key. Newly created resources delete successfully through the same admin route. Cleaning those historical tenant bindings requires an API key with access to their underlying Starter resources or Drive9 backend intervention; it is not a ti client defect and does not reopen this spec.

## Tests

Unit tests must cover:

- remote list pagination, deterministic sorting, empty results, malformed JSON, repeated page detection, and region routing;
- list output for resources with and without local tokens;
- describe/delete without an FS token and rejection without TiDB Cloud credentials;
- delete removes only the matching new credential after remote acceptance and preserves it after any failure;
- create stores the one-time token atomically and never returns `exists` from local state;
- create persistence failure returns the accepted ID and token once with `credentials_stored: false`;
- import derives the file system ID from a valid wrapped token, verifies it remotely, and stores it without TiDB Cloud credentials;
- import rejects malformed wrappers/JWTs, missing tenant claims, expired/revoked/disabled tokens, wrong regions, and caller-asserted ID mismatches without writing;
- import idempotency, conflict handling, explicit replacement, mutually exclusive token inputs, secure token-file permissions, stdin input, and dry-run behavior;
- selector precedence for `--file-system-id`, `TI_FS_FILE_SYSTEM_ID`, verified token-derived IDs, token sources, and region sources;
- token-only sandbox resolution with only `TI_FS_TOKEN` and `TI_REGION_CODE`, including rejection when an optional asserted ID does not match the verified token;
- no `--file-system-name` or `TI_FS_FILE_SYSTEM_NAME` dependency remains after migration;
- migration of one and multiple legacy resources;
- migration without TiDB Cloud keys;
- idempotent migration and every same-ID token/region conflict case;
- migration performs no companion or remote API call and remains available during remote authorization, not-found, and transient failures;
- no secret appears in output, errors, operation logs, telemetry, or dry-run results;
- old registry and old companion homes remain untouched.

Black-box e2e must use a fake Drive9 companion to verify exact command arguments and mixed configuration sources. Live e2e must:

1. Create a uniquely identified remote file system and retain its returned ID/token.
2. List it through remote inventory using TiDB Cloud keys.
3. Describe it by ID.
4. Use it through at least one data-plane command with locally stored credentials.
5. Copy the credential setup into a clean temporary ti home and prove list/describe work without the old registry.
6. Prove a known token and region work without TiDB Cloud credentials, a profile, local state, or `TI_FS_FILE_SYSTEM_ID`; repeat with an optional matching ID assertion and reject a mismatched assertion.
7. Remove its local credential, import the known token into the clean home, and prove subsequent data-plane use no longer requires a token flag or environment variable.
8. Delete by ID using TiDB Cloud credentials after removing the new local token from the clean home.
9. Confirm the resource disappears from remote list without deleting any pre-existing resource.

Migration e2e fixtures must be created by the test itself under a temporary ti home. Tests must not depend on `ref/` fixtures or the developer's real `~/.ti` state.

## Documentation Updates During Implementation

Update README, AGENTS, completed FS specs with historical notes, installer next steps, and every English PingCAP ti FS command/reference/example page. Replace name-based examples and environment variables with file system IDs for locally stored credentials. Document that token-only sandboxes need only `TI_FS_TOKEN` and `TI_REGION_CODE`, while `TI_FS_FILE_SYSTEM_ID` is an optional consistency assertion. Explain that list is remote and region-scoped, while tokens remain local or explicitly injected.

## Acceptance Criteria

- A clean configured machine lists every authorized remote file system in its selected region without prior local FS registry state.
- A user can describe and delete a remote file system by ID after losing all local FS tokens.
- A user cannot read or mount a remote file system without a valid token.
- Existing name-keyed resources migrate to ID-keyed credentials without remote mutation, token loss, source deletion, or rollback breakage.
- A user can import a valid known token into an empty ti home and subsequently use that file system by ID without repeatedly supplying the token.
- Remote list results never include stale local-only resources.
- New creation does not accept or invent a file system name and returns the server-selected ID plus one-time token.
- Token-only sandboxes work with exactly token and region; a separately supplied ID is optional and must match the verified token.
- All regions currently published by the hosted manifest pass live create, list, describe, data-plane use, delete, and post-delete list verification.

Future Drive9 regional rollouts require their own deployment acceptance checks. They do not change the completed ti command, credential, migration, or companion integration contracts in this spec.

## Out Of Scope

- User-defined remote file system names, aliases, or rename operations.
- Returning existing owner token plaintext from list/get.
- Token generation, rotation, disable, enable, metadata listing, or revocation.
- Automatic cross-region inventory aggregation or implicit region searching.
- Automatic deletion of legacy registry files.
- Changes to Drive9 backend authorization scope for `org:owner` or `project:owner`.
