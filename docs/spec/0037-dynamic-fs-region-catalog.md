# Dynamic Filesystem Region Catalog

## Verification Status

Implementation and local regression coverage are complete: `go test ./...` and
`make e2e` pass, including the fake Filesystem region catalog, the derived
address fallback, the credential backfill, and the `ti fs list-regions`
contract. Live verification is still pending: run `make live-e2e-fs` against the
real Filesystem API and record the published catalog, the profile region, and the
credential backfill result. Keep this spec outside `done/` until that evidence is
recorded.

## Goal

Stop compiling the TiDB Cloud Filesystem region list into `ti`. Discover the
available Filesystem regions, their cloud providers, and their regional
`serviceAddress` at runtime from the Filesystem open API instead:

```text
GET https://fs.tidbapi.com/v1beta2/regions
Authorization: HTTP Digest (TiDB Cloud public key : private key)
```

A region published by the backend must become usable without shipping a new `ti`
release. The TiDB Cloud Starter (DB) region list stays static: it has no
equivalent open API, and `ti db` placement validation must not start accepting
Filesystem regions.

A region's `serviceAddress` is a stable property of that region, not a value that
rotates. `ti` therefore records it once, when it learns about a File System, and
never needs to look the region up again for data-plane traffic.

## Motivation

Two compiled-in tables decide Filesystem placement today:

1. `internal/config/region.supported` plus `filesystemOnly` decide whether a
   canonical `region_code` is accepted, so `ti fs` can only reach whitelisted
   regions.
2. `internal/api/endpoints.builtInFSRegionManifest()` maps six `region_code`
   values to `https://<region>.drive9.ai` URLs.

The backend already publishes the authoritative list, including regions that
were historically missing from the deployed Drive9 rollout, and the observed
response is a direct superset of both tables:

```json
{"regions":[{"name":"regions/aws-ap-southeast-1","regionId":"aws-ap-southeast-1",
"cloudProvider":"aws","displayName":"Singapore (ap-southeast-1)",
"serviceAddress":"https://aws-ap-southeast-1.drive9.ai","state":"ACTIVE"}],
"totalSize":6,"nextPageToken":""}
```

Keeping the tables means every new region waits for a release, removed or
republished regions are resolved from stale data, and the installer scripts and
documentation repeat a list that drifts from the backend.

## Constraints

- The regions API requires TiDB Cloud API keys. Data-plane, mount, `fs-git`,
  `fs-journal`, owner `fs-vault`, and `ti exec` commands must keep working
  without TiDB Cloud keys, because their only credential is a File System token.
  They therefore never request the catalog: they use the address recorded with
  the token and otherwise derive the conventional regional address.
- A regional `serviceAddress` is required for every remote Filesystem operation:
  `ti` injects `DRIVE9_SERVER` into the companion environment. A File System
  token carries only `tenant_id`, never a region or an address.
- The only user-facing placement input stays one canonical `region_code`. No
  server URL flag and no separate provider field are added.
- `ti update` keeps ignoring `~/.ti/`.

## API Contract

| Item | Behavior |
| --- | --- |
| Method / path | `GET {base}/v1beta2/regions` |
| Default base | `https://fs.tidbapi.com` |
| Auth | HTTP Digest with the selected TiDB Cloud API keys, through the existing `internal/api/transport` digest transport |
| Pagination | Follow `nextPageToken` until it is empty; request pages with `pageSize` and `pageToken`; refuse a repeated token and cap the page count |
| Entry fields | `regionId` is the canonical `region_code`; `cloudProvider` is the API provider (`aws`, `alicloud`, `gcp`, `azure`); `serviceAddress` is the regional base URL; `displayName` and `state` are presentation and eligibility data |
| Eligibility | Only `state` equal to `ACTIVE` (case-insensitive) is routable; other states stay in the catalog for `ti fs list-regions` output |
| Address validation | `serviceAddress` must be an absolute URL with a host; `https` is required except for loopback hosts, which local fake servers use. A trailing `/` is trimmed; user info, query, and fragment are rejected |
| Unknown providers | A `cloudProvider` outside the internal mapping keeps an empty internal provider and is only listed; it never falls back to another provider |

A malformed page (missing or malformed region id, duplicate `regionId`,
provider mismatch, non-JSON body, or a non-2xx status) fails the fetch as a
whole. Partial pages are never used.

## Region Catalog Ownership

A new package `internal/fsregion` owns the catalog:

- `Client.Fetch(ctx)` performs the digest request, pagination, validation,
  de-duplication, and stable ordering by `region_code`. It owns no local state
  and does not import `internal/api/endpoints`.
- `Catalog` is an immutable validated snapshot with `Lookup(regionCode)` and
  `EligibleRegionCodes()`.
- Error constructors produce the actionable failures listed below.

There is **no** catalog cache. A stale region list is exactly what the compiled-in
tables did, and the catalog is only consulted by commands that already require
network access and TiDB Cloud credentials.

