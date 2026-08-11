---
title: AGENTS.md - ti development guide for AI coding agents
---

# Repository Overview

ti is a Go command-line product for TiDB Cloud Starter. It is designed to be
agent-friendly, predictable, scriptable, and safe for automation.

Module: `github.com/tidbcloud/ti-cli`
Go version: 1.26.5 (see `go.mod`)

The most important product document is `docs/priciples.md`. Treat that file as
the source of truth for product principles. Requirement specs live in
`docs/spec/`; completed specs are moved to `docs/spec/done/`.

Completed specs that mention `tdc` describe the pre-v0.2 product name. Do not
mechanically rename those historical records. Current code, documentation,
commands, paths, and environment variables use `ti`.

## Current Implementation Status

Implemented:

- Ti CLI repository, executable, local-state, environment-variable, installer,
  updater, release, telemetry, and documentation rename with safe v0.1 state
  migration from `docs/spec/done/0027-ti-cli-rename-and-migration.md`
- CLI foundation from `docs/spec/done/0001-cli-foundation.md`
- Local config and credentials from
  `docs/spec/done/0002-local-config-and-credentials.md`
- Global settings and operation logging configuration from
  `docs/spec/done/0021-global-settings.md`
- Privacy-preserving CLI telemetry from
  `docs/spec/done/0022-telemetry.md`
- Telemetry environment metadata from
  `docs/spec/done/0025-telemetry-environment-metadata.md`
- Output, query, and dry-run contracts from
  `docs/spec/done/0003-output-error-query-dry-run.md`
- API client auth, authorization, and region routing from
  `docs/spec/done/0004-api-client-auth-and-region-routing.md`
- Organization project listing from
  `docs/spec/done/0005-organization-management.md`
- Starter DB cluster lifecycle from
  `docs/spec/done/0006-starter-db-cluster-lifecycle.md`
- Starter DB branch lifecycle from
  `docs/spec/done/0007-starter-db-branch-lifecycle.md`
- Starter DB SQL access and query from
  `docs/spec/done/0008-starter-db-sql-access-and-query.md`
- Default virtual project discovery and DB create resolution from
  `docs/spec/done/0017-default-virtual-project-resolution.md`
- Starter-only DB resource guardrails from
  `docs/spec/done/0023-starter-only-db-resource-guardrails.md`
- Region-scoped Starter DB cluster listing from
  `docs/spec/done/0024-region-scoped-db-cluster-listing.md`
- Product-aware DB dispatch, Starter package isolation, dynamic permissions,
  and filtered pagination from
  `docs/spec/done/0026-db-provider-dispatch-and-starter-refactor.md`
- ti fs Unix-style command aliases from
  `docs/spec/done/0014-tdc-fs-unix-command-aliases.md`
- ti fs control plane from
  `docs/spec/done/0009-tdc-fs-control-plane.md`
- ti fs data plane from
  `docs/spec/done/0010-tdc-fs-data-plane.md`
- ti fs mount runtime from
  `docs/spec/done/0011-tdc-fs-mount-runtime.md`
- ti fs FUSE correctness and Drive9 parity extension from
  `docs/spec/done/0011-ext01-fuse-cache-and-open-handle-correctness.md`
- profile-scoped 1:N ti fs resource registry from
  `docs/spec/done/0016-profile-fs-resource-registry.md`
- explicit ti fs resource selection from
  `docs/spec/done/0020-explicit-file-system-selection.md`
- FS token authentication and configuration-free access from
  `docs/spec/done/0018-fs-token-auth-and-config-free-access.md`
- install and update distribution from
  `docs/spec/done/0012-install-and-update-distribution.md`
- English PingCAP Preview documentation from
  `docs/spec/done/0019-pingcap-tdc-documentation.md`
- `ti configure`
- `ti update --check`
- `ti update`
- `ti organization list-projects`
- `ti db create-db-cluster`
- `ti db list-db-clusters`
- `ti db describe-db-cluster`
- `ti db update-db-cluster`
- `ti db delete-db-cluster`
- `ti db create-db-cluster-branch`
- `ti db list-db-cluster-branches`
- `ti db describe-db-cluster-branch`
- `ti db delete-db-cluster-branch`
- `ti db create-db-sql-users`
- `ti db format-db-connection-string`
- `ti db execute-sql-statement`
- `ti fs create-file-system`
- `ti fs import-file-system-token`
- `ti fs delete-file-system`
- `ti fs list-file-systems`
- `ti fs describe-file-system`
- `ti fs check-file-system`
- `ti fs copy-file`
- `ti fs read-file`
- `ti fs list-files`
- `ti fs describe-file`
- `ti fs move-file`
- `ti fs delete-file`
- `ti fs create-directory`
- `ti fs chmod-file`
- `ti fs create-symlink`
- `ti fs create-hardlink`
- `ti fs search-file-content`
- `ti fs find-files`
- `ti fs create-layer`
- `ti fs list-layers`
- `ti fs describe-layer`
- `ti fs diff-layer`
- `ti fs create-layer-checkpoint`
- `ti fs rollback-layer`
- `ti fs commit-layer`
- `ti fs mount-file-system`
- `ti fs drain-file-system`
- `ti fs unmount-file-system`
- Unix-style `ti fs` command aliases: `cp`, `cat`, `ls`, `stat`, `mv`, `rm`,
  `mkdir`, `chmod`, `symlink`, `hardlink`, `grep`, `find`, `mount`, `drain`,
  and `umount`
- `ti fs-vault create-secret`
- `ti fs-vault replace-secret`
- `ti fs-vault read-secret`
- `ti fs-vault list-secrets`
- `ti fs-vault delete-secret`
- `ti fs-vault create-grant`
- `ti fs-vault delete-grant`
- `ti fs-vault list-audit-events`
- `ti fs-vault run-with-secret`
- `ti fs-vault mount-vault`
- `ti fs-vault unmount-vault`
- `ti fs-journal create-journal`
- `ti fs-journal append-journal-entries`
- `ti fs-journal read-journal-entries`
- `ti fs-journal search-journal-entries`
- `ti fs-journal verify-journal`
- help and version behavior at every command level
- structured JSON/text rendering and JMESPath `--query`
- `--dry-run` on mutating control-plane commands
- TiDB Cloud Digest-auth API client foundation and auth/authz error mapping
- region-scoped remote ti fs inventory, profile-scoped ID-keyed credentials,
  and legacy credential migration from
  `docs/spec/done/0028-remote-fs-resource-inventory.md`
- ti fs/fs-git/fs-journal/fs-vault commands routed through the bundled
  `ti-drive9` companion, with ti-owned profile loading, credential storage,
  region resolution, and output/error handling
- Drive9 public CLI coverage for ti fs data-plane operations, FUSE/WebDAV
  mount, mount drain, layers, pack/unpack, vault, journal, and Git clone,
  hydrate, add-worktree, and remove-worktree workflows
- GoReleaser/GitHub Releases install and update workflow
- Makefile build/test/e2e workflow
- independent telemetry ingestion backend with strict schema validation,
  bounded in-memory batching, TiDB storage, and personless PostHog forwarding

