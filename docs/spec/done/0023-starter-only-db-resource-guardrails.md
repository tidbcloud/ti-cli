# Starter-only DB Resource Guardrails

## Goal

Guarantee that every `tdc db` command only exposes or operates on TiDB Cloud Starter clusters. A cluster ID copied from another client, API response, script, or configuration must not allow `tdc` to read, update, delete, branch, or configure SQL access for an Essential, Premium, BYOC, or otherwise non-Starter cluster.

The TiDB Cloud Starter and Essential API uses the same cluster endpoints for multiple service plans. Command naming and creation validation alone are therefore not a sufficient product boundary. `tdc` must enforce the Starter boundary from resource metadata before returning a cluster or performing any cluster-scoped action.

## User-facing Behavior

After this spec:

- `tdc db list-db-clusters` returns Starter clusters only.
- Commands that accept `--db-cluster-id` verify that the cluster is Starter before continuing.
- Branch commands verify the parent cluster before listing, creating, describing, or deleting a branch.
- SQL user, connection string, and statement commands verify the cluster before reading or writing SQL credentials or contacting the SQL endpoint.
- A non-Starter or unverifiable cluster produces a stable actionable error and no subsequent mutation.
- Existing Starter command names, flags, successful output, and permission contracts remain unchanged.

Example rejection:

```text
tdc [ERROR]: cluster "cluster-id" uses service plan "Essential"; tdc db only manages Starter clusters
```

If the API response cannot establish the service plan:

```text
tdc [ERROR]: cannot verify cluster "cluster-id" is a Starter cluster because the API response omitted servicePlan; no operation was performed
```

## Canonical Plan Resolution

The API model currently exposes both `servicePlan` and `clusterPlan`. Live API responses identify the product tier through `servicePlan`, for example:

```json
{
  "clusterId": "cluster-id",
  "clusterPlan": null,
  "servicePlan": "Starter"
}
```

Plan resolution follows these rules:

1. Trim whitespace and compare plan values case-insensitively.
2. Use `servicePlan` as the canonical current field.
3. Use `clusterPlan` only as a compatibility fallback when `servicePlan` is absent.
4. Accept only the normalized value `starter`.
5. Reject Essential, Premium, BYOC, and every other non-Starter value with `db.not_starter_cluster`.
6. If both fields are present and disagree, fail closed with `db.cluster_plan_unknown`.
7. If neither field identifies a plan, fail closed with `db.cluster_plan_unknown`.

An empty `clusterPlan` must no longer imply Starter. The existing behavior that accepts an empty `clusterPlan` is unsafe because current API responses can put the actual plan only in `servicePlan`.

## Command Coverage

### Cluster List

`tdc db list-db-clusters` calls the shared Starter and Essential list endpoint, so it must filter each returned page locally:

- Include only clusters whose resolved plan is Starter.
- Omit non-Starter clusters.
- Omit clusters whose plan is missing or ambiguous; they are not proven Starter resources.
- Preserve the server-provided `next_page_token` so callers can continue pagination.
- Omit `total_size` after local filtering because the server value counts resources before the Starter-only filter and would be misleading.
- Preserve user-supplied `--page-size`, `--page-token`, `--filter`, `--order-by`, and `--skip` behavior.

A filtered page can contain no clusters and still contain a `next_page_token`. Scripts must continue while a next token is present. This is preferable to inventing client-side pagination tokens or walking all remote pages in one command.

### Cluster Create

- Continue accepting only `--db-cluster-type starter`.
- Validate the create response with the same canonical plan resolver.
- If the accepted create response cannot be verified as Starter, return an error containing the created cluster ID and state that the resource was retained for inspection. Never delete it automatically.
- `--wait` validates every cluster returned by polling before reporting success.

### Cluster Describe, Update, And Delete

- `describe-db-cluster` performs `GET cluster`, validates the plan, and only then renders the cluster.
- `update-db-cluster` performs `GET cluster`, validates the plan, and only then sends `PATCH cluster`. It validates the update response again.
- `delete-db-cluster` performs `GET cluster`, validates the plan, and only then sends `DELETE cluster`.
- `delete-db-cluster --wait` validates any cluster representation returned while polling. If deletion was already accepted and a later response becomes unverifiable, report that deletion might still be in progress and do not send another mutation.

### Branch Lifecycle

Every branch command performs a parent-cluster preflight before calling a branch endpoint:

1. `GET /v1beta1/clusters/{clusterId}`.
2. Resolve and validate the parent cluster plan.
3. Only for Starter, call the requested branch list, create, describe, or delete endpoint.

This guard applies to:

- `create-db-cluster-branch`
- `list-db-cluster-branches`
- `describe-db-cluster-branch`
- `delete-db-cluster-branch`

For mutating branch commands, rejection must occur before `POST` or `DELETE`.

### SQL Access And Query

The same parent-cluster guard applies to:

- `create-db-sql-users`
- `format-db-connection-string`
- `execute-sql-statement`

`create-db-sql-users` must validate the cluster before any IAM SQL-user request or local SQL credential write. Connection string formatting and SQL execution must validate the cluster through their shared connection-input path before loading cluster-scoped SQL credentials or contacting an HTTPS/MySQL SQL endpoint.