## Endpoint Resolution Split

Two distinct paths replace the single compiled-in mapping:

1. **Control-plane resolution** (`endpoints.Resolver.ResolveFSProfile` /
   `ResolveFSRegion`) fetches the published catalog and requires TiDB Cloud API
   keys. It serves `create-file-system`, `list-file-systems`,
   `describe-file-system`, `delete-file-system`, `import-file-system-token`, the
   token lifecycle commands, AI provider configuration, and `list-regions`.
   A canonical code the catalog does not publish or does not currently activate
   fails with `api.fs_endpoint_unsupported`, listing the eligible regions.
2. **Credential resolution** (`endpoints.Resolver.ResolveFSCredentialEndpoint`)
   uses the `service_address` recorded in the selected File System's local
   credentials. It serves data-plane, mount, layer, pack/unpack, Git, journal,
   vault, and Runtime execution commands, and makes no network request at all.
   When the credential records no address (it predates the catalog) or records an
   unusable one, the conventional address is derived from the canonical region
   code: `aws-us-east-1` becomes `https://aws-us-east-1.drive9.ai`. That
   derivation is a documented compatibility rule, not a second region table: it
   needs no publication data, keeps token-only use working on a machine without
   TiDB Cloud keys, and never overrides a recorded catalog address.

`--region` keeps overriding `TI_REGION_CODE` and profile `region_code`. An
explicit `--region` that conflicts with a locally stored File System region
still fails before any remote request.

## Credential Service Address

The ID-keyed credential file gains one optional field:

```toml
file_system_id = "<id>"
region_code = "aws-us-east-1"
service_address = "https://aws-us-east-1.drive9.ai"
api_key = "drive9_..."
```

- `ti fs create-file-system` writes the address resolved from the catalog.
- `ti fs import-file-system-token` resolves the catalog entry before validating
  the token remotely and writes the same address. This is the bootstrap for a
  machine that has only a File System token: import needs TiDB Cloud API
  credentials once, and every later data-plane command needs none.
- `ti fs list-regions` backfills the address on stored credentials that predate
  this change. It only rewrites credentials it can match to an eligible catalog
  entry; per-credential write failures never fail the command. Backfill is an
  accuracy improvement, not a prerequisite: data-plane routing would otherwise
  fall back to the derived conventional address.
- A missing `service_address` stays loadable, so existing credentials keep
  parsing and keep working through the derived address.
- A stored address must pass the same validation as a catalog address. An
  unparsable stored value is dropped instead of failing the credential load, so
  the derived address or a later `list-regions` backfill takes over. The token
  stays authoritative.

Background mounts record the resolved address in the mount locator so
`drain-file-system` and `unmount-file-system` can rebuild the companion
environment without the catalog. A locator written before this change has no
address; those commands then use the stored credential address and finally the
derived address.

## `ti fs list-regions`

A new read-only command lists the published Filesystem regions:

```text
ti fs list-regions [--output json|text] [--query J]
```

- Requires TiDB Cloud API keys; it never uses a File System token.
- Declares the new permission `authz.FSRegionRead` (`fs.region.read`) and enters
  `authz.commandPermissions`.
- Always requests the API; a fetch failure is a command failure, never a silent
  fallback.
- Refreshes stored credential addresses as described above.
- Structured result:

```json
{
  "region_count": 6,
  "regions": [
    {
      "region_code": "aws-ap-southeast-1",
      "cloud_provider": "aws",
      "display_name": "Singapore (ap-southeast-1)",
      "service_address": "https://aws-ap-southeast-1.drive9.ai",
      "state": "ACTIVE",
      "eligible": true
    }
  ]
}
```

- Sorted by `region_code`; every state is listed and `eligible` is true only for
  `ACTIVE` entries.
- Text output renders an aligned table with the columns `REGION`, `PROVIDER`,
  `DISPLAY NAME`, `STATE`, `SERVICE ADDRESS`.
- Read-only commands reject `--dry-run`.

## Placement Validation Split

`internal/config/region` separates parsing from Starter eligibility:

- `ParseCanonicalCode(code)` validates only the canonical shape: provider prefix
  from the known set, native part lowercase alphanumeric with `-` and `.`.
  Unknown prefixes are rejected; a backend reporting a new cloud provider needs
  a `ti` change to map it.
- `CanonicalCode(provider, nativeCode)` returns the canonical string without a
  Starter whitelist check.
- `Validate(provider, regionCode)` keeps the static TiDB Cloud Starter
  whitelist and stays the only authority for `ti db` placement. The
  Filesystem-only diagnostic is preserved from `filesystemOnly` purely as a
  better message; it no longer decides whether a Filesystem region exists.
- `ValidateFilesystem` is removed. Filesystem eligibility is decided by the
  catalog.
- `ti configure` validates the canonical shape only and writes the code without
  a network request. Command-time validation stays authoritative: Starter
  commands reject non-Starter placements locally, and Filesystem commands reject
  regions the catalog does not publish.