There are no registered placeholder commands at the current stage. Implemented
mutating commands support `--dry-run` where their command contract declares
dry-run support.

The completed remote ti fs inventory implementation and its verified regional
rollout status are recorded in
`docs/spec/done/0028-remote-fs-resource-inventory.md`. Future Drive9 region
publication and cleanup of historical backend tenant bindings are external
deployment work and do not reopen the ti client spec.

## Reference Code

- `ref/tidbcloud-cli/` is the previous TiDB Cloud CLI implementation. Use it as
  a reference for TiDB Cloud concepts, profile handling, output helpers,
  telemetry, and API client patterns.
- `ref/drive9/` is the filesystem reference implementation. Use it as context
  for filesystem commands, mount behavior, and data-plane semantics. In ti
  user-facing output, this domain is always called `ti fs`.
- `ref/fs/` is the TiDB Filesystem server deployed for the Drive9-backed TiDB
  Cloud Filesystem service. Use it to verify server routes, TiDB Cloud IAM and
  billing authorization, tenant inventory and lifecycle behavior, quotas, and
  data-plane contracts. It is server reference code, not a ti dependency.
- `ref/serverless-js/` is a reference for the HTTPS SQL API call shape.
Reference directories are not product source for ti. They exist only to give
agents context and implementation examples. In main project code, behave as if
`ref/` does not exist:

- Do not import packages from `ref/`.
- Do not add `replace`, workspace, module, script, or build-time dependencies on
  anything under `ref/`.
- Do not make tests depend on code, data, fixtures, or generated artifacts under
  `ref/`.
- Exclude `ref/` from build, test, lint, release, and packaging flows.

Do not rewrite reference directories unless the task explicitly asks for
reference changes.

## Build And Test Commands

Use the Makefile targets:

```bash
make build
make build-telemetry-backend
make test
make e2e
make telemetry-e2e
make live-e2e-configure
make live-e2e-organization
make live-e2e-db
make live-e2e-fs
make live-e2e-fs-git
make live-e2e-fs-journal
make live-e2e-fs-vault
make live-e2e
make release-snapshot
make clean
```

`make build` writes the binary to `bin/ti`.
`make build-telemetry-backend` writes the independent ingestion service to
`bin/ti-telemetry-backend`.
`make build-telemetry-migrator` writes the one-shot Goose migration runner to
`bin/ti-telemetry-migrate`.

`make test` runs ordinary Go tests and must not require live cloud credentials.
`make e2e` builds `bin/ti` and runs black-box tests against the real binary via
`TI_E2E_BIN`.
`make telemetry-e2e` is separately opt-in. It loads the ignored
`e2e/.env.telemetry` file, requires a test-only `TI_TEST_TELEMETRY_TIDB_DSN`
whose user can create and drop databases, and creates a unique temporary TiDB
database. It verifies Goose initialization from empty state, an additive
upgrade preserving a legacy event, and the real local CLI-to-backend-to-TiDB
delivery path before dropping only that temporary database. It must not run as
part of `make test`, `make e2e`, or any live-e2e target.
The `make live-e2e-<family>` targets build `bin/ti` and run only the selected
top-level command family against the `live-e2e` profile by default. Keep
configure, organization, db, fs, fs-git, fs-journal, and fs-vault tests
independently selectable. Do not make a focused family target run tests from a
different family, and do not add separate mutating/non-mutating variants.
`make live-e2e` runs every live family together in one test process and remains
the full release/CI verification suite. `LIVE_E2E_PROFILE=<profile>` overrides
the profile for both focused and complete live targets.
Live e2e must strictly cover every implemented interface and command for the
current project stage, including real create/update/delete flows when those
commands are implemented. For Starter DB clusters, the live suite creates a
uniquely named `ti-e2e-*` cluster with `--wait`, without a
spending limit or explicit/configured project ID, verifies the returned state
is `ACTIVE` and has a non-empty server-selected project label, and deletes only
that cluster. The server-selected account default is not required to equal the
`tidbx_virtual` project discovered by `ti configure`. For Starter DB branches,
the live suite creates, reads, lists, and deletes only a `ti-e2e-branch-*`
branch on the cluster created by the same test run. Branch creation must use
`--wait`; cluster deletion must use `--wait`. For Starter DB SQL access, the live suite prepares ti-managed
read-only, read-write, and admin SQL users on the temporary cluster, verifies
connection string output, and executes the HTTPS SQL API with all three access
modes.
For ti fs data-plane and mount runtime, the live suite creates uniquely named
remote paths, exercises real file create/read/list/copy/move/delete flows,
range reads, append, resume, recursive local/remote copy, stdin/stdout copy,
tags/descriptions, chmod, symlink, hardlink, pack/unpack, real public layer
create/list/describe/diff/checkpoint/rollback/commit flows, layer-aware
copy/find where Drive9 exposes it, vault create/read/replace/delete, delegated
vault grant reads, vault mount read on macOS/Linux hosts when available,
journal create/append/read/search/verify, public Git clone/hydrate/worktree
flows, mount and drain through the companion runtime, and explicit WebDAV
fallback when the platform supports it.
If remote inventory has no resource with a local token, the suite creates one
temporary ti fs resource, records the server-assigned ID, and deletes only
that ID before the DB lifecycle needs the Starter slot or when the process
exits. `TI_LIVE_FS_ID` may select a remotely visible resource that already has
local credentials. Never delete a pre-existing resource to make room for a
test. Fake-companion e2e covers multiple remote resources and ID routing.
When a service command is implemented, add its real live verification to
`make live-e2e`; do not leave the target at profile, smoke-test-only, or
mock-only coverage.

For focused work, direct Go commands are also fine:

```bash
go test ./...
go test ./internal/config -run TestName
go build ./cmd/ti
```

Build and release artifacts are ignored through `.gitignore`. Do not commit
binaries or GoReleaser `dist/` output.

Formatting should be standard Go formatting via `gofmt`. Do not run formatters
that rewrite unrelated files.

## Project Layout

Current layout:

