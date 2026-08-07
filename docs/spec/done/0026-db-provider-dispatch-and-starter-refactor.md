# DB Provider Dispatch And Starter Refactor

## Goal

Refactor `tdc db` from a Starter-specific implementation into a product-aware command layer with runtime provider dispatch. Move the existing cluster, branch, SQL-user, connection, and SQL execution implementation under `internal/db/product/starter`, while keeping shared contracts, dispatch, cluster-type discovery, pagination, and reusable helpers directly under `internal/db`.

This spec implements only TiDB Cloud Starter. Essential, Premium, and Dedicated are separate products whose APIs, domains, authentication, flags, capabilities, and response models may differ substantially. The architecture must leave room for those products without guessing or implementing their APIs.

## User-facing Contract

Commands that do not require `--db-cluster-id` require an explicit `--db-cluster-type`:

```bash
tdc db create-db-cluster --db-cluster-type starter --db-cluster-name demo
tdc db list-db-clusters --db-cluster-type starter
```

There is no default cluster type. Omitting the flag is a breaking usage error:

```text
tdc [ERROR]: --db-cluster-type is required
```

The only accepted CLI value is the exact lowercase value `starter`. Trim surrounding whitespace for empty-value validation, but do not lowercase, alias, infer, or auto-correct input. Reject `Starter`, `STARTER`, `serverless`, `essential`, `premium`, `dedicated`, and every unknown value with:

```text
db.unsupported_cluster_type: --db-cluster-type must be one of: starter
```

Commands that require `--db-cluster-id` do not expose `--db-cluster-type`. They discover the authoritative service plan from the cluster ID and dispatch to the registered provider:

```bash
tdc db describe-db-cluster --db-cluster-id <cluster-id>
tdc db update-db-cluster --db-cluster-id <cluster-id> --db-cluster-name new-name
tdc db delete-db-cluster --db-cluster-id <cluster-id>
tdc db create-db-cluster-branch --db-cluster-id <cluster-id> --db-cluster-branch-name dev
tdc db create-db-sql-users --db-cluster-id <cluster-id>
tdc db format-db-connection-string --db-cluster-id <cluster-id>
tdc db execute-sql-statement --db-cluster-id <cluster-id> --sql "SELECT 1"
```

Do not accept both a user-declared type and a server-resolved type for an ID-based command. The cluster resource is the single source of truth.

Successful JSON and text output schemas remain the TiDB Cloud API-derived schemas already exposed by tdc. Do not add a synthetic `db_cluster_type` field to cluster output. tdc may map service plans to internal cluster types for routing, but it must not modify the formal API response with invented fields.

## Command Classification

Type-selected commands:

| Command | Selection source |
| --- | --- |
| `create-db-cluster` | required `--db-cluster-type` |
| `list-db-clusters` | required `--db-cluster-type` |

ID-discovered commands:

| Command | Required discovery view |
| --- | --- |
| `describe-db-cluster` | requested `--view`, or BASIC when omitted |
| `update-db-cluster` | BASIC |
| `delete-db-cluster` | BASIC |
| `create-db-cluster-branch` | BASIC |
| `list-db-cluster-branches` | BASIC |
| `describe-db-cluster-branch` | BASIC |
| `delete-db-cluster-branch` | BASIC |
| `create-db-sql-users` | FULL |
| `format-db-connection-string` | FULL |
| `execute-sql-statement` | FULL |

The resolved snapshot is passed into the selected provider and reused. A provider must not repeat the same preflight GetCluster request. Later polling requests required by `--wait` are not duplicate discovery and remain valid.

## Cluster Type Model

Define a stable internal `db.ClusterType` model. CLI parsing and server-plan parsing are separate operations:

- CLI parsing currently accepts only `starter`.
- Server-plan parsing recognizes `starter`, `essential`, `premium`, and `dedicated` case-insensitively after trimming whitespace.
- `servicePlan` is canonical.
- `clusterPlan` is a legacy fallback only when `servicePlan` is empty.
- If both fields are present and normalize to different types, fail closed.
- If both are absent or either contains an unknown value needed for resolution, fail closed.

