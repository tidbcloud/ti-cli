# Database Provider Architecture

`internal/db` is the product-neutral routing layer for `tdc db`. It owns cluster-type parsing, service-plan discovery, provider registration, capability dispatch, dynamic permission selection, and tdc-owned pagination cursors. It does not implement a TiDB Cloud database product API.

Concrete database products live under `internal/db/product`. The current implementation is `internal/db/product/starter`, which owns the TiDB Cloud Starter v1beta1 cluster, branch, SQL-user, connection-string, and SQL execution behavior. Reusable formatters, credential storage, SQL transports, and validation helpers remain in their focused packages directly under `internal/db`.

## Dispatch Rules

- Commands without a required cluster ID select a provider from the required `--db-cluster-type` value. The only accepted value today is `starter`.
- Commands with a required cluster ID do not accept a type flag. A registered resolver reads authoritative cluster metadata, maps `servicePlan` with the legacy `clusterPlan` fallback, and passes the original snapshot to the selected provider.
- The provider reuses that snapshot for the operation precondition. It must not repeat the discovery `GetCluster` request. Polling required after an accepted mutation is separate and remains allowed.
- Providers expose small capability interfaces. A product is not required to implement branches, SQL-user management, or any other capability that its API does not support.
- Each provider maps a DB operation to its permission. Static command-path permission lookup is intentionally not used by `tdc db`.

## Adding A Database Type

Do not add a new type by switching the Starter endpoint or reusing Starter request models. Essential, Premium, and Dedicated may use different domains, API versions, authentication, resources, and command contracts.

To add a product:

1. Add the internal `ClusterType` value and separately decide whether the CLI should accept it. Server discovery can recognize a type before users can select it for create or list.
2. Implement a product package under `internal/db/product/<type>` with its own API clients and typed product options.
3. Implement only the capability interfaces supported by that product.
4. Implement `Provider.ClusterType` and the complete operation-to-permission mapping for those capabilities.
5. Add or register a resolver for the product's authoritative cluster metadata. Return a typed snapshot that the provider can reuse.
6. Register the resolver and provider in the CLI composition root. The root `internal/db` package must not import a child product package.
7. Add unit tests for type resolution, capability absence, dynamic permissions, snapshot reuse, output stability, and pagination. Add black-box and live tests for every exposed command.
8. Update `README.md`, `AGENTS.md`, product principles, command help, and PingCAP documentation in the same change.

Unknown, missing, or conflicting plan metadata must fail closed with `db.cluster_type_unknown`. A recognized type with no registered provider must fail with `db.cluster_type_not_supported`. Never route either case to Starter by default.

## Starter Listing

The current TiDB Cloud list endpoint cannot filter by service plan. The Starter provider requests upstream pages of 100 resources and filters each page by authoritative plan and effective region. The dispatcher fills a user-facing page incrementally and stops as soon as it has enough results.

The returned page token is owned by tdc. It records the upstream page position, a matching-resource offset, and a page fingerprint, and binds the position to profile, cluster type, region, filter, and ordering. A changed replay page fails with `db.page_token_stale` instead of silently duplicating or skipping clusters. Do not expose or accept the upstream API token directly.