```text
cmd/ti/                    CLI entrypoint
cmd/ti-telemetry-backend/  independent telemetry ingestion service entrypoint
cmd/ti-telemetry-migrate/  one-shot embedded Goose migration runner
internal/api/               shared HTTP API client and service clients
internal/api/endpoints/     provider/region endpoint resolver
internal/api/transport/     Digest/Bearer/debug HTTP transports
internal/apperr/            typed CLI errors and exit-code helpers
internal/auth/              authenticated profile validation and transports
internal/authz/             permission constants and permission errors
internal/cli/               command wiring
internal/config/            profile loading and precedence rules
internal/config/configure/  interactive configure wizard
internal/config/envcompat/  canonical and legacy environment compatibility
internal/config/fsresource/ legacy flat ti fs migration key names
internal/config/homemigration/ atomic pre-v0.2 state migration
internal/config/region/     provider and region validation
internal/config/store/      TOML read/write, file modes, atomic writes
internal/db/                DB type discovery, capability dispatch, and pagination
internal/db/product/        concrete database product implementations
internal/db/product/starter/ TiDB Cloud Starter cluster, branch, and SQL provider
internal/db/connectionstring/ DB connection string formatters
internal/db/sqlaccess/      DB SQL user preparation logic
internal/db/sqlcred/        cluster-scoped DB SQL credential store
internal/db/sqlhttp/        HTTPS SQL API transport
internal/db/sqlmysql/       explicit MySQL fallback transport
internal/db/sqlresult/      SQL result model and decoding
internal/db/sqlsingle/      one-statement validation
internal/db/validate/       DB flag and request validation helpers
internal/dryrun/            shared dry-run result envelope
internal/fs/                ti fs control-plane, data-plane, and mount use cases
internal/fs/fscred/         ID-keyed ti fs credentials, selection, and legacy migration
internal/fs/mountlocator/   non-secret Drive9 background mount routing state
internal/oplog/             local JSONL operation log writer
internal/output/            structured JSON/text/raw rendering
internal/organization/      organization project command use cases
internal/query/             JMESPath query application
internal/secretinput/       no-echo secret input helper
internal/settings/          global settings parsing and legacy logging migration
internal/telemetry/         CLI eligibility, identity, event, and delivery path
internal/telemetrybackend/  telemetry API, batcher, TiDB, and PostHog sinks
internal/update/            GitHub Releases update checks and self-update logic
internal/version/           build version metadata
scripts/                    installer scripts
deploy/telemetry/           telemetry Docker Compose and Caddy deployment
e2e/                        black-box tests against the compiled binary
docs/priciples.md           product principles and MVP scope source of truth
docs/spec/                  pending requirement specs
docs/spec/done/             completed requirement specs
docs/pingcap-docs/docs/     pingcap/docs English documentation submodule
ref/                        read-only client and server reference implementations
```

Keep one package per directory. Package names should be short, lowercase, and
without underscores.

## CLI Product Rules

Follow these rules unless `docs/priciples.md` is updated:

- The command tree is at most two levels: `ti <command> [subcommand]`.
- `ti configure` and `ti update` are the only intentional top-level verb
  exceptions. `ti configure` is the only interactive command.
- Other top-level commands are nouns such as `db`, `fs`, and `organization`.
- Use long flags only, for example `--profile` and `--db-cluster-name`.
- Do not add short flags or one-letter aliases. The current CLI rejects short
  flags before invoking Cobra.
- `ti fs` Unix-style aliases are command-name aliases only. They must keep the
  same long flags, output modes, auth, permissions, dry-run behavior, and command
  handlers as their canonical commands.
- Do not prompt for input except inside `ti configure`.
- Successful structured control-plane commands output JSON by default.
- Implement DB, organization, and fs control-plane commands through
  `controlPlaneCommandSpec` in `internal/cli`, so normal execution, dry-run,
  output rendering, and query handling stay on the shared path.
- Non-DB control-plane commands must declare exactly one `authz.Permission` in
  their command spec. DB commands declare one `db.Operation`; the selected
  product provider maps that operation to its permission. Do not infer
  permissions from command names or SQL text.
- Mutating control-plane commands support `--dry-run`.
- `--dry-run` must validate local config, credentials, provider, and region
  before reporting a planned mutation.
- `ti db create-db-cluster --wait` waits up to 12 minutes for the
  created cluster to reach `ACTIVE`. It must never delete the cluster on
  timeout, cancellation, a polling failure, or a terminal state; errors must
  retain the created cluster ID and provide an inspection command.
- `ti db create-db-cluster-branch --wait` waits up to 5 minutes
  for the created branch to reach `ACTIVE`. A failed wait must not delete or
  recreate the accepted branch.
- `ti db delete-db-cluster --wait` waits up to 12 minutes and
  succeeds when the API reports `DELETED` or the deleted cluster is no longer
  accessible. A failed wait must state that deletion may still be in progress.
- `ti fs create-file-system --wait` waits up to 10 minutes for the
  root to become readable through the public Drive9 CLI. It must retain the
  resource and local credentials when waiting fails.
- `ti fs delete-file-system` is asynchronous. After Drive9 accepts deletion,
  output status is `deleting`, not `deleted`.
- Read-only commands reject `--dry-run`.
- Apply `--query` after command execution and before rendering.
- Users provide cloud placement as one canonical `region_code`, never as
  separate provider/region fields or server URLs.
- DB commands without a required cluster ID require exact lowercase
  `--db-cluster-type starter`; there is no default. ID-based commands do not
  expose that flag. They discover `servicePlan`, use `clusterPlan` only as a
  legacy fallback, and dispatch through capability interfaces. Reject
  recognized but unsupported products and missing, unknown, or conflicting
  plans before the product operation.
- Only `ti db` uses dynamic operation-to-permission mapping. Keep FS and
  organization command permissions static. The CLI composition root registers
  product resolvers/providers; the root `internal/db` package must not import
  child product packages.
- `ti db list-db-clusters --db-cluster-type starter` adds an immutable API
  filter for the effective provider and region, scans upstream pages of 100,
  and incrementally fills the requested ti page with verified Starter
  clusters. Never load all account clusters into memory. Return a ti-owned
  opaque cursor that binds profile, type, region, filter, and order and records
  replay offset/fingerprint. Do not expose the upstream token or total size.
  Missing or conflicting region metadata is excluded.
- The global `--region <canonical-region-code>` flag overrides placement for
  the current command only. It has higher priority than `TI_REGION_CODE` and
  profile `region_code`, but it must not change the selected profile or
  credential source.
- Every command should be usable by scripts and agents without
  terminal-specific assumptions.
- When creating a pull request, check whether the target repository has a pull
  request template. If it does, follow that template when writing the pull
  request description.
- Help must work as:
  - `ti help`
  - `ti <command> help`
  - `ti <command> <subcommand> help`
- In generated Flags and Global Flags sections, render value types as
  `<type>` and append `(required)` to flags marked with
  `ti_usage_required`.
- Keep the global `--version` behavior intact at every command level. Do not
  add command-specific `--version <value>` flags; use names such as
  `--target-version` when a command needs a version input.
- `ti update --check` and `ti update` use GitHub Releases metadata and
  must not read or mutate `~/.ti/` or inspect/migrate `~/.ti/`.
- `ti update` may replace only ti-owned archive/script installs. It must
  refuse local, unknown, Homebrew, Scoop, Winget, or other package-manager
  installs with actionable guidance.
- `ti update` is itself explicit update intent and must not require `--yes`.
  It downloads, extracts, verifies, stages, and replaces artifacts as the
  current user and must never invoke sudo or another privilege escalation
  mechanism.
- Installer scripts default to the stable user-owned `~/.ti/bin` directory on
  macOS, Linux, and Windows unless `--install-dir`/`-InstallDir` or
  `TI_INSTALL_DIR` overrides it. They must not prefer or overwrite an active
  system-level ti found on PATH, invoke sudo, create system-directory
  symlinks, or modify shell profile files automatically.
- Installer scripts must detect PATH shadowing, bootstrap `~/.ti/config` only
  when missing, print the exact command that prepends `~/.ti/bin` to PATH,
  print DB and ti fs region lists, and show clear next steps. They must never
  write `~/.ti/credentials`.
