# Default Virtual Project Resolution

## Goal

Make TiDB Cloud project selection convenient and deterministic when local project information is available without requiring users to pass `--project-id` on every command. `tdc configure` discovers the authenticated account's `tidbx_virtual` project and stores its ID in the selected profile. `tdc db create-db-cluster` uses that stored ID when the user does not explicitly select another project.

The Starter API also accepts cluster creation without a project label and selects the account's default project. When neither an explicit nor configured project ID is available, tdc uses this server-side fallback instead of rejecting the command locally.

## User-facing Behavior

Configuration remains one command:

```bash
tdc configure
tdc configure --non-interactive \
  --region-code aws-us-east-1 \
  --tdc-public-key "$TDC_PUBLIC_KEY"
```

The private key may continue to come from `TDC_PRIVATE_KEY` for automation. After collecting the region and TiDB Cloud API key pair, configure calls the project-list API, finds the unique project whose `type` is `tidbx_virtual`, and stores its ID in the selected profile.

Cluster creation does not require `--project-id`. It uses the selected profile's discovered default when present and otherwise lets TiDB Cloud select the account's default project:

```bash
tdc db create-db-cluster \
  --db-cluster-name demo \
  --db-cluster-type starter
```

Users may still override the default explicitly:

```bash
tdc db create-db-cluster \
  --db-cluster-name demo \
  --db-cluster-type starter \
  --project-id 1372813089206541286
```

`--project-id` is an optional command flag and must appear with the optional flags in generated help. The explicit flag may select any project accessible to the API key; it is not restricted to `tidbx_virtual`.

## Configuration Model

Store the discovered project ID as a flat, non-sensitive profile value in `~/.tdc/config`:

```toml
[default]
region_code = "aws-us-east-1"
project_id = "1372813089454645969"
```

Do not store the project ID in `~/.tdc/credentials`, a nested project section, or a separate file. Do not store the project display name or copy the project response into local config. The ID is sufficient for deterministic cluster creation.

The value belongs to the selected local profile. Environment-sourced API keys do not change that namespace and must not create an `[env]` section. For example, `TDC_PUBLIC_KEY` and `TDC_PRIVATE_KEY` with no explicit profile update `[default]`; `--profile stage` updates `[stage]`.

Re-running `tdc configure` repeats project discovery and replaces that profile's previous `project_id`. This ensures changing API keys refreshes the project association instead of retaining an ID from the old account.

## Project Discovery Rules

Project discovery must be deterministic:

1. Build an authenticated IAM client from the region and API keys collected for the configure operation. Do not require those values to have already been persisted.
2. Call `GET /v1beta1/projects` and follow `nextPageToken` until all accessible projects have been read.
3. Select projects whose exact `type` is `tidbx_virtual`.
4. If exactly one project matches, store its `id` as `project_id`.
5. If no project matches, fail configure with an actionable error and do not write the newly collected profile values.
6. If multiple projects match, fail as ambiguous and report the matching IDs in structured error details. Do not choose the first API result.

Ignore regular projects such as `type = "tidbx"` during automatic selection. Unknown future project types must also be ignored rather than treated as virtual projects.

Project pagination must have the same defensive behavior as other paginated clients: reject a repeated non-empty page token to prevent an infinite loop, propagate authentication and authorization failures, and honor command context cancellation.

## Configure Transaction

Interactive and non-interactive configure use the same discovery path. The sequence is:

1. Resolve the selected local profile name.
2. Collect and validate `region_code`, public key, and private key using the existing precedence and prompt rules.
3. Discover the unique `tidbx_virtual` project using those in-memory credentials.
4. Prepare the updated config and credentials documents, including `project_id` in config.
5. Persist through the existing secure config-store path.
6. Render the configure result without exposing the private key.

An API, authentication, authorization, pagination, or project-cardinality error occurs before persistence. Existing profile files must remain unchanged when discovery fails. Ctrl+C must continue to return exit code 130, including while project discovery is in progress.

Configure success output adds the selected project information:

```json
{
  "profile": "default",
  "region_code": "aws-us-east-1",
  "project_id": "1372813089454645969",
  "project_type": "tidbx_virtual",
  "credentials_stored": true
}
```