Known but unregistered types return:

```text
db.cluster_type_not_supported: cluster "<id>" uses database cluster type "essential", which is not supported by this tdc version
```

Missing, conflicting, or unknown plan metadata returns:

```text
db.cluster_type_unknown: cannot determine the database cluster type for cluster "<id>" from servicePlan and clusterPlan
```

Do not silently route an unknown product to Starter. Do not infer a type from cluster name, region, project, endpoint, labels, spending limit, API domain, or command flags.

## Resolver Design

Define a resolver contract under `internal/db`:

```go
type ClusterResolver interface {
    ResolveCluster(context.Context, ResolveClusterRequest) (ResolvedCluster, error)
}
```

`ResolveClusterRequest` contains the profile, cluster ID, requested discovery view, debug settings, and discovery permission. `ResolvedCluster` contains the normalized cluster ID, internal cluster type, original service-plan values, and a typed provider snapshot contract that the selected provider can reuse.

The resolver registry is extensible, but this spec registers only the current v1beta1 resolver used for Starter discovery. It must not probe guessed Essential, Premium, or Dedicated URLs after a 404. Future products can add their own resolver implementations and endpoint selection in separate specs.

The current resolver can receive non-Starter records from the shared v1beta1 endpoint. It maps recognized plans so the dispatcher can return `db.cluster_type_not_supported` without invoking a Starter mutation.

## Provider And Capability Design

Do not define one giant provider interface that requires every database product to implement branches, SQL users, or Starter-specific settings. Define small capability interfaces for the current command operations, including:

```text
ClusterCreator
ClusterLister
ClusterDescriber
ClusterUpdater
ClusterDeleter
BranchCreator
BranchLister
BranchDescriber
BranchDeleter
SQLUserCreator
ConnectionStringFormatter
SQLExecutor
```

A registered provider declares its `ClusterType`, resolves permissions for supported `db.Operation` values, and exposes the capability interfaces it supports. Dispatch fails closed with `db.operation_not_supported` when a selected provider lacks the requested capability.

Use typed composition for provider-specific options. Keep shared request fields in shared structs and compose them with a typed product options interface or product-specific struct. Do not use a bare `any` options bag and do not place every future product flag into one growing common struct. The Starter implementation owns `StarterCreateOptions`, including `monthly_spending_limit_usd_cents`, and any other Starter-only request fields.

`internal/db` must not import packages under `internal/db/product`. CLI wiring is the composition root and registers the Starter resolver and provider with the dispatcher. `internal/db/product/starter` may import shared contracts from `internal/db`; this one-way dependency prevents an import cycle.

## Package Layout

Target layout:

```text
internal/db/
  README.md
  dispatcher.go
  operation.go
  provider.go
  resolver.go
  type.go
  pagination.go
  shared request and result contracts
  connectionstring/
  sqlcred/
  sqlresult/
  sqlsingle/
  product/
    starter/
      service.go
      cluster.go
      cluster_plan.go
      cluster_region.go
      branch.go
      sql.go
      product-specific validation and tests
```

Move the current DB product behavior and tests into `internal/db/product/starter`. Keep genuinely reusable helpers such as connection-string formatting, local SQL credential primitives, SQL result decoding, and single-statement validation directly under `internal/db`, outside the product hierarchy. `internal/api/starter` remains the transport and wire-model package for the current API.

Add `internal/db/README.md` explaining:

- the distinction between CLI cluster type, server service plan, resolver, provider, and capability;
- the current Starter-only provider registration;
- how dynamic permissions are resolved;
- how an ID-based command reuses a discovery snapshot;
- how list pagination and continuation tokens work;
- the exact steps for adding a new database product without importing it into the root `db` package;
- which tests and user documentation must be added for a new product.

## Dynamic Permission Model

DB commands no longer use the static command-path-to-Starter-permission mapping. Define product-neutral `db.Operation` values for every DB operation. Examples:

```text
cluster.create
cluster.list
cluster.discover
cluster.describe
cluster.update
cluster.delete
branch.create
branch.list
branch.describe
branch.delete
sql_user.create
connection_string.format
sql.execute
```

The DB dispatcher resolves permissions per API call:

- ID-based commands use `db.cluster.discover` for the discovery GET.
- After type resolution, the selected provider maps the requested operation to a concrete permission such as `starter.cluster.update` or `starter.sql.execute`.
- Type-selected create/list commands resolve the provider operation permission directly and do not use discovery permission.
- Missing operation or provider permission mappings fail with `authz.permission_mapping_missing` before the protected API call.

Only `tdc db` adopts dynamic permission dispatch in this spec. Organization and FS commands retain their static `authz.ForCommand` behavior. Update `controlPlaneCommandSpec` so a DB command can declare a dynamic DB operation instead of one static permission, while preserving the invariant that every control-plane command has exactly one declared authorization strategy.

The resolved permission must reach API error mapping, local operation logs, and dry-run checks. Do not use Starter permission names before the cluster type has been resolved.

ID-based mutating dry-runs are allowed to perform the read-only discovery GET. They must not call a mutation endpoint. Their checks include both permissions:

```json
{
  "checks": [
    {
      "name": "cluster_discovery_permission",
      "status": "passed",
      "message": "db.cluster.discover"
    },
    {
      "name": "operation_permission",
      "status": "passed",
      "message": "starter.cluster.update"
    }
  ]
}
```

Create dry-run and other type-selected dry-runs show only the selected provider's operation permission.

## Starter List Filtering

The TiDB Cloud v1beta1 list endpoint serves multiple products. Its documented and observed filter grammar does not accept `servicePlan`, `service_plan`, or `clusterPlan` clauses. Real API requests reject those expressions. Therefore tdc must filter cluster type client-side.

Continue applying the immutable effective-region filter at the server and defensively verifying the returned region. Pass supported user `--filter` and `--order-by` values upstream. Then resolve each returned cluster's type and include only verified Starter clusters in the final result.

Remove `--skip`. API skip occurs before client-side product filtering and cannot represent "skip N Starter clusters" correctly.

## Result Pagination

`--page-size` describes the number of verified Starter results returned by tdc, not the number requested from one API page:

- default final page size: `10`;
- maximum final page size: `1000`;
- fixed upstream API scan page size: `100`;
- continue reading API pages until the requested number of verified results has been collected or the upstream inventory ends.

Do not read all clusters eagerly. An organization can contain more than 100,000 clusters. Stop as soon as the final result page is full.

When the final result ends partway through a filtered API page, return a tdc-owned opaque `next_page_token`. On continuation, replay that API page, verify it, skip the already returned matching items, and continue scanning. For example, if API page 1 contains 3 Starter clusters and API page 2 contains 9, a requested result page of 10 contains all 3 from page 1 and the first 7 from page 2. The token resumes at matching offset 7 in API page 2.

The token is Base64URL-encoded, versioned structured data and contains at least:

```text
schema version
profile name
cluster type
effective canonical region code
user filter
order-by expression
fixed upstream page size
upstream page token used to fetch the partially consumed page
matching offset within that filtered page
SHA-256 fingerprint of the ordered verified matching cluster IDs in that page
```

The token is opaque routing state, not an authentication credential. Parse it with strict size and field limits. Do not log its raw value. API authentication remains responsible for resource access.

On continuation:

1. Require the selected profile, cluster type, effective region, filter, and order-by values to match the token.
2. Fetch the recorded upstream page with the fixed upstream page size.
3. Apply the same region and type verification.
4. Recompute the fingerprint before applying `matching_offset`.
5. Return `db.page_token_stale` if the matching ID sequence changed or the offset is no longer valid.
6. Resume after the consumed matching entries and scan subsequent API pages until the requested final page is full or inventory ends.