- Installer scripts must invoke the staged `ti` binary's shared home migration
  before creating the default `~/.ti/bin` destination. Do not duplicate state
  copying in shell or PowerShell.

## Commands

Implemented command behavior:

- `ti` without a command returns `cli.missing_command` with exit code `2` and
  an AWS-style compact two-level usage synopsis on stderr
- `ti configure`
- `ti configure --non-interactive`
- `ti help`
- `ti --version`
- `ti <command> help`
- `ti <command> <subcommand> help`
- `ti <command> --version`
- `ti <command> <subcommand> --version`
- `ti update --check`
- `ti update --check --fail-if-update-available`
- `ti update --dry-run`
- `ti update`
- `ti update --target-version v0.1.1`
- `ti organization list-projects`
- `ti organization list-projects --query 'projects[0].id'`
- `ti organization list-projects --output text`
- `ti db create-db-cluster --db-cluster-type starter --db-cluster-name demo`
- `ti db create-db-cluster --db-cluster-type starter --db-cluster-name demo --wait`
- `ti db create-db-cluster --db-cluster-type starter --db-cluster-name demo --dry-run`
- `ti db create-db-cluster --db-cluster-type starter --db-cluster-name demo --project-id <project-id>`
- `ti db list-db-clusters --db-cluster-type starter`
- `ti db list-db-clusters --db-cluster-type starter --query 'clusters[].id'`
- `ti db describe-db-cluster --db-cluster-id <cluster-id>`
- `ti db update-db-cluster --db-cluster-id <cluster-id> --db-cluster-name new-name`
- `ti db update-db-cluster --db-cluster-id <cluster-id> --monthly-spending-limit-usd-cents 1000 --dry-run`
- `ti db delete-db-cluster --db-cluster-id <cluster-id>`
- `ti db delete-db-cluster --db-cluster-id <cluster-id> --wait`
- `ti db delete-db-cluster --db-cluster-id <cluster-id> --dry-run`
- `ti db create-db-cluster-branch --db-cluster-id <cluster-id> --db-cluster-branch-name dev`
- `ti db create-db-cluster-branch --db-cluster-id <cluster-id> --db-cluster-branch-name dev --wait`
- `ti db create-db-cluster-branch --db-cluster-id <cluster-id> --db-cluster-branch-name dev --dry-run`
- `ti db list-db-cluster-branches --db-cluster-id <cluster-id>`
- `ti db list-db-cluster-branches --db-cluster-id <cluster-id> --query 'branches[].id'`
- `ti db list-db-cluster-branches --db-cluster-id <cluster-id> --output text`
- `ti db describe-db-cluster-branch --db-cluster-id <cluster-id> --db-cluster-branch-id <branch-id>`
- `ti db delete-db-cluster-branch --db-cluster-id <cluster-id> --db-cluster-branch-id <branch-id>`
- `ti db delete-db-cluster-branch --db-cluster-id <cluster-id> --db-cluster-branch-id <branch-id> --dry-run`
- `ti db create-db-sql-users --db-cluster-id <cluster-id>`
- `ti db create-db-sql-users --db-cluster-id <cluster-id> --dry-run`
- `ti db format-db-connection-string --db-cluster-id <cluster-id>`
- `ti db format-db-connection-string --db-cluster-id <cluster-id> --read-write --format mysql-uri`
- `ti db format-db-connection-string --db-cluster-id <cluster-id> --read-only --format env`
- `ti db format-db-connection-string --db-cluster-id <cluster-id> --admin --format jdbc`
- `ti db execute-sql-statement --db-cluster-id <cluster-id> --sql "select 1"`
- `ti db execute-sql-statement --db-cluster-id <cluster-id> --read-write --sql "select 1"`
- `ti db execute-sql-statement --db-cluster-id <cluster-id> --read-only --sql "select 1"`
- `ti db execute-sql-statement --db-cluster-id <cluster-id> --admin --sql "select 1"`
- `ti db execute-sql-statement --db-cluster-id <cluster-id> --transport https --sql "select 1"`
- `ti db execute-sql-statement --db-cluster-id <cluster-id> --transport mysql --sql "select 1"`
- `ti fs create-file-system`
- `ti fs create-file-system --wait`
- `ti fs create-file-system --dry-run`
- `ti fs import-file-system-token --from-file ./fs-token`
- `ti fs delete-file-system --file-system-id <file-system-id>`
- `ti fs delete-file-system --file-system-id <file-system-id> --dry-run`
- `ti fs list-file-systems`
- `ti fs describe-file-system --file-system-id <file-system-id>`
- `ti fs check-file-system`
- `ti fs check-file-system --file-system-id <file-system-id>`
- `ti fs copy-file --from-local ./README.md --to-remote /workspace/README.md`
- `ti fs copy-file --from-remote /workspace/README.md --to-local ./README.copy.md --create-parents`
- `ti fs copy-file --from-remote /workspace/README.md --to-remote /workspace/README.copy.md`
- `ti fs read-file --path /workspace/README.md`
- `ti fs read-file --path /workspace/README.md --offset 0 --length 1024`
- `ti fs copy-file --from-local ./tail.log --to-remote /workspace/app.log --append`
- `ti fs copy-file --from-remote /workspace/large.bin --to-local ./large.bin --resume`
- `ti fs copy-file --from-local ./large.bin --to-remote /workspace/large.bin --resume`
- `ti fs copy-file --from-local ./src-dir --to-remote /workspace/src-dir --recursive`
- `ti fs copy-file --from-remote /workspace/src-dir --to-local ./src-dir.copy --recursive`
- `ti fs copy-file --from-remote /workspace/src-dir --to-remote /workspace/src-dir.copy --recursive`
- `ti fs copy-file --from-stdin --to-remote /workspace/stdin.txt --tag source=stdin --description "stdin upload"`
- `ti fs copy-file --from-remote /workspace/stdin.txt --to-stdout`
- `ti fs list-files --path /workspace`
- `ti fs list-files --path /workspace --output text`
- `ti fs describe-file --path /workspace/README.md`
- `ti fs move-file --from-remote /workspace/README.copy.md --to-remote /workspace/archive/README.md`
- `ti fs delete-file --path /workspace/archive/README.md`
- `ti fs delete-file --path /workspace --recursive`
- `ti fs create-directory --path /workspace/archive --mode 0755`
- `ti fs chmod-file --path /workspace/README.md --mode 0600`
- `ti fs create-symlink --target README.md --link-path /workspace/README.link`
- `ti fs create-hardlink --source-path /workspace/README.md --link-path /workspace/README.hard`
- `ti fs search-file-content --path /workspace --pattern "hello"`
- `ti fs search-file-content --path /workspace --pattern "hello" --layer-id layer-1`
- `ti fs find-files --path /workspace --file-name-pattern "*.md"`
- `ti fs find-files --path /workspace --file-name-pattern "*.md" --layer-id layer-1`
- `ti fs create-layer --layer-id layer-1 --base-root-path /workspace --layer-name task --durability-mode restore-safe --tag task=auth`
- `ti fs list-layers`
- `ti fs list-layers --output text`
- `ti fs describe-layer --layer-id layer-1`
- `ti fs diff-layer --layer-id layer-1`
- `ti fs copy-file --from-local ./README.md --to-remote /workspace/layered.md --layer-id layer-1`
- `ti fs create-layer-checkpoint --layer-id layer-1 --checkpoint-id cp-1 --label before-commit`
- `ti fs rollback-layer --layer-id layer-1`
- `ti fs commit-layer --layer-id layer-1`
- `ti fs pack-file-system --local-root ~/.ti/local/fs/demo --remote-root /workspace --mount-profile portable`
- `ti fs pack-file-system --mount-path ./workspace`
- `ti fs unpack-file-system --local-root ~/.ti/local/fs/demo --remote-root /workspace --mount-profile portable`
- `ti fs mount-file-system --file-system-id <file-system-id> --mount-path ./workspace`
- `ti fs mount-file-system --file-system-id <file-system-id> --mount-path ./workspace --driver fuse`
- `ti fs mount-file-system --file-system-id <file-system-id> --mount-path ./workspace --driver webdav`
- `ti fs mount-file-system --file-system-id <file-system-id> --mount-path ./workspace --mount-profile coding-agent`
- `ti fs mount-file-system --file-system-id <file-system-id> --mount-path ./workspace --mount-profile portable --pack-path /`
- `ti fs mount-file-system --file-system-id <file-system-id> --mount-path ./workspace --driver fuse --read-cache-size-mb 256 --read-cache-max-file-mb 16`
- `ti fs mount-file-system --file-system-id <file-system-id> --mount-path ./workspace --driver fuse --cache-dir ~/.ti/cache/workspace --write-back-cache=false`
- `ti fs drain-file-system --mount-path ./workspace`
- `ti fs drain-file-system --mount-path ./workspace --timeout 30s`
- `ti fs unmount-file-system --mount-path ./workspace`
- `ti fs unmount-file-system --mount-path ./workspace --ignore-absent`
- `ti fs-vault create-secret --secret-name db-prod --field DB_URL=mysql://example --field PASSWORD=@./password.txt`
- `ti fs-vault replace-secret --secret-path /n/vault/db-prod --from-directory ./secret-fields`
- `ti fs-vault read-secret --secret-name db-prod`
- `ti fs-vault read-secret --secret-name db-prod --field PASSWORD --format raw`
- `ti fs-vault read-secret --secret-name db-prod --field DB_URL --format env`
- `ti fs-vault list-secrets`
- `ti fs-vault delete-secret --secret-name db-prod`
- `ti fs-vault create-grant --agent-id deploy-agent --scope db-prod/DB_URL --permission read --ttl 10m`
- `ti fs-vault delete-grant --grant-id <grant-id> --reason rotated`
- `ti fs-vault list-audit-events --secret-name db-prod --limit 20`
- `ti fs-vault run-with-secret --secret-path /n/vault/db-prod -- env`
- `ti fs-vault mount-vault --mount-path ./vault --vault-token "$TI_VAULT_TOKEN"`
- `ti fs-vault unmount-vault --mount-path ./vault`
- `ti fs-journal create-journal --journal-id jrn-demo --journal-kind agent --title "demo task" --actor agent:ti --label env=dev`
- `ti fs-journal append-journal-entries --journal-id jrn-demo --entry-json '{"type":"task.started"}'`
- `ti fs-journal read-journal-entries --journal-id jrn-demo --after-seq 0 --limit 100`
- `ti fs-journal search-journal-entries --entry-type task.started --label env=dev --include-entries`
- `ti fs-journal verify-journal --journal-id jrn-demo --output text`
- `ti fs-git clone-git-workspace --repo-url https://github.com/pingcap/tidb.git --target-path ./workspace/tidb`
- `ti fs-git clone-git-workspace --repo-url https://github.com/pingcap/tidb.git --target-path ./workspace/tidb --blobless --hydrate sync`
- `ti fs-git hydrate-git-workspace --target-path ./workspace/tidb --timeout 30m`
- `ti fs-git add-git-worktree --base-path ./workspace/tidb --worktree-path ./workspace/tidb-feature --branch-name feature-x`
- `ti fs-git remove-git-worktree --worktree-path ./workspace/tidb-feature --force`