For a rejected cluster, `tdc` must not create, rotate, overwrite, or delete files under `~/.tdc/db_users/`.

## Dry-run Behavior

Dry-run remains non-mutating and keeps the existing control-plane contract:

- Create dry-run verifies that the requested type is Starter.
- Update, delete, branch, and SQL-user dry-runs describe the planned Starter precondition but do not claim that a remote cluster has already been verified.
- Normal execution always performs the remote plan check before mutation.
- Do not add a remote read to dry-run solely for this guard unless the global dry-run contract is changed in a separate spec.

## API Call Chains

Cluster list:

1. `GET /v1beta1/clusters` with the existing pagination and filter parameters.
2. Resolve each cluster plan from `servicePlan`, falling back to `clusterPlan` only when needed.
3. Return only verified Starter clusters, preserve `next_page_token`, and omit the unfiltered `total_size`.

Single-cluster read:

1. `GET /v1beta1/clusters/{clusterId}`.
2. Validate Starter.
3. Return the resource or continue to the requested read path.

Single-cluster mutation:

1. `GET /v1beta1/clusters/{clusterId}`.
2. Validate Starter.
3. Only then call `PATCH` or `DELETE /v1beta1/clusters/{clusterId}`.

Branch operation:

1. `GET /v1beta1/clusters/{clusterId}`.
2. Validate Starter.
3. Only then call the relevant `/v1beta1/clusters/{clusterId}/branches` endpoint.

SQL user preparation:

1. `GET /v1beta1/clusters/{clusterId}` with the view required for SQL endpoints.
2. Validate Starter.
3. Only then call SQL-user IAM endpoints and update local SQL credentials.

SQL connection or execution:

1. `GET /v1beta1/clusters/{clusterId}` with endpoint metadata.
2. Validate Starter.
3. Load the selected local SQL role credentials.
4. Format a connection string or issue one HTTPS/MySQL SQL request.

## Implementation Design

- Keep API response fields in `internal/api/starter`; do not infer plans from cluster names, regions, project IDs, labels, endpoints, or command context.
- Implement one plan resolver and one Starter assertion in `internal/db` and reuse them from cluster, branch, and SQL services.
- Keep the check below `internal/cli`, so all command aliases, output modes, and callers receive the same protection.
- Return typed `apperr` errors with stable codes `db.not_starter_cluster` and `db.cluster_plan_unknown`.
- Do not add a new package unless the shared logic cannot remain cohesive inside `internal/db`.
- Do not depend on `ref/` code or fixtures.

No new third-party package is required. This change uses standard Go string normalization and existing API/error models. It adds no cgo dependency and does not change cross-platform support.

## Tests

Unit tests must cover:

- `servicePlan=Starter` with empty `clusterPlan` is accepted.
- Starter values are normalized for case and surrounding whitespace.
- Essential, Premium, BYOC, and unknown service plans are rejected.
- A missing `servicePlan` can use an explicit legacy Starter `clusterPlan` fallback.
- Both plan fields missing are rejected.
- Conflicting `servicePlan` and `clusterPlan` values are rejected.
- A mixed list page returns only Starter clusters, preserves `next_page_token`, and omits `total_size`.
- A page containing only non-Starter clusters can return an empty cluster list with a next token.
- Describe rejects a non-Starter cluster.
- Request recorder tests prove update and delete never send `PATCH` or `DELETE` after a failed guard.
- Request recorder tests prove every branch command performs the cluster preflight and sends no branch request after rejection.
- SQL-user preparation sends no IAM mutation and writes no local credential file after rejection.
- Connection formatting and SQL execution send no SQL request after rejection.
- Create and wait errors retain the accepted Starter cluster ID when later plan verification fails.

Black-box e2e tests must cover mixed-plan mocked responses and stable structured error codes. Live e2e continues to exercise the complete Starter happy path. It must never update or delete a pre-existing non-Starter cluster. If a dedicated opt-in live fixture provides a non-Starter ID, tests may verify read-only rejection only.

## Dependencies

- `docs/spec/done/0004-api-client-auth-and-region-routing.md`
- `docs/spec/done/0006-starter-db-cluster-lifecycle.md`
- `docs/spec/done/0007-starter-db-branch-lifecycle.md`
- `docs/spec/done/0008-starter-db-sql-access-and-query.md`

## Acceptance Criteria

- No `tdc db` command returns a non-Starter cluster as a supported resource.
- Every command that accepts a cluster ID validates the canonical service plan before continuing.
- Non-Starter and unverifiable clusters fail with stable typed errors.
- Rejection occurs before every cluster, branch, IAM SQL-user, credential-store, or SQL mutation.
- List pagination remains deterministic and does not report the server's unfiltered total as a Starter total.
- Starter create, lifecycle, branch, SQL-role, connection string, and SQL execution behavior remains functional.
- Unit and black-box tests prove both allowed and rejected call chains.
- README.md, AGENTS.md, and published English command documentation are updated when this spec is implemented.

## Out Of Scope

- Creating or managing Essential, Premium, BYOC, or Dedicated clusters.
- Adding a Dedicated API endpoint or client.
- Inferring a service plan when the API omits or contradicts plan metadata.
- Deleting a resource automatically after a create or wait validation error.
- Changing TiDB Cloud organization/project RBAC.
