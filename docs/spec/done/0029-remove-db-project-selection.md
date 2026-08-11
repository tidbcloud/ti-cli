# Remove TiDB Cloud Project Selection And Inventory

## Goal

Remove client-side TiDB Cloud project selection and project inventory from TiDB Cloud CLI. TiDB Cloud Starter cluster creation must always omit project selection and let the TiDB Cloud service choose its server-side default project.

Project is fading out as a user-facing TiDB Cloud concept. `ti` must not discover a default project, persist a project ID, accept a project selector, infer a project from local state, or expose a standalone project-listing command.

This removal applies only to ti-owned command inputs, configuration, discovery, and inventory. It must not alter the shape or values of TiDB Cloud API responses. If a cluster response contains project-related fields or `labels["tidb.cloud/project"]`, ti returns them unchanged as opaque service-owned resource metadata.

This is an intentional breaking change that supersedes the active behavior originally introduced by `docs/spec/done/0017-default-virtual-project-resolution.md`. The completed spec remains unchanged as a historical record.

## Product Decisions

- Remove `--project-id` from `ti db create-db-cluster` without a compatibility alias or deprecation period.
- Do not add `TI_PROJECT_ID`, another environment variable, or another local project selector.
- `ti configure` does not discover `tidbx_virtual`, does not select a default project, and does not write `project_id`.
- `ti configure` becomes a local AWS CLI-style configuration operation. It validates local input and writes the selected profile without making a TiDB Cloud API request.
- Invalid or unauthorized API keys are reported by the first remote command that uses the permission required by that command, not by `ti configure`.
- A Starter create request omits project placement entirely. It must not send `project_id: ""`, `labels: {}`, or `labels: {"tidb.cloud/project": ""}`.
- TiDB Cloud remains free to return project-related fields and `labels["tidb.cloud/project"]` on cluster resources. ti preserves the API response and must not hide, rewrite, rename, filter, or interpret those values as local configuration.
- Remove `ti organization list-projects` and the now-empty `ti organization` top-level command without a compatibility alias or placeholder.
- Remove the project-list API client surface and `organization.project.read` permission from ti. Do not remove IAM SQL-user APIs or the IAM endpoint resolver because DB SQL-user workflows still depend on them.
- Existing cluster, branch, IAM SQL-user, and SQL operations continue to identify resources through cluster and branch IDs. They must not acquire a project parameter.
- Future DB product providers must not reintroduce a generic `--project-id` on `ti db` without a new approved product design.

## User-facing Behavior

Configure a profile:

```bash
ti configure
```

Non-interactive configuration remains:

```bash
TI_REGION_CODE=aws-us-east-1 \
TIDB_CLOUD_PUBLIC_KEY='<public-key>' \
TIDB_CLOUD_PRIVATE_KEY='<private-key>' \
ti configure --non-interactive
```

The successful JSON result becomes:

```json
{
  "profile": "default",
  "region_code": "aws-us-east-1",
  "credentials_stored": true
}
```

The text result contains the profile, canonical default region code, and credential persistence status. It contains no project ID or project type.

Create a Starter cluster:

```bash
ti db create-db-cluster \
  --db-cluster-type starter \
  --db-cluster-name application-db \
  --wait
```

The following old invocation is invalid after this spec:

```bash
ti db create-db-cluster \
  --db-cluster-type starter \
  --db-cluster-name application-db \
  --project-id project-123
```

It fails as a normal unknown flag usage error with exit code `2`. ti must not silently ignore the supplied project ID because doing so would create the cluster in a different placement than the caller requested.

The following commands are also removed:

```bash
ti organization
ti organization list-projects
```

They fail as unknown commands. Project inventory is no longer part of the TiDB Cloud CLI command surface.

## Configure Contract

`ti configure` collects only:

- profile namespace from `--profile`, `TI_PROFILE`, or `default`;
- canonical default region code;
- TiDB Cloud public API key;
- TiDB Cloud private API key.

Configuration call chain:

1. Resolve and validate the profile name.
2. Read the region and credentials from flags, canonical environment variables, or the interactive prompt according to the existing precedence rules.
3. Validate the canonical region syntax and required local values.
4. Atomically update the selected profile's non-secret config and secret credentials.
5. Remove a legacy `project_id` key from the selected profile if it exists.
6. Return the local configuration result.

Configure must not:

- resolve the IAM endpoint;
- call `GET /v1beta1/projects`;
- paginate projects;
- search for `type = "tidbx_virtual"`;
- require `organization.project.read`;
- call the Starter cluster API merely to probe credentials;
- fail because the machine is offline or a TiDB Cloud endpoint is temporarily unavailable.

This deliberately changes the meaning of configure from "authenticate and discover a virtual project" to "persist local command inputs". A successful configure result means the local profile was written; it does not claim that the API keys were remotely authenticated.

## Starter Create Request

The public Starter API request remains:

```text
POST /v1beta1/clusters
```

An illustrative request body is:

```json
{
  "displayName": "application-db",
  "region": {
    "name": "regions/aws-us-east-1"
  }
}
```

If a monthly spending limit is supplied, `spendingLimit` is added as before. The body must not contain project placement or an empty labels object.

The service selects its default project. A successful response can include:

```json
{
  "clusterId": "cluster-123",
  "displayName": "application-db",
  "servicePlan": "Starter",
  "labels": {
    "tidb.cloud/project": "server-selected-project-id"
  }
}
```

ti returns those labels as received. The returned project label is resource metadata only; it is not copied into `~/.ti/config` and does not become a default for a later create request.

## Other DB Operations

No request other than Starter create currently sends a project selection. Preserve that boundary explicitly:

- `list-db-clusters` calls the organization-level cluster list API and filters by effective region and verified product type.
- `describe-db-cluster`, `update-db-cluster`, and `delete-db-cluster` use the cluster ID.
- Branch commands use cluster ID and branch ID.
- SQL-user preparation and repair use cluster ID.
- Connection-string formatting and SQL execution use cluster ID plus locally managed SQL credentials.
- Product dispatch discovers the cluster service plan through cluster metadata and does not use a project ID.

Do not add a project filter to list pagination, dispatch discovery, Starter guardrails, SQL credential paths, operation logs, or telemetry. Do not remove or rewrite project-related fields received in cluster responses; response preservation is independent of project selection.

## Dry-run Behavior

`ti db create-db-cluster --dry-run` renders the same projectless request that normal execution sends:

```json
{
  "request": {
    "method": "POST",
    "path": "/v1beta1/clusters",
    "body": {
      "displayName": "application-db",
      "region": {
        "name": "regions/aws-us-east-1"
      }
    }
  }
}
```

The dry-run output must not contain `project_id`, `project-id`, `tidb.cloud/project`, or `labels`. A dry run may explain in a non-request description that TiDB Cloud selects the server-side default project, but that explanation must not be represented as a request field.

## Local Config And Migration

New profiles contain no project key:

```toml
[default]
region_code = "aws-us-east-1"
```

Existing profiles can contain the legacy key:

```toml
[default]
region_code = "aws-us-east-1"
project_id = "legacy-project-id"
```

Migration rules:

- Every DB command ignores the legacy value immediately after upgrade.
- Loading a profile must not copy the legacy value into the runtime `config.Profile` used by DB services.
- Ordinary DB, FS, update, and help commands do not rewrite the config merely to remove the value.
- The next successful `ti configure` for that profile removes its `project_id` while updating region and credentials.
- Reconfiguring one profile must not remove or change values in another profile.
- A legacy `project_id` with malformed or unexpected content must remain inert and must not block profile loading or a projectless DB request.
- No migration marker is required because the old field is non-secret, ignored, and safely removable on reconfigure.

The config store may retain a private decode-only compatibility field for one release if needed to remove the old TOML key deterministically. It must not expose that field through the runtime profile or use it in any request.

## Implementation Design

### CLI

In `internal/cli`:

- remove the `project-id` flag registration from `create-db-cluster`;
- stop reading `project-id` in `createClusterOptions`;
- update help, usage, and command tests;
- keep `db-cluster-type`, `db-cluster-name`, spending limit, wait, and dry-run behavior unchanged.
- remove registration of the `organization` parent and `list-projects` child commands;
- remove organization-only service construction, help, aliases, permissions, and command-path mappings;
- ensure `ti organization` and `ti organization list-projects` both fail through the ordinary unknown-command path.

### Configure

In `internal/config/configure`:

- remove project-listing client construction, pagination, virtual-project selection, and related constants;
- remove `ProjectID` and `ProjectType` from the configure result;
- remove IAM resolver and transport requirements that exist only for project discovery;
- retain interrupt handling, secret input, region validation, environment precedence, profile selection, and owner-only credential persistence.

In `internal/config/store` and `internal/config`:

- stop exposing `ProjectID` on the runtime profile;
- provide a deterministic selected-profile write path that deletes legacy `project_id` during configure;
- preserve unrelated profile sections and credentials;
- do not turn an environment credential source into a persisted `[env]` profile.

### DB Contracts And Starter Provider

In `internal/db` and `internal/db/product/starter`:

- remove `ProjectID` and `ProjectIDExplicit` from generic create options;
- delete default-project resolution logic and its errors, including `db.empty_project_id`;
- keep project concerns out of the generic provider contract.

In `internal/api/starter`:

- remove `ProjectID` from `CreateClusterRequest`;
- stop creating project labels in the request wire model;
- preserve response labels in the existing API model.

No project-specific helper should remain in the create call path merely to pass an empty value.

### Organization And IAM Project Inventory

- delete `internal/organization` because it has no non-project use case;
- remove `Project`, `ListProjectsOptions`, `ListProjectsResponse`, project response wire types, and `ListProjects` from `internal/api/iam`;
- retain SQL-user request and response types and methods in `internal/api/iam`;
- retain IAM endpoint routing used by SQL-user operations;
- remove `authz.OrganizationProjectRead` and its command permission mapping;
- remove the focused `make live-e2e-organization` target and organization live tests;
- remove organization from issue-template command-family choices when no other organization command remains;
- remove installer next steps that recommend project listing.

Do not generalize this removal into deleting all uses of the word `project`. Vercel project IDs, source-code projects, and other unrelated provider concepts are outside this TiDB Cloud Project boundary.

### API Response Fidelity

Public TiDB Cloud response models remain faithful to the service contract:

- preserve the generic cluster `labels` and `annotations` maps;
- preserve any project-related field that is part of an official cluster or future product response;
- do not redact `labels["tidb.cloud/project"]` from create, list, describe, update, wait, dry-run discovery, or error-context responses when that value came from TiDB Cloud;
- do not synthesize project fields when the service omitted them;
- do not copy a returned project value into local configuration or use it in a later request.

Removing `ProjectID` and `ProjectType` from ti's configure result and removing the `--project-id` input do not authorize removing similarly named fields from an API resource response. Configure output is a ti-owned result, and `--project-id` is a ti-owned request selector; neither is an upstream API response field.

The ownership boundary is:

| Surface | Treatment |
| --- | --- |
| `ti configure` result fields `project_id` and `project_type` | Remove because ti synthesizes this local command result. |
| `ti db create-db-cluster --project-id` | Remove because it is a ti-owned request selector. |
| DB create project resolver and outgoing project label | Remove because ti must defer placement to the service. |
| Project-related fields returned by a TiDB Cloud resource API | Preserve exactly as returned. |
| `labels["tidb.cloud/project"]` returned on a cluster | Preserve inside the unmodified labels map. |

## Error Behavior

After this spec:

- `ti configure` can fail for invalid local input, interrupted input, unreadable state, or failed local writes.
- `ti configure` does not return IAM/project discovery errors such as `config.virtual_project_not_found`, `config.virtual_project_ambiguous`, or `config.invalid_virtual_project`.
- `ti db create-db-cluster --project-id ...` fails with Cobra's unknown-flag usage error.
- The first remote command reports authentication or authorization errors using that command's declared permission.
- A server-side create rejection is returned unchanged through the existing API error mapping.
- `ti organization` and `ti organization list-projects` return the normal unknown-command usage error because those commands no longer exist.

Do not add a warning merely because TiDB Cloud assigned a project label in the response. That is expected server behavior.

## API Call Chain

Configure:

```text
flags/environment/prompts -> local validation -> ~/.ti/config + ~/.ti/credentials
```

There is no network call.

Starter create:

```text
ti db create-db-cluster
  -> profile credentials and effective region
  -> DB provider dispatch for starter
  -> POST /v1beta1/clusters without project labels
  -> TiDB Cloud selects project
  -> ti validates Starter metadata and optionally waits for ACTIVE
```

No runtime path calls `GET /v1beta1/projects`.

## Dependencies And Platform Impact

- No new Go module is required.
- No cgo dependency is introduced.
- Configure becomes faster and works offline after the required local inputs are available.
- The CLI no longer declares or exercises `organization.project.read`.
- The change is identical on macOS, Linux, and Windows.
- This is a CLI and configure-output breaking change because `ti organization`, `ti organization list-projects`, `--project-id`, `project_id`, `project_type`, and the saved default project are removed.