Registered command surface:

- `ti update --check`
- `ti update`
- `ti organization list-projects`
- `ti db create-db-cluster`
- `ti db list-db-clusters`
- `ti db describe-db-cluster`
- `ti db update-db-cluster`
- `ti db delete-db-cluster`
- `ti db create-db-cluster-branch`
- `ti db list-db-cluster-branches`
- `ti db describe-db-cluster-branch`
- `ti db delete-db-cluster-branch`
- `ti db create-db-sql-users`
- `ti db format-db-connection-string`
- `ti db execute-sql-statement`
- `ti fs create-file-system`
- `ti fs import-file-system-token`
- `ti fs delete-file-system`
- `ti fs list-file-systems`
- `ti fs describe-file-system`
- `ti fs check-file-system`
- `ti fs copy-file`
- `ti fs read-file`
- `ti fs list-files`
- `ti fs describe-file`
- `ti fs move-file`
- `ti fs delete-file`
- `ti fs create-directory`
- `ti fs chmod-file`
- `ti fs create-symlink`
- `ti fs create-hardlink`
- `ti fs search-file-content`
- `ti fs find-files`
- `ti fs create-layer`
- `ti fs list-layers`
- `ti fs describe-layer`
- `ti fs diff-layer`
- `ti fs create-layer-checkpoint`
- `ti fs rollback-layer`
- `ti fs commit-layer`
- `ti fs pack-file-system`
- `ti fs unpack-file-system`
- `ti fs mount-file-system`
- `ti fs drain-file-system`
- `ti fs unmount-file-system`
- `ti fs cp` aliases `ti fs copy-file`
- `ti fs cat` aliases `ti fs read-file`
- `ti fs ls` aliases `ti fs list-files`
- `ti fs stat` aliases `ti fs describe-file`
- `ti fs mv` aliases `ti fs move-file`
- `ti fs rm` aliases `ti fs delete-file`
- `ti fs mkdir` aliases `ti fs create-directory`
- `ti fs chmod` aliases `ti fs chmod-file`
- `ti fs symlink` aliases `ti fs create-symlink`
- `ti fs hardlink` aliases `ti fs create-hardlink`
- `ti fs grep` aliases `ti fs search-file-content`
- `ti fs find` aliases `ti fs find-files`
- `ti fs mount` aliases `ti fs mount-file-system`
- `ti fs drain` aliases `ti fs drain-file-system`
- `ti fs umount` aliases `ti fs unmount-file-system`
- `ti fs-vault create-secret`
- `ti fs-vault replace-secret`
- `ti fs-vault read-secret`
- `ti fs-vault list-secrets`
- `ti fs-vault delete-secret`
- `ti fs-vault create-grant`
- `ti fs-vault delete-grant`
- `ti fs-vault list-audit-events`
- `ti fs-vault run-with-secret`
- `ti fs-vault mount-vault`
- `ti fs-vault unmount-vault`
- `ti fs-journal create-journal`
- `ti fs-journal append-journal-entries`
- `ti fs-journal read-journal-entries`
- `ti fs-journal search-journal-entries`
- `ti fs-journal verify-journal`
- `ti fs-git clone-git-workspace`
- `ti fs-git hydrate-git-workspace`
- `ti fs-git add-git-worktree`
- `ti fs-git remove-git-worktree`

