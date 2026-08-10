# tdc fs Control Plane

> **Latest identity update:** `0027-remote-fs-resource-inventory.md` supersedes the name-keyed local inventory. Current commands use server-assigned file system IDs and Drive9's region-scoped remote inventory.

> **Current status:** The original 1:1 profile model, flat `fs_*` storage, and native control-plane integration in this document are historical. `0015-drive9-companion-wrapper-for-tdc-fs.md` makes `tdc-drive9` the unconditional Filesystem implementation; `0016-profile-fs-resource-registry.md` provides profile-scoped 1:N resource storage; `0018-fs-token-auth-and-config-free-access.md` adds token-only use of existing resources; and `0020-explicit-file-system-selection.md` removes persistent default selection. The command intent and dry-run requirements below remain useful context.

## Goal

Provide lifecycle and health operations for tdc fs resources before exposing
file data-plane commands.

## User-facing Commands

Initial command set:

- `tdc fs create-file-system`
- `tdc fs delete-file-system`
- `tdc fs check-file-system`

## Behavior

- Use `tdc fs` as the product naming for the filesystem domain.
- Do not expose reference implementation names in user-facing command output.
- Mutating commands support `--dry-run`.
- Commands must not prompt.
- `check` validates local config, credentials, provider/region routing, endpoint
  resolution, authorization, and remote service reachability.
- Create provisions the remote fs resource according to the available fs API.
- Create stores the returned `tdc fs` resource API key in
  `~/.tdc/credentials`.
- Delete removes the selected fs resource using a non-interactive safety
  mechanism.
- No command accepts a user-provided server URL or filesystem metadata database
  URL.

## Inputs And Config

- Requires resolved credentials.
- Uses canonical `region_code`.
- Resource identifiers must use explicit long flags.
- MVP supports one default tdc fs resource per profile. `--file-system-name`
  creates or validates the profile's `fs_resource_name`.
- Resource metadata is stored as flat `fs_*` keys under `[profile]` in
  `~/.tdc/config`: `fs_resource_name`, `fs_tenant_id`, `fs_cloud_provider`, and
  `fs_region_code`.
- Resource secrets are stored as flat `fs_*` keys under `[profile]` in
  `~/.tdc/credentials`: `fs_api_key`.

## Output And Errors

- JSON is the default output.
- `check` returns structured status for each checked dependency.
- Create/delete return resource or operation status.
- Create output must not print the raw `api_key` by default; it should report
  that credentials were stored.
- Errors must distinguish local configuration failures from remote fs service
  failures.
- Permission errors must name the required permission, such as
  `fs.volume.create`.

## After This Spec

Users can provision and validate `tdc fs` resources in the active profile's
canonical region:

```bash
tdc fs check-file-system
tdc fs create-file-system --file-system-name workspace --dry-run
tdc fs create-file-system --file-system-name workspace
tdc fs delete-file-system --file-system-name workspace
```

The user chooses one canonical region code during `tdc configure`; the CLI
resolves all fs endpoints internally.

## Implementation Design

- `internal/cli/fs` registers `tdc fs` commands and keeps all filesystem actions
  at the second command level.
- `internal/fs/control` owns create/delete/check use cases and authorization
  requirements.
- `internal/api/fs` contains fs control-plane HTTP methods and response models.
- `internal/fs/fscred` stores and loads the profile-level `fs_api_key` in
  `~/.tdc/credentials`.
- `internal/api/endpoints` provides fs endpoint resolution from
  canonical `region_code` by fetching the hosted tdc fs region manifest and
  selecting `tidb_cloud_native` entries.
- `internal/fs/status` defines the structured check response with local config,
  credential, permission, endpoint, and service health entries.
- Deletion requires an explicit `--file-system-name <name>` and does not add a
  console-style name confirmation flag.

## API Call Chain

Confirmed from the reference filesystem protocol and region discovery flow:

- `GET https://drive9.ai/manifest/regions/drive9-regions.json` currently
  returns the hosted tdc fs compatible region manifest.
- `GET /v1/status` checks service reachability and reports tenant/server
  capabilities.
- `POST /v1/provision` provisions or initializes a tenant/resource.
- `DELETE /v1/tenant` deletes the tenant/resource.
- Successful provision responses are expected to include `tenant_id`,
  `api_key`, and `status`.

Endpoint selection:

1. Validate the profile canonical `region_code` against tdc's supported TiDB
   Cloud Starter placements.
2. Fetch the region manifest.
3. Select the single entry where `mode == "tidb_cloud_native"`,
   `cloud_provider` matches the TiDB Cloud API provider name, and `tidb_region`
   matches the profile region.
4. Use that entry's `server_url` for tdc fs control-plane calls.
5. If no entry exists, return an unsupported tdc fs endpoint error. Do not ask
   the user for a raw server URL.

Command mapping:

- `tdc fs check-file-system`
  1. Validate local profile, credentials, provider, and region.
  2. Resolve the tdc fs base URL through the region manifest.
  3. If `fs_api_key` is configured, call `GET /v1/status` with
     `Authorization: Bearer <api-key>`.
  4. Report local config, endpoint resolution, auth, and remote status.
  5. If `fs_api_key` is missing, report remote status as a warning and do not
     make an unauthenticated status request.
- `tdc fs create-file-system`
  1. Validate `--file-system-name` and dry-run behavior.
  2. Resolve the tdc fs base URL through the region manifest.
  3. Call `POST /v1/provision` without Bearer auth. The native JSON request body
     is:

     ```json
     {
       "public_key": "<tdc_public_key>",
       "private_key": "<tdc_private_key>"
     }
     ```

  4. Store `fs_resource_name`, `fs_tenant_id`, `fs_cloud_provider`, and
     canonical `fs_region_code` in `[profile]` of `~/.tdc/config`.
  5. Store `fs_api_key` in `[profile]` of `~/.tdc/credentials`.
- `tdc fs delete-file-system`
  1. Validate `--file-system-name`.
  2. Resolve the tdc fs base URL through the region manifest.
  3. Load the stored resource API key and call `DELETE /v1/tenant` with
     `Authorization: Bearer <api-key>`.
  4. Send the native deletion body:

     ```json
     {
       "public_key": "<tdc_public_key>",
       "private_key": "<tdc_private_key>"
     }
     ```

  5. Remove local `fs_*` metadata and secret only after remote deletion
     succeeds, unless an explicit local-forget command is added later.

Current endpoint boundary:

- Endpoint discovery depends on the hosted manifest. At the time this spec was
  implemented, the manifest exposed TiDBCloud native endpoints for AWS
  `ap-southeast-1` and AWS `us-east-1`. Other configured Starter placements may
  be valid for TiDB Cloud but unsupported for tdc fs until the manifest adds
  entries.
- The provision response's `api_key` is sensitive and must never be logged,
  emitted to telemetry, or shown in default command output.

## Dependencies And Platform

- No new third-party dependency beyond specs `0001` through `0004`.
- Uses the shared authenticated HTTP client.
- No cgo is required.
- Platform-neutral.

## Dependencies

- `0004-api-client-auth-and-region-routing.md`.
- `0003-output-error-query-dry-run.md`.

## Acceptance Criteria

- Mock API tests cover create/delete/check.
- Tests cover endpoint resolution from static overrides and the region
  manifest, including unsupported provider/region handling.
- Tests reject user-supplied server URL flags or config keys.
- Tests cover dry-run for create and delete.
- Tests cover storing fs `api_key` in credentials and redacting it from output,
  logs, and telemetry.
- Tests verify user-facing output says `tdc fs`, not the reference name.

## Out Of Scope

- File upload, download, listing, and mutation.
- Local mount lifecycle.