The stored config does not need a separate `project_type` key because automatic discovery always selects `tidbx_virtual`.

## Cluster Creation Resolution

Resolve the project for `tdc db create-db-cluster` in this order:

1. Explicit non-empty `--project-id`.
2. `project_id` from the selected profile in `~/.tdc/config`.
3. Otherwise omit `labels["tidb.cloud/project"]` and let the Starter API select the account's default project.

An explicitly provided empty value, `--project-id ""`, is invalid and must not fall back to profile config. This matches the explicit-empty behavior of global profile and region flags.

Normal and dry-run creation must use the same resolver. When a project ID resolves, the final Starter request contains:

```json
{
  "displayName": "demo",
  "region": {
    "name": "us-east-1"
  },
  "labels": {
    "tidb.cloud/project": "1372813089454645969"
  }
}
```

When no project ID resolves, the request omits `labels`:

```json
{
  "displayName": "demo",
  "region": {
    "name": "us-east-1"
  }
}
```

Dry-run output must show the resolved project label when it came from the flag or profile, but it must omit the entire `labels` field when no project ID resolves. It must not claim the API request was sent.

## Command Scope

The stored default project is consumed only where a project decision is required:

- `tdc db create-db-cluster` uses it when `--project-id` is omitted.
- `tdc organization list-projects` remains an account-wide listing command and does not filter to the configured project.
- DB list, describe, update, delete, branch, SQL-user, connection-string, and SQL-execution commands continue to identify resources by globally unique cluster and branch IDs. They do not send `project_id`.
- `tdc db list-db-clusters` continues to return all clusters visible to the API key unless the user supplies an existing API-supported filter. It must not silently filter to the default project.
- `tdc fs`, `tdc fs-git`, `tdc fs-journal`, and `tdc fs-vault` do not consume `project_id`. The Drive9 companion provisioning interface does not expose project selection, so claiming that tdc can apply the DB default to fs would be incorrect.

## API Call Chain

Configure:

1. `internal/cli` resolves configure inputs without writing them.
2. `internal/config/configure` creates an in-memory authenticated profile or equivalent IAM client input.
3. `internal/api/iam.Client.ListProjects` sends Digest-authenticated `GET /v1beta1/projects?pageSize=<n>` requests to `https://iam.tidbapi.com` and follows `nextPageToken`.
4. The configure service selects the unique response object with `type = "tidbx_virtual"`.
5. `internal/config/store` writes `region_code` and `project_id` to the selected config profile and writes the API key pair to the corresponding credentials profile.

Default cluster creation:

1. `internal/cli` loads the selected profile and reads the optional `--project-id` flag state.
2. `internal/db` resolves the explicit or profile project ID, or leaves it absent for server-side default selection.
3. `internal/api/starter.Client.CreateCluster` sends Digest-authenticated `POST /v1beta1/clusters`. It includes `labels["tidb.cloud/project"]` only when an ID resolved and omits the label otherwise.
4. The existing Starter response and authorization handling remains unchanged.

The IAM project-list request does not take a project ID. No new service URL or user-provided endpoint is introduced.

## Package Design

- `internal/config` adds `ProjectID` to the non-sensitive profile model and loads the flat `project_id` key.
- `internal/config/configure` orchestrates project discovery before persistence and includes the selected project in its result.
- `internal/api/iam` remains the owner of project-list wire models and pagination requests. The existing `Project.Type` field is used for selection.
- A small configure-owned project selector validates zero, one, or multiple `tidbx_virtual` matches. Do not put product-selection policy into the generic IAM API client.
- `internal/db` owns project precedence for cluster creation so normal execution and dry-run share one resolver.
- `internal/cli` changes `--project-id` from required help metadata to optional while retaining explicit-empty detection.
- `internal/config/store` continues to own TOML preservation, permissions, and atomic file replacement.

Do not add a package solely to hold one project-selection function unless implementation complexity demonstrates a real need.

## Errors And Authorization

Required stable error cases:

- Invalid TiDB Cloud API keys: existing authentication error and exit code.
- API key lacks project-list permission: existing authorization error, naming the project-read operation.
- No `tidbx_virtual` project: a configuration error explaining that no default virtual project is available for the account.
- Multiple `tidbx_virtual` projects: an ambiguity error containing non-secret matching project IDs.
- Explicit empty `--project-id`: a usage error; do not use the profile fallback.
- Invalid or inaccessible explicit project ID: propagate the Starter API response through existing API error mapping.

Do not log API keys, project response bodies, or private configuration values. The local operation log may retain the selected profile, operation name, status, duration, and request ID under its existing policy; project IDs must not be added to command logs.

## Dependencies And Portability

- No new third-party Go dependency is required.
- Reuse the existing Digest-auth transport, IAM client, TOML store, typed errors, and Cobra command framework.
- No cgo dependency is introduced.
- Behavior remains portable across macOS, Linux, and Windows.
- Project discovery adds one or more IAM HTTPS requests to `tdc configure`; ordinary commands other than configure and cluster creation are unaffected.

## Tests

Unit tests must cover:

- Project selection with one regular and one `tidbx_virtual` project.
- Pagination where the virtual project is not on the first page.
- Zero virtual projects.
- Multiple virtual projects without first-result selection.
- Repeated page-token rejection.
- Authentication, authorization, network, cancellation, and malformed-response propagation.
- Interactive and non-interactive configure storing `project_id` in the selected profile.
- Environment credentials writing the selected profile rather than `[env]`.
- Discovery failure preserving existing config and credentials.
- Reconfigure replacing a stale `project_id`.
- Explicit `--project-id` overriding profile `project_id`.
- Omitted `--project-id` using profile `project_id`.
- Explicit empty `--project-id` failing without fallback.
- Missing flag and missing profile default sending an HTTP create request without a project label.
- Dry-run and normal creation using the same optional project-label behavior.
- Other DB commands continuing to send no project ID.

Black-box e2e tests must cover help showing `--project-id` as optional, configure output containing the selected project ID, cluster-creation dry-run using the stored default, and an actual create request omitting labels when no project is configured.

`make live-e2e` must verify the authenticated account exposes exactly one `tidbx_virtual` project, create its temporary `tdc-e2e-*` Starter cluster with environment credentials and a clean local home that has no configured project ID, assert the resulting cluster has a non-empty server-selected `tidb.cloud/project` label, and delete only that temporary cluster through the existing lifecycle cleanup. The server-selected account default is not required to equal the virtual project discovered by `tdc configure`.

## Documentation Updates During Implementation

When this spec is implemented:

- Update README configuration examples to include `project_id`.
- Update cluster creation examples so the common path omits `--project-id` and document the explicit override.
- Update AGENTS.md configuration rules, command examples, and project layout if packages move.
- Update `docs/present.md` and active e2e examples to remove unnecessary project-ID variables.
- Move this file to `docs/spec/done/` only after implementation and tests pass.

## Acceptance Criteria

- `tdc configure` discovers exactly one `tidbx_virtual` project through the real paginated IAM API before writing profile state.
- Successful configure stores its ID as flat `project_id` under the selected profile in `~/.tdc/config`.
- Configure fails without partial profile changes when discovery cannot produce exactly one virtual project.
- `tdc db create-db-cluster` treats `--project-id` as optional.
- Omitted `--project-id` resolves to the profile's `project_id` and sends it as `tidb.cloud/project` when configured.
- Missing flag and missing profile `project_id` omit the project label and let TiDB Cloud select the default project.
- Explicit `--project-id` overrides the profile default.
- tdc never sends `tidb.cloud/project` with an empty value.
- No unrelated DB or fs command begins sending or requiring Project ID.
- Unit, e2e, and live-e2e tests cover the resolution and real project placement.

## Out Of Scope

- Allowing users to choose the automatic configure default interactively from multiple projects.
- Automatically creating a missing `tidbx_virtual` project.
- Persisting an organization ID or project display name.
- Filtering all DB list operations to the configured project.
- Applying DB project selection to Drive9-backed tdc fs provisioning.
- Changing TiDB Cloud's server-side behavior when a raw Starter API caller omits the project label.