Do not rename commands without updating specs, README, e2e tests, and AGENTS.
Any code change that changes user-visible behavior must keep README.md in sync.
It must also update the matching English ti pages under
`docs/pingcap-docs/docs/` when the published behavior changes. Use globally
unique `ti-` basenames, preserve the standard Preview note on every page, and
update `TOC-ai.md` and `ai/_index.md` when adding or removing a document.
Validate command names and flags against the compiled CLI help, not historical
specs or demos.

## Configuration And Credentials

All ti local state belongs under `~/.ti/`.

For v0.2.x, `internal/config/homemigration` performs a one-way migration when
only `~/.tdc` exists. It copies only durable config, credential, preferences,
telemetry identity, DB-user, and filesystem-registry state through a sibling
staging directory, validates it, then atomically publishes `~/.ti`. It never
modifies or deletes `~/.tdc`. Both homes, unsafe source files, or an active old
mount are hard errors unless the new home contains the valid owner-only
`.migrated-from-tdc` marker produced by the atomic migration. Runtime logs,
caches, local data, mount locators, and the old Drive9 home are not migrated.
Normal non-update commands and both installers use the same Go implementation;
every update mode bypasses it.

Canonical environment variables use `TI_*`, except TiDB Cloud API credentials
which use `TIDB_CLOUD_PUBLIC_KEY` and `TIDB_CLOUD_PRIVATE_KEY`. During v0.2.x,
the corresponding `TDC_*` variables are accepted only as deterministic legacy
fallbacks. If both names are present with different values, fail with
`config.environment_conflict` before local or remote mutation. Never print a
legacy-variable warning into command output; operation logs may record only
the deprecated variable name.

- `~/.ti/config` stores profile-scoped non-sensitive TOML values.
- `~/.ti/credentials` stores profile-scoped sensitive TOML values.
- Both files use profile sections such as `[default]` and `[stage]`.
- `~/.ti/.preferences` is optional hidden global TOML configuration and is
  never selected by profile. Fresh installs and `ti configure` do not create
  it. Do not create or migrate the unshipped intermediate `~/.ti/settings`
  path.
- `~/.ti/.telemetry-installation-id` is a machine-generated pseudonymous
  telemetry identity, not TOML configuration. It is created lazily with mode
  `0600` only for an eligible, effectively enabled invocation with a
  build-configured endpoint. Users can delete it to reset the identity.
- The profile name `logging` is reserved so legacy global logging configuration
  cannot be confused with a profile.
- The default profile name is `default`.
- The global `--profile` flag selects a profile when explicitly provided.
- The global `--region` flag selects command-scope placement when explicitly
  provided and must reject an explicit empty value.
- `ti configure` writes canonical `region_code`, discovers the unique
  `tidbx_virtual` project as `project_id`, and writes
  `tidb_cloud_public_key` and `tidb_cloud_private_key`.
- `ti configure --non-interactive` must not prompt. It reads values from flags
  first, then `TI_REGION_CODE`, `TIDB_CLOUD_PUBLIC_KEY`, and `TIDB_CLOUD_PRIVATE_KEY`.
  Missing values fail with an actionable error.
- For CI/CD, prefer environment variables for private keys over command-line
  secret flags.
- Interactive `ti configure` must respond to Ctrl+C and surface an
  `interrupted` error with exit code 130.
- The credentials file is restricted to owner read/write permissions where
  POSIX mode bits are meaningful.

Typical configured profile keys:

```toml
# ~/.ti/config
[default]
region_code = "aws-us-east-1"
project_id = "..."

# ~/.ti/credentials
[default]
tidb_cloud_public_key = "..."
tidb_cloud_private_key = "..."
```

`project_id` is written by `ti configure` but is not required to create a
Starter cluster. If it is absent and `--project-id` is not provided, the create
request omits the project label and TiDB Cloud selects the account default.

One profile can access multiple remotely inventoried ti fs resources. The main
config stores neither a default resource nor resource credentials.

New local credentials are keyed by the server-assigned file system ID:

```text
~/.ti/fs_credentials/<profile-key>/<file-system-id-key>/credentials
```

Credential files contain `file_system_id`, canonical `region_code`, and
`api_key`, use mode `0600`, and must never be written to the main
`~/.ti/credentials` file. Profile and ID path segments are safely encoded.
Remote Drive9 list/get is authoritative for inventory and status; local state
only determines `has_local_token` and data-plane access.

`ti fs create-file-system` returns the stored owner credential as `fs_token`;
this is the only ordinary command result that may reveal it. Treat `fs_token`
as a secret and never include it in logs, telemetry, debug output, errors,
mount locators, non-secret config, or test diagnostics.

Legacy flat fields and name-keyed `~/.ti/fs_resources` entries are migration
input only. The first FS command copies complete credentials into the ID-keyed
store without deleting name-keyed source files or old companion homes.
Incomplete or conflicting state fails closed.

DB SQL user credentials live outside the main credentials file:

```text
~/.ti/db_users/<cluster-id>/credentials
```

That file uses role sections:

```toml
[read_only]
username = "prefix.ti_ro"
password = "..."

[read_write]
username = "prefix.ti_rw"
password = "..."

[admin]
username = "prefix.ti_admin"
password = "..."
```

Do not ask users to provide TiDB Cloud API endpoints, filesystem metadata
database URLs, or server URLs. Endpoint selection is an internal resolver
responsibility based on canonical `region_code`. Test-only endpoint
overrides, if added later, must be hidden from ordinary user workflows and must
not be required by MVP usage.

TiDB Cloud control-plane API calls use HTTP Digest auth through
`internal/api/transport`; never send `tidb_cloud_private_key` as Basic Auth for those
APIs. SQL HTTPS API execution and ti fs data-plane auth are separate
authentication schemes. SQL HTTPS API execution uses the prepared DB SQL
username/password as Basic Auth against
`https://http-<cluster-host>/v1beta/sql`; TiDB Cloud API keys must not be used
for SQL execution Basic Auth.

Use `internal/api/endpoints` for Starter, IAM/account, and fs endpoint
selection. Do not add service URLs to user config. The default Starter host is
`https://serverless.tidbapi.com`; the default IAM host is
`https://iam.tidbapi.com`. The ti fs host is resolved from the hosted ti fs
region manifest at
`https://drive9.ai/manifest/regions/drive9-regions.json`, matching the active
profile's cloud provider and region against `tidb_cloud_native` entries. If the
manifest does not contain the profile placement, return a clear unsupported
endpoint error; do not add a user-facing raw server URL flag or config key.
Tests may override the IAM base URL with `TI_TEST_IAM_BASE_URL` and the fs
manifest URL with `TI_TEST_FS_MANIFEST_URL`, only when
`TI_ALLOW_TEST_ENDPOINTS=1`; these are hidden test controls, not supported
user configuration.