If a result page consumes an API page exactly, the next token points at the upstream response's next page token with offset zero. If inventory is exhausted, omit `next_page_token`.

Detect malformed tokens, unsupported token versions, mismatched request context, repeated upstream page tokens, and impossible offsets. Use stable errors:

```text
db.invalid_page_token
db.page_token_context_mismatch
db.page_token_stale
db.pagination_loop
```

Changing `--page-size`, `--output`, `--query`, or `--debug` between requests is allowed. Changing profile, type, effective region, filter, or order-by is rejected. A TiDB Cloud native API page token is not a valid tdc token after this spec.

Do not return the upstream unfiltered `totalSize`.

## API Call Chains

Type-selected create:

1. Load profile and require `--db-cluster-type`.
2. Parse the strict CLI type and select the registered provider.
3. Resolve the provider's create permission.
4. Build typed shared and Starter create options.
5. Call the Starter create capability.
6. Verify the returned service plan and retain the existing `--wait` behavior.

Type-selected list:

1. Load profile and require `--db-cluster-type`.
2. Select the registered provider and list permission.
3. Decode or initialize the tdc pagination cursor.
4. Read upstream pages with fixed size 100, preserving supported region/user filters and ordering.
5. Verify region and service plan, collect Starter results, and produce a continuation cursor when needed.

ID-based operation:

1. Load profile and validate `--db-cluster-id`.
2. Resolve discovery permission and perform exactly one initial GetCluster using the command's required view.
3. Resolve the server plan to an internal cluster type.
4. Select the registered provider and requested capability.
5. Resolve the provider operation permission.
6. Pass the discovered snapshot into the provider.
7. Execute the read, mutation, branch, SQL-user, connection-string, or SQL capability without repeating the discovery GET.

ID-based dry-run follows the same first five steps, constructs the provider-specific plan, and performs no mutation.

## Errors And Safety

- Missing `--db-cluster-type` fails before profile API calls for type-selected commands.
- Unsupported CLI type fails before any product API call.
- Unsupported discovered type fails before every cluster, branch, IAM SQL-user, local credential, or SQL mutation.
- Unknown or conflicting plan metadata fails closed.
- Missing capability or permission mapping fails closed.
- Discovery authentication and authorization errors identify `db.cluster.discover`; provider API errors identify the resolved product permission.
- A create accepted by the server but returned with an unexpected or unverifiable plan retains the cluster and reports its ID, as before.
- A failed wait never deletes or recreates a cluster or branch.
- Pagination cursor errors never silently restart from the first page.
- Structured errors and warnings must not corrupt JSON stdout.

## Dependencies And Platform

No new third-party dependency is required. Use the Go standard library for Base64URL, JSON, SHA-256 fingerprints, and strict cursor validation. The refactor introduces no cgo dependency and does not change macOS, Linux, or Windows support.

Do not import or depend on `ref/`.

## Tests

Unit tests must cover:

- required `--db-cluster-type` on create and list;
- strict acceptance of only lowercase `starter` CLI input;
- absence of `--db-cluster-type` on every ID-based command;
- server-plan normalization for Starter, Essential, Premium, and Dedicated;
- unknown, missing, and conflicting plan errors;
- recognized but unregistered type errors;
- capability and permission mapping failures;
- exactly one discovery GET for every ID-based command before provider execution;
- reuse of BASIC and FULL discovery snapshots as declared;
- zero mutation calls after unsupported, unknown, permission, or dry-run rejection;
- two-permission dry-run checks for ID-based mutations;
- static permission behavior remaining unchanged for FS and organization commands;
- output schema remaining unchanged with no synthetic `db_cluster_type` field;
- fixed upstream page size 100 and final default/max page-size validation;
- filling one final result page across multiple sparse API pages;
- stopping page scans as soon as enough matching results exist;
- replaying a partially consumed API page without duplicate or missing IDs;
- exact-page-boundary and end-of-inventory behavior;
- pages with zero Starter matches;
- malformed, oversized, wrong-version, context-mismatched, stale, impossible-offset, and looped tokens;
- changing final page size during continuation;
- a synthetic inventory exceeding 100,000 clusters that proves the implementation does not preload all pages.