## Removed Behavior

- `endpoints.builtInFSRegionManifest()`, `FSRegionManifest`,
  `fetchFSManifest`, `selectFSManifestEntry`, and `FSManifestURL` are deleted.
  No Filesystem endpoint is derived from a compiled-in table.
- The hidden `TI_TEST_FS_MANIFEST_URL` control is replaced by
  `TI_TEST_FS_REGIONS_URL`, which points the catalog fetch at a local server.
  Like every other hidden control it applies only to a local build with
  `TI_ALLOW_TEST_ENDPOINTS=1`.
- Filesystem control-plane commands no longer accept a region because a
  compiled-in table names it; an unpublished region fails locally even when a
  conventional address could be derived, because the backend is authoritative
  for existence.
- `scripts/install.sh` and `scripts/install.ps1` stop printing region lists.
  The installer e2e assertion that every Filesystem region string appears in
  both scripts is inverted into an assertion that no region list is compiled in.

## Output, Privacy, And Failure Codes

- The catalog holds only public placement metadata. Keys travel through the
  redacting digest transport and never appear in output, logs, telemetry, or
  errors.
- Operation logs may record the service, HTTP method and status, the canonical
  region code, and the fetch outcome; never keys, request or response bodies, or
  local paths.
- Codes:
  - `fs.region_catalog_unavailable` (config, exit `2`) — the catalog request
    cannot be made because TiDB Cloud API credentials are missing.
  - `api.fs_endpoint_unsupported` (config, exit `2`) — the catalog does not
    publish the region, does not currently activate it, or publishes it for a
    cloud provider this `ti` release cannot map. An unmapped provider is listed
    by `ti fs list-regions` but never routed, and never borrows the provider
    mapping of the region-code prefix.
  - `api.fs_regions_request` / `api.fs_regions_unavailable` /
    `api.fs_regions_decode` / `api.fs_regions_invalid` (api, exit `1`) — request
    construction, transport, decode, and contract validation failures.
  - `config.invalid_region` (exit `2`) — malformed canonical code.

## Testing

Unit tests:

- `internal/fsregion`: multi-page fetch, terminal token, repeated token
  rejection, ACTIVE filtering, duplicate/malformed entries, provider mismatch,
  address validation (including loopback-only plain http), transport and HTTP
  failures, missing credentials, and the actionable error constructors.
- `internal/api/endpoints`: catalog-driven control-plane resolution, unpublished
  and inactive regions, missing credentials, credential-address resolution without
  network, invalid stored address, and the local-build test override.
- `internal/config/region`: structural acceptance and rejection, Starter
  whitelist unchanged, Filesystem-only diagnostic preserved.
- `internal/fs`, `internal/fs/fscred`: `list-regions` output and credential
  backfill, create/import writing the address, the derived-address fallback,
  mount locator address round-trip, and data-plane routing with no network.
- `internal/cli`: `ti fs list-regions` registration, flags, permission, and
  `--dry-run` rejection.

Black-box e2e:

- Every fake Filesystem deployment is served through `TI_TEST_FS_REGIONS_URL`
  with the new response shape.
- Control-plane commands resolve regions from the fake catalog, including a
  region the binary never knew about.
- An unpublished region and an active region whose cloud provider `ti` cannot
  map both fail with `api.fs_endpoint_unsupported`, and the unmapped one stays
  visible in `ti fs list-regions`.
- A token-only data-plane command without a recorded credential routes through
  the derived conventional address and never requests the catalog.
- A recorded credential routes the companion to its address instead of the
  derived one, ignores inherited `DRIVE9_*` values, and needs no catalog.
- `ti fs list-regions` renders JSON and text.
- Release builds ignore `TI_TEST_FS_REGIONS_URL`.
- Both installers contain no region list.

Live e2e (`make live-e2e-fs`):

- `ti fs list-regions` lists the profile's region with `ACTIVE` state and an
  `https` service address, and backfills stored credentials.
- Create/list/describe/delete and one data-plane command use the live catalog and
  the recorded address.
- A region the live API does not publish fails locally without a remote request.

## Documentation

Update in the same change:

- `AGENTS.md`: replace the built-in Filesystem endpoint mapping and the static
  Filesystem region table with the catalog, the credential `service_address`,
  `ti fs list-regions`, the import bootstrap, and the installer rule that no
  region list is printed.
- `docs/priciples.md`: replace the Filesystem region enumeration sentence and the
  configuration-free access claim.
- `README.md`: region discovery, `ti fs list-regions`, the import bootstrap, and
  the removal of the installer region lists.
- `docs/pingcap-docs/docs/`: the matching English `ti-` pages when the published
  behavior is described there.
- Move this spec to `docs/spec/done/` only after live verification.

## Non-Goals

- Dynamic TiDB Cloud Starter regions.
- Any user-facing server URL, provider flag, or region-list file override.
- A region-catalog cache, TTL, or refresh flag.
- Changing `ti fs create-file-system` region selection semantics.