Local profile namespace lookup order for authenticated commands:

1. If `--profile <name>` is explicitly provided, use that profile name.
2. If `TI_PROFILE` is set, use that profile name.
3. Otherwise use `default`.

TiDB Cloud API key lookup order:

1. If either `TIDB_CLOUD_PUBLIC_KEY` or `TIDB_CLOUD_PRIVATE_KEY` is set, read the API key pair
   from environment variables. Both are required in this mode.
2. Otherwise read `tidb_cloud_public_key` and `tidb_cloud_private_key` from the
   selected local profile in `~/.ti/credentials`.

Placement lookup order for authenticated commands:

1. If `--region <canonical-region-code>` is explicitly provided, use it for
   this command only.
2. If `TI_REGION_CODE` is set, use it for this command only.
3. Otherwise use the selected profile's `region_code`.

Starter DB cluster creation project lookup order is:

1. Explicit non-empty `--project-id`.
2. The selected profile's `project_id`, discovered by `ti configure` from the
   unique accessible project whose type is `tidbx_virtual`.
3. Otherwise omit the `tidb.cloud/project` label and let the Starter API select
   the account's default project.

An explicitly empty `--project-id` is an error and must not use the profile
or server fallback. When no project ID resolves, omit the label entirely; do
not send `tidb.cloud/project` with an empty value. Other DB commands identify
existing resources by cluster or branch ID and do not send `project_id`.
Drive9-backed ti fs commands do not consume this DB project default.

Environment credentials are a credential source only; they must not change the
local profile namespace and must not cause ti to write local `[env]` sections.
Generated ti fs state is always stored under the selected local profile:
`--profile`, `TI_PROFILE`, or `default`.

ti fs data-plane resource selection order is:

1. Explicit `--file-system-id` or `TI_FS_FILE_SYSTEM_ID`.
2. If an explicit `--fs-token` or `TI_FS_TOKEN` exists, derive the ID from its
   structured token claim and require any separately supplied ID to match.
3. Otherwise fail with `fs.missing_file_system_id` before endpoint resolution,
   companion startup, or a remote request.

Never infer a ti fs resource from profile state, credential-store cardinality,
creation order, or deletion side effects. `TI_FS_FILE_SYSTEM_ID` is an
explicit process-scoped assertion and must not be persisted.

Remote ti fs, fs-git, fs-journal, and owner fs-vault commands use this FS
credential lookup order:

1. Explicit command-local `--fs-token`.
2. `TI_FS_TOKEN`.
3. The selected ID's `api_key` in its ID-keyed credentials file.

Those commands do not require TiDB Cloud public/private keys. A clean machine
can use an existing resource with only a canonical region and FS token; the ID
is derived in memory from the token. Do not
persist ephemeral flag/environment credentials or create a synthetic `[env]`
profile. `ti fs create-file-system`, remote list/describe, and
`ti fs delete-file-system` remain TiDB Cloud-authenticated. Delete requires an
ID but does not require a locally stored owner token.

The ID selector is available on ti fs data-plane/runtime commands and all
`fs-git`, `fs-journal`, and `fs-vault` subcommands. Creation accepts no ID;
description and deletion require an ID. Drain and unmount resolve an existing
mount through its mount path and locator instead of selecting a resource again.

When implementing command handlers, detect whether `--profile` was explicitly
set before calling `config.Load`; the root flag has a default value, but that
default must not suppress `TI_PROFILE`. Also pass the explicit `--region`
value into profile loading so endpoint selection sees the override.

Supported MVP placement values:

| Canonical region code | Cloud provider | Region label |
| --- | --- | --- |
| `aws-us-east-1` | AWS | N. Virginia |
| `aws-us-west-2` | AWS | Oregon |
| `aws-eu-central-1` | AWS | Frankfurt |
| `aws-ap-northeast-1` | AWS | Tokyo |
| `aws-ap-southeast-1` | AWS | Singapore |
| `ali-ap-southeast-1` | Alibaba Cloud | Singapore |

The prefix before the first `-` is the cloud provider selector. `aws` maps to
internal provider `aws`; `ali` maps to internal provider `alibaba_cloud`. Keep
this mapping centralized in `internal/config/region`.

Do not store secrets in logs, telemetry, generated docs examples, or test
fixtures.

Local operation logs are enabled by default and live at
`~/.ti/logs/ti.jsonl`. They are local audit/debug summaries, not telemetry.
`TI_LOGGING=off` disables them for the current process, and global settings
can disable them with:

```toml
# ~/.ti/.preferences
schema_version = 1

[logging]
enabled = false
```

Environment values `off`, `false`, `0`, and `no` disable logging; `on`,
`true`, `1`, and `yes` enable it. The environment variable takes precedence
over settings. Do not add a `ti logging status` command. Invalid settings or
environment values fail closed for logging without failing the requested
command. Legacy `[logging]` in `~/.ti/config` is migrated atomically into
`~/.ti/.preferences`; config and credentials remain profile-only afterward.
Every `ti update` form must bypass settings, migration, profiles, credentials,
operation logs, and all other `~/.ti/` state. The operation log may
record command paths, flag names, profile names, region codes, duration, exit
code, app error code/category, service name, HTTP method/status, operation, and
request id. It must never record flag values, SQL text, SQL results, file
contents, raw request/response bodies, connection strings, local paths, ti fs
raw paths, API keys, DB passwords, or ti fs API keys.

Generated DB SQL usernames and passwords live in
`~/.ti/db_users/<cluster-id>/credentials`, not in the main
`~/.ti/credentials` file. Do not add nested
`[profile.db_users."<cluster-id>".role]` TOML sections to
`~/.ti/credentials`. TiDB Cloud cluster IDs are globally unique, so DB SQL
credentials are cluster-scoped rather than profile-scoped. `ti db
create-db-sql-users` owns those credentials and must be idempotent: it
creates or repairs the stable ti-managed read-only, read-write, and admin
users for a cluster instead of creating a new group every time.

Generated `ti fs` resource API keys live only in the ID-keyed credentials
files under `~/.ti/fs_credentials/`. User-facing docs and commands must call
these `ti fs` API keys or resource credentials, never reference implementation
API keys. Filesystem data-plane
commands route through the installer-managed Drive9 companion binary named
`ti-drive9`. ti owns profile loading, region resolution, credential storage,
preflight errors, output/query handling, and command naming; Drive9 owns the
filesystem runtime semantics for data-plane file operations, FUSE/WebDAV mount,
FUSE mount drain, layer behavior, pack/unpack, Git workflows, journal, and vault.
Do not reintroduce a runtime fallback to ti-native fs behavior. Public fs
service methods must route through the Drive9 companion path unconditionally;
do not add switches such as `UseDrive9Companion` or hidden environment flags
that select old ti HTTP/FUSE/WebDAV implementations.
The companion runs with resource-scoped isolated state under
`~/.ti/drive9-home/<profile-key>/<resource-key>`; do not write or require user
edits to `~/.drive9`. Never use a shared Drive9 `current_context` as the source
of truth for ti resource selection.