Black-box e2e tests must cover the breaking CLI flags, dynamic routing, unsupported-plan safety, cursor continuation, output/query behavior, and stable error codes.

`make live-e2e-db` must use explicit `--db-cluster-type starter` for create and list. It must run the existing real create, wait, describe, update, branch, SQL-role, SQL execution, delete, and wait flow. It must not mutate a pre-existing non-Starter cluster.

Required verification:

```bash
make test
make e2e
make live-e2e-db
```

## Documentation Updates

Update:

- `README.md` command examples and full command list;
- `AGENTS.md` implementation status, package layout, command examples, dynamic DB permission rule, pagination contract, and live-e2e requirements;
- `docs/priciples.md` to remove the default Starter type and describe explicit type selection plus runtime dispatch;
- `docs/present.md` examples;
- completed DB specs with short historical notes pointing to this newer contract where necessary;
- every affected English PingCAP command page, Quick Start, guide, example, configuration page, and command index under `docs/pingcap-docs/docs`.

Documentation must state that tdc currently implements only Starter even though its DB dispatcher is product-aware. Create and list examples must always show `--db-cluster-type starter`. ID-based examples must not show that flag. Remove `--skip` from help and reference documentation.

## After This Spec

Users explicitly choose the product for commands that create or enumerate clusters, while commands targeting a globally identified cluster derive the product from server metadata. Starter behavior lives behind a typed provider and capability boundary instead of defining the whole `db` package. A future database product can add its own resolver, provider, permissions, typed options, capabilities, tests, and documentation without importing its implementation into the root `db` package or weakening Starter safety.

## Dependencies

- `docs/spec/done/0003-output-error-query-dry-run.md`
- `docs/spec/done/0004-api-client-auth-and-region-routing.md`
- `docs/spec/done/0006-starter-db-cluster-lifecycle.md`
- `docs/spec/done/0007-starter-db-branch-lifecycle.md`
- `docs/spec/done/0008-starter-db-sql-access-and-query.md`
- `docs/spec/done/0023-starter-only-db-resource-guardrails.md`
- `docs/spec/done/0024-region-scoped-db-cluster-listing.md`

## Acceptance Criteria

- Create and list require explicit `--db-cluster-type starter` with no default.
- ID-based commands do not expose a type flag and route from one authoritative discovery request.
- Current DB product implementation resides under `internal/db/product/starter`; reusable helpers remain directly under `internal/db`.
- Root DB package provides resolver, dispatcher, capability, operation, dynamic permission, and pagination contracts without importing Starter.
- Essential, Premium, Dedicated, missing, unknown, and conflicting plans cannot reach a Starter mutation.
- DB API calls carry dynamically resolved discovery or provider permissions; FS and organization static permission behavior is unchanged.
- Dry-run performs allowed discovery reads and no mutation.
- Starter listing returns full final pages through bounded incremental API scanning and resumable tdc cursors.
- Pagination does not preload a 100,000-cluster inventory and does not silently duplicate or omit items when a replayed page changes.
- Successful cluster JSON remains API-compatible and contains no invented `db_cluster_type` field.
- `internal/db/README.md` gives an actionable, accurate new-product integration guide.
- Unit, black-box, and live DB tests pass.
- Repository and published English documentation match the implemented CLI.

## Out Of Scope

- Implementing Essential, Premium, Dedicated, BYOC, or any other DB product.
- Guessing or probing future product API domains, versions, authentication, or capabilities.
- Adding aliases or defaults for `--db-cluster-type`.
- Adding `--db-cluster-type` to ID-based commands.
- Adding a synthetic cluster-type field to API-derived output.
- Server-side service-plan filtering, which the current list API does not support.
- Preserving `--skip` or accepting native TiDB Cloud page tokens after this pagination contract changes.