## Tests

Unit tests must cover:

- configure writes region and credentials without constructing or calling an IAM client;
- configure succeeds with an unreachable IAM/Starter endpoint because it performs no network request;
- configure JSON and text output contain no project fields;
- reconfiguring a profile removes only that profile's legacy `project_id`;
- a profile containing legacy `project_id` loads successfully but exposes no runtime project selection;
- create options and generic DB contracts contain no project input;
- Starter create request omits `labels` for every invocation, including profiles with a legacy project value;
- dry-run output omits project and labels fields;
- API response project labels remain present in rendered cluster output;
- `--project-id` is absent from help and rejected as an unknown flag;
- the organization parent and project-list subcommand are absent from help and rejected as unknown commands;
- the IAM client retains SQL-user behavior after its project-listing types and methods are removed;
- cluster API response fixtures containing project-related fields or `labels["tidb.cloud/project"]` render those values unchanged.

Black-box `make e2e` must cover:

- interactive and non-interactive configure without an IAM test server;
- config and configure output with no project fields;
- a legacy config containing `project_id` followed by create dry-run and normal fake-API create, proving the request omits labels;
- explicit `--project-id` rejection;
- rejection of the removed `ti organization` and `ti organization list-projects` commands.

`make live-e2e-configure` must verify local persistence without requiring project discovery. Remove `make live-e2e-organization`. `make live-e2e-db` must create a real Starter cluster without an explicit or configured project ID, wait for `ACTIVE`, preserve any server-selected project metadata in the returned resource, and complete the existing branch, SQL, update, and delete lifecycle. The live profile loader must not run configure merely because `project_id` is absent.

## Documentation Updates

When implemented, update:

- `docs/priciples.md` as the product source of truth;
- `AGENTS.md` current behavior, config examples, command examples, and live-e2e requirements;
- `README.md` configure and Starter creation workflows;
- current PingCAP Preview documentation for configure, credentials, Starter DB, create command reference, troubleshooting, and examples;
- remove the organization command reference page and its TOC entries rather than leaving a page for a command that no longer exists;
- remove `ti organization list-projects` from installer next steps, command inventories, examples, and presentation material;
- release notes to identify removal of `--project-id` and configure result fields as a breaking change.

Do not rewrite completed specs or archived release notes to pretend the previous default-project behavior never existed.

## After This Spec

Every Starter cluster created through ti uses TiDB Cloud's server-side default project selection:

```bash
ti configure
ti db create-db-cluster \
  --db-cluster-type starter \
  --db-cluster-name application-db \
  --wait
```

There is no TiDB Cloud project inventory command in ti after this spec. Users do not need to discover or select a project before creating a Starter cluster.

## Acceptance Criteria

- No public `ti db` command accepts a project ID.
- No `ti db` request sends a project ID or project-selection label.
- Starter create omits the project label rather than sending an empty value.
- TiDB Cloud can select the project and ti preserves every returned project-related field or label as resource metadata.
- `ti configure` performs no network request and stores no project ID.
- Existing profile `project_id` values are ignored immediately and removed only when that profile is reconfigured.
- Configure output contains no `project_id` or `project_type`.
- `ti organization` and `ti organization list-projects` are absent from help and rejected as unknown commands.
- No runtime code calls `GET /v1beta1/projects` or declares `organization.project.read`.
- Dry-run, unit, black-box e2e, configure live-e2e, and DB live-e2e coverage prove the projectless request path.
- README and current product documentation match the implemented behavior.

## Out Of Scope

- Choosing or changing the TiDB Cloud service-side default project.
- Moving an existing cluster between projects.
- Hiding, renaming, filtering, or otherwise changing project fields and labels returned by TiDB Cloud.
- Modifying historical completed specs or old release notes.
- Designing project behavior for unimplemented Essential, Premium, or Dedicated providers.
- Removing unrelated concepts such as Vercel project IDs.

## Dependencies

- `docs/spec/done/0002-local-config-and-credentials.md`
- `docs/spec/done/0005-organization-management.md`
- `docs/spec/done/0006-starter-db-cluster-lifecycle.md`
- `docs/spec/done/0017-default-virtual-project-resolution.md`, whose active behavior this spec supersedes
- `docs/spec/done/0026-db-provider-dispatch-and-starter-refactor.md`
- `docs/spec/done/0027-ti-cli-rename-and-migration.md`