Background FS and vault mounts write only a non-secret locator under
`~/.ti/mounts/`. Drain and unmount must route through that locator to the
original resource-scoped companion HOME without requiring the FS token again.
Successful unmount removes the locator; failed unmount preserves it for retry.

Do not implement or expose ti commands for Drive9 internal APIs that are not
part of Drive9's public CLI. In particular, do not reintroduce low-level layer
entry/object/event commands, low-level Git workspace/tree/state/object-pack/
overlay commands, or legacy vault token commands unless Drive9 exposes a
matching public command and the ti command surface is intentionally updated in
README, specs, tests, and AGENTS. Use `TI_DRIVE9_BIN` only as a developer/test
override for a compatible companion; normal installs should rely on the
installer-managed `ti-drive9`.

`ti fs create-directory --mode` is a compatibility flag only in the Drive9
companion path: validate the octal value, but do not emulate directory chmod
with a non-public backend call. `ti fs chmod-file` remains the explicit chmod
command and should follow Drive9 public CLI behavior.

`ti fs-vault mount-vault` requires a delegated vault token from
`ti fs-vault create-grant`; the selected resource's owner API key is used for
`create-secret`, `read-secret`, `list-secrets`, `replace-secret`,
`delete-secret`, grants, audit, and `run-with-secret`, but not for the vault
mount consumption path.

`ti fs drain-file-system` is meaningful only for FUSE mounts where the
companion records a drain control socket. WebDAV mounts flush through normal
file close semantics and should not be expected to support drain.

When invoking a data-plane companion command, resolve exactly one file system ID and build a
sanitized environment: `HOME` from that resource's scoped companion directory,
`DRIVE9_SERVER` from its resolved endpoint, `DRIVE9_REGION_CODE` from its
canonical resource region, `DRIVE9_API_KEY` from its per-resource credentials,
and TiDB Cloud public/private keys only for remote inventory/create/describe/delete flows. Strip
inherited `DRIVE9_*` values so user shell state cannot override ti selection.
Debug and error output must redact TiDB Cloud keys, ti fs API keys, vault tokens, SQL
credentials, file contents, and secret values.

`ti db format-db-connection-string` and `ti db execute-sql-statement` use
read-write credentials by default. `--read-write`, `--read-only`, and `--admin`
must be mutually exclusive explicit selections. Do not add SQL-text
classification or an automatic access mode.

## Output And Errors

Use structured output contracts from the start.

- JSON is the default for successful structured control-plane commands.
- Data-plane commands may stream bytes or plain file listings when JSON would
  break expected filesystem usage.
- `--output json` and `--output text` are the initial output modes.
- `--query` uses JMESPath semantics and is applied after command execution to
  the structured result.
- Raw output commands must reject `--query`.
- Mutating control-plane commands use `internal/dryrun` for shared `--dry-run`
  envelopes, load the active profile, and must stop before remote mutation.
- API/auth errors must preserve categories and exit codes: `3` authentication,
  `4` authorization, and `5` remote not found.
- Errors follow this shape:

```text
ti [ERROR]: <actionable message>
```

Library code returns errors instead of printing or exiting. Only the CLI
boundary writes to stdout/stderr and maps errors to exit codes.

## Telemetry Rules

The product-owned telemetry backend is implemented as the independent
`ti-telemetry-backend` process. The CLI collection and delivery path is
implemented by `internal/telemetry` under
`docs/spec/done/0022-telemetry.md`. Release archives contain a build-configured
product endpoint; local builds and CI default to disabled. `TI_TELEMETRY`
overrides `[telemetry].enabled` in `~/.ti/.preferences`. Help, version,
commandless usage, and all `ti update` forms must be excluded before telemetry
reads preferences or installation state. Allowed fields:

- command and subcommand invoked
- flag names used, never flag values
- error codes and execution time
- TiDB Cloud provider and canonical region
- CLI version, OS, architecture, and install source
- anonymous installation ID and profile source category, never profile name
- an explicit process-scoped `TI_TELEMETRY_TAG` value, bounded to 128 UTF-8 bytes
- an explicit process-scoped `TI_TELEMETRY_EXTRA` JSON value, accepted only when complete, bounded to 2 KiB, depth-limited, and free of prohibited field names

Never collect credentials, tokens, resource IDs, file contents, SQL text, query
output, flag values, raw errors, profile names, local or remote paths, host
identity, command output, or API request/response payloads. Delivery is one
best-effort HTTPS POST with a three-second hard timeout, no foreground retry, no
redirect following, and no local durable queue. Any failure must preserve the
command's stdout, stderr, output format, and exit status.

`TI_TELEMETRY_TAG` and `TI_TELEMETRY_EXTRA` are read only after telemetry is
eligible and enabled, are never persisted under `~/.ti/`, and must never appear
in normal output, errors, debug diagnostics, or operation logs. The CLI emits
schema version 2; the backend must continue to accept schema version 1 without
metadata during rollout.

## Go Style

- Return errors; do not panic in library code.
- Wrap errors with operation context using `%w`.
- Prefer typed string constants for domain enums.
- Constructors should use `New(...)` or `NewWithConfig(cfg Config)`.
- Test helpers accept `*testing.T` as the first argument and call `t.Helper()`.
- Use standard library facilities unless the project already has a chosen
  dependency for the same purpose.
- Keep command handlers thin; put reusable behavior in internal packages.

Imports should be grouped as standard library, third-party, then internal
packages, separated by blank lines.

## Testing Expectations

For new behavior, add focused tests at the package boundary that owns the
contract.

Current expectations:

- `make test` must pass without live cloud credentials.
- `make e2e` must pass and should exercise the compiled binary, not internal Go
  packages directly.
- Unit tests should use temp home directories for config and credentials.
- E2E tests should use temp `HOME` values and must not touch the user's real
  `~/.ti/`.
- Unit/e2e helpers should set `TI_LOGGING=off` by default unless the test is
  explicitly verifying operation logging.
- API client tests should use mock HTTP servers once API specs are implemented.
- Live cloud tests are opt-in, skipped by default, and run through
  the focused `make live-e2e-<family>` targets or the aggregate
  `make live-e2e`. They must use the `live-e2e` profile and verify the real
  API/command surface for every implemented spec. Implemented mutating commands
  must have real live mutation coverage with resource IDs captured from create
  responses and cleanup that only targets resources created by that run.

Do not require live cloud credentials for ordinary `go test ./...`.

## Documentation Workflow

Pending requirements live in `docs/spec/` and are numbered by dependency order,
for example `0003-output-error-query-dry-run.md`. When a requirement is fully
implemented and verified, move its file to `docs/spec/done/` and mention the
verification evidence in the final response.

README.md is the user-facing source for current usage. After every code change,
check whether README.md still matches the implemented CLI. Update README.md in
the same change whenever commands, flags, config files, environment variables,
build/test commands, error behavior, outputs, or implemented/not-implemented
status changes. Do not leave code and README.md out of sync.

Keep each spec decision-complete for implementation: commands, behavior, inputs,
outputs, dependencies, acceptance criteria, and explicit out-of-scope notes.
