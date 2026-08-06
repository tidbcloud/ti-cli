---
title: AGENTS.md - tdc development guide for AI coding agents
---

# Repository Overview

tdc is a Go command-line product for TiDB Cloud Starter. It is designed to be
agent-friendly, predictable, scriptable, and safe for automation.

Module: `github.com/tidbcloud/tdc`
Go version: 1.26.5 (see `go.mod`)

The most important product document is `docs/priciples.md`. Treat that file as
the source of truth for product principles. Requirement specs live in
`docs/spec/`; completed specs are moved to `docs/spec/done/`.

## Current Implementation Status

Implemented:

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
- tdc fs Unix-style command aliases from
  `docs/spec/done/0014-tdc-fs-unix-command-aliases.md`
- tdc fs control plane from
  `docs/spec/done/0009-tdc-fs-control-plane.md`
- tdc fs data plane from
  `docs/spec/done/0010-tdc-fs-data-plane.md`
- tdc fs mount runtime from
  `docs/spec/done/0011-tdc-fs-mount-runtime.md`
- tdc fs FUSE correctness and Drive9 parity extension from
  `docs/spec/done/0011-ext01-fuse-cache-and-open-handle-correctness.md`
- profile-scoped 1:N tdc fs resource registry from
  `docs/spec/done/0016-profile-fs-resource-registry.md`
- explicit tdc fs resource selection from
  `docs/spec/done/0020-explicit-file-system-selection.md`
- FS token authentication and configuration-free access from
  `docs/spec/done/0018-fs-token-auth-and-config-free-access.md`
- install and update distribution from
  `docs/spec/done/0012-install-and-update-distribution.md`
- English PingCAP Preview documentation from
  `docs/spec/done/0019-pingcap-tdc-documentation.md`
- `tdc configure`
- `tdc update --check`
- `tdc update`
- `tdc organization list-projects`
- `tdc db create-db-cluster`
- `tdc db list-db-clusters`
- `tdc db describe-db-cluster`
- `tdc db update-db-cluster`
- `tdc db delete-db-cluster`
- `tdc db create-db-cluster-branch`
- `tdc db list-db-cluster-branches`
- `tdc db describe-db-cluster-branch`
- `tdc db delete-db-cluster-branch`
- `tdc db create-db-sql-users`
- `tdc db format-db-connection-string`
- `tdc db execute-sql-statement`
- `tdc fs create-file-system`
- `tdc fs delete-file-system`
- `tdc fs list-file-systems`
- `tdc fs describe-file-system`
- `tdc fs check-file-system`
- `tdc fs copy-file`
- `tdc fs read-file`
- `tdc fs list-files`
- `tdc fs describe-file`
- `tdc fs move-file`
- `tdc fs delete-file`
- `tdc fs create-directory`
- `tdc fs chmod-file`
- `tdc fs create-symlink`
- `tdc fs create-hardlink`
- `tdc fs search-file-content`
- `tdc fs find-files`
- `tdc fs create-layer`
- `tdc fs list-layers`
- `tdc fs describe-layer`
- `tdc fs diff-layer`
- `tdc fs create-layer-checkpoint`
- `tdc fs rollback-layer`
- `tdc fs commit-layer`
- `tdc fs mount-file-system`
- `tdc fs drain-file-system`
- `tdc fs unmount-file-system`
- Unix-style `tdc fs` command aliases: `cp`, `cat`, `ls`, `stat`, `mv`, `rm`,
  `mkdir`, `chmod`, `symlink`, `hardlink`, `grep`, `find`, `mount`, `drain`,
  and `umount`
- `tdc fs-vault create-secret`
- `tdc fs-vault replace-secret`
- `tdc fs-vault read-secret`
- `tdc fs-vault list-secrets`
- `tdc fs-vault delete-secret`
- `tdc fs-vault create-grant`
- `tdc fs-vault delete-grant`
- `tdc fs-vault list-audit-events`
- `tdc fs-vault run-with-secret`
- `tdc fs-vault mount-vault`
- `tdc fs-vault unmount-vault`
- `tdc fs-journal create-journal`
- `tdc fs-journal append-journal-entries`
- `tdc fs-journal read-journal-entries`
- `tdc fs-journal search-journal-entries`
- `tdc fs-journal verify-journal`
- help and version behavior at every command level
- structured JSON/text rendering and JMESPath `--query`
- `--dry-run` on mutating control-plane commands
- TiDB Cloud Digest-auth API client foundation and auth/authz error mapping
- profile-scoped 1:N tdc fs resource registry with per-resource credentials
- tdc fs/fs-git/fs-journal/fs-vault commands routed through the bundled
  `tdc-drive9` companion, with tdc-owned profile loading, credential storage,
  region resolution, and output/error handling
- Drive9 public CLI coverage for tdc fs data-plane operations, FUSE/WebDAV
  mount, mount drain, layers, pack/unpack, vault, journal, and Git clone,
  hydrate, add-worktree, and remove-worktree workflows
- GoReleaser/GitHub Releases install and update workflow
- Makefile build/test/e2e workflow
- independent telemetry ingestion backend with strict schema validation,
  bounded in-memory batching, TiDB storage, and personless PostHog forwarding

There are no registered placeholder commands at the current stage. Implemented
mutating commands support `--dry-run` where their command contract declares
dry-run support.

## Reference Code

- `ref/tidbcloud-cli/` is the previous TiDB Cloud CLI implementation. Use it as
  a reference for TiDB Cloud concepts, profile handling, output helpers,
  telemetry, and API client patterns.
- `ref/drive9/` is the filesystem reference implementation. Use it as context
  for filesystem commands, mount behavior, and data-plane semantics. In tdc
  user-facing output, this domain is always called `tdc fs`.
- `ref/serverless-js/` is a reference for the HTTPS SQL API call shape.

Reference directories are not product source for tdc. They exist only to give
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

`make build` writes the binary to `bin/tdc`.
`make build-telemetry-backend` writes the independent ingestion service to
`bin/tdc-telemetry-backend`.
`make build-telemetry-migrator` writes the one-shot Goose migration runner to
`bin/tdc-telemetry-migrate`.

`make test` runs ordinary Go tests and must not require live cloud credentials.
`make e2e` builds `bin/tdc` and runs black-box tests against the real binary via
`TDC_E2E_BIN`.
`make telemetry-e2e` is separately opt-in. It loads the ignored
`e2e/.env.telemetry` file, requires a test-only `TDC_TEST_TELEMETRY_TIDB_DSN`
whose user can create and drop databases, and creates a unique temporary TiDB
database. It verifies Goose initialization from empty state, an additive
upgrade preserving a legacy event, and the real local CLI-to-backend-to-TiDB
delivery path before dropping only that temporary database. It must not run as
part of `make test`, `make e2e`, or any live-e2e target.
The `make live-e2e-<family>` targets build `bin/tdc` and run only the selected
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
uniquely named `tdc-e2e-*` cluster with `--wait`, without a
spending limit or explicit/configured project ID, verifies the returned state
is `ACTIVE` and has a non-empty server-selected project label, and deletes only
that cluster. The server-selected account default is not required to equal the
`tidbx_virtual` project discovered by `tdc configure`. For Starter DB branches,
the live suite creates, reads, lists, and deletes only a `tdc-e2e-branch-*`
branch on the cluster created by the same test run. Branch creation must use
`--wait`; cluster deletion must use `--wait`. For Starter DB SQL access, the live suite prepares tdc-managed
read-only, read-write, and admin SQL users on the temporary cluster, verifies
connection string output, and executes the HTTPS SQL API with all three access
modes.
For tdc fs data-plane and mount runtime, the live suite creates uniquely named
remote paths, exercises real file create/read/list/copy/move/delete flows,
range reads, append, resume, recursive local/remote copy, stdin/stdout copy,
tags/descriptions, chmod, symlink, hardlink, pack/unpack, real public layer
create/list/describe/diff/checkpoint/rollback/commit flows, layer-aware
copy/find where Drive9 exposes it, vault create/read/replace/delete, delegated
vault grant reads, vault mount read on macOS/Linux hosts when available,
journal create/append/read/search/verify, public Git clone/hydrate/worktree
flows, mount and drain through the companion runtime, and explicit WebDAV
fallback when the platform supports it.
If the live profile has no registry resource named by `TDC_LIVE_FS_NAME` or
`workspace`, the suite creates that temporary tdc fs resource, stores its
metadata and API key in the profile-scoped resource registry, and deletes only
that auto-created resource before the DB lifecycle needs the Starter slot, or
when the test process exits if execution stops earlier.
The live suite also attempts a separate 1:N registry lifecycle with two unique
`tdc-e2e-fs-*` resources, covering create, list, default selection, explicit
selection, isolated deletion, and cleanup. If the second resource is rejected
specifically because Starter quota is full, complete the single-resource live
flow and rely on `make e2e` for fake-companion multi-resource routing coverage.
Never delete a pre-existing resource to make room for this test.
When a service command is implemented, add its real live verification to
`make live-e2e`; do not leave the target at profile, smoke-test-only, or
mock-only coverage.

For focused work, direct Go commands are also fine:

```bash
go test ./...
go test ./internal/config -run TestName
go build ./cmd/tdc
```

Build and release artifacts are ignored through `.gitignore`. Do not commit
binaries or GoReleaser `dist/` output.

Formatting should be standard Go formatting via `gofmt`. Do not run formatters
that rewrite unrelated files.

## Project Layout

Current layout:

```text
cmd/tdc/                    CLI entrypoint
cmd/tdc-telemetry-backend/  independent telemetry ingestion service entrypoint
cmd/tdc-telemetry-migrate/  one-shot embedded Goose migration runner
internal/api/               shared HTTP API client and service clients
internal/api/endpoints/     provider/region endpoint resolver
internal/api/transport/     Digest/Bearer/debug HTTP transports
internal/apperr/            typed CLI errors and exit-code helpers
internal/auth/              authenticated profile validation and transports
internal/authz/             permission constants and permission errors
internal/cli/               command wiring
internal/config/            profile loading and precedence rules
internal/config/configure/  interactive configure wizard
internal/config/fsresource/ legacy flat tdc fs migration key names
internal/config/region/     provider and region validation
internal/config/store/      TOML read/write, file modes, atomic writes
internal/db/                Starter DB cluster, branch, and SQL use cases
internal/db/connectionstring/ DB connection string formatters
internal/db/sqlaccess/      DB SQL user preparation logic
internal/db/sqlcred/        cluster-scoped DB SQL credential store
internal/db/sqlhttp/        HTTPS SQL API transport
internal/db/sqlmysql/       explicit MySQL fallback transport
internal/db/sqlresult/      SQL result model and decoding
internal/db/sqlsingle/      one-statement validation
internal/db/validate/       DB flag and request validation helpers
internal/dryrun/            shared dry-run result envelope
internal/fs/                tdc fs control-plane, data-plane, and mount use cases
internal/fs/fscred/         profile-scoped tdc fs registry, selection, and migration
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
ref/                        read-only reference implementations
```

Keep one package per directory. Package names should be short, lowercase, and
without underscores.

## CLI Product Rules

Follow these rules unless `docs/priciples.md` is updated:

- The command tree is at most two levels: `tdc <command> [subcommand]`.
- `tdc configure` and `tdc update` are the only intentional top-level verb
  exceptions. `tdc configure` is the only interactive command.
- Other top-level commands are nouns such as `db`, `fs`, and `organization`.
- Use long flags only, for example `--profile` and `--db-cluster-name`.
- Do not add short flags or one-letter aliases. The current CLI rejects short
  flags before invoking Cobra.
- `tdc fs` Unix-style aliases are command-name aliases only. They must keep the
  same long flags, output modes, auth, permissions, dry-run behavior, and command
  handlers as their canonical commands.
- Do not prompt for input except inside `tdc configure`.
- Successful structured control-plane commands output JSON by default.
- Implement DB, organization, and fs control-plane commands through
  `controlPlaneCommandSpec` in `internal/cli`, so normal execution, dry-run,
  output rendering, and query handling stay on the shared path.
- Each control-plane command must declare exactly one `authz.Permission` in its
  command spec. Do not infer permissions from command names or SQL text.
- Mutating control-plane commands support `--dry-run`.
- `--dry-run` must validate local config, credentials, provider, and region
  before reporting a planned mutation.
- `tdc db create-db-cluster --wait` waits up to 12 minutes for the
  created cluster to reach `ACTIVE`. It must never delete the cluster on
  timeout, cancellation, a polling failure, or a terminal state; errors must
  retain the created cluster ID and provide an inspection command.
- `tdc db create-db-cluster-branch --wait` waits up to 5 minutes
  for the created branch to reach `ACTIVE`. A failed wait must not delete or
  recreate the accepted branch.
- `tdc db delete-db-cluster --wait` waits up to 12 minutes and
  succeeds when the API reports `DELETED` or the deleted cluster is no longer
  accessible. A failed wait must state that deletion may still be in progress.
- `tdc fs create-file-system --wait` waits up to 10 minutes for the
  root to become readable through the public Drive9 CLI. It must retain the
  resource and local credentials when waiting fails.
- `tdc fs delete-file-system` is asynchronous. After Drive9 accepts deletion,
  output status is `deleting`, not `deleted`.
- Read-only commands reject `--dry-run`.
- Apply `--query` after command execution and before rendering.
- Users provide cloud placement as one canonical `region_code`, never as
  separate provider/region fields or server URLs.
- Every `tdc db` command must enforce the Starter-only product boundary from
  API resource metadata. Treat `servicePlan` as canonical and `clusterPlan` as
  a legacy fallback. Reject non-Starter, missing, or conflicting plans before
  any cluster, branch, IAM SQL-user, local SQL-credential, or SQL mutation.
- `tdc db list-db-clusters` adds an immutable API filter for the effective
  provider and region, then defensively filters each API page to verified
  Starter clusters in that same region. It preserves the API next-page token
  and omits the API total size because that total can include resources outside
  tdc's verified result. Missing or conflicting region metadata is excluded.
- The global `--region <canonical-region-code>` flag overrides placement for
  the current command only. It has higher priority than `TDC_REGION_CODE` and
  profile `region_code`, but it must not change the selected profile or
  credential source.
- Every command should be usable by scripts and agents without
  terminal-specific assumptions.
- When creating a pull request, check whether the target repository has a pull
  request template. If it does, follow that template when writing the pull
  request description.
- Help must work as:
  - `tdc help`
  - `tdc <command> help`
  - `tdc <command> <subcommand> help`
- In generated Flags and Global Flags sections, render value types as
  `<type>` and append `(required)` to flags marked with
  `tdc_usage_required`.
- Keep the global `--version` behavior intact at every command level. Do not
  add command-specific `--version <value>` flags; use names such as
  `--target-version` when a command needs a version input.
- `tdc update --check` and `tdc update` use GitHub Releases metadata and
  must not read or mutate `~/.tdc/`.
- `tdc update` may replace only tdc-owned archive/script installs. It must
  refuse local, unknown, Homebrew, Scoop, Winget, or other package-manager
  installs with actionable guidance.
- `tdc update` is itself explicit update intent and must not require `--yes`.
  It downloads, extracts, verifies, stages, and replaces artifacts as the
  current user and must never invoke sudo or another privilege escalation
  mechanism.
- Installer scripts default to the stable user-owned `~/.tdc/bin` directory on
  macOS, Linux, and Windows unless `--install-dir`/`-InstallDir` or
  `TDC_INSTALL_DIR` overrides it. They must not prefer or overwrite an active
  system-level tdc found on PATH, invoke sudo, create system-directory
  symlinks, or modify shell profile files automatically.
- Installer scripts must detect PATH shadowing, bootstrap `~/.tdc/config` only
  when missing, print the exact command that prepends `~/.tdc/bin` to PATH,
  print DB and tdc fs region lists, and show clear next steps. They must never
  write `~/.tdc/credentials`.

## Commands

Implemented command behavior:

- `tdc` without a command returns `cli.missing_command` with exit code `2` and
  an AWS-style compact two-level usage synopsis on stderr
- `tdc configure`
- `tdc configure --non-interactive`
- `tdc help`
- `tdc --version`
- `tdc <command> help`
- `tdc <command> <subcommand> help`
- `tdc <command> --version`
- `tdc <command> <subcommand> --version`
- `tdc update --check`
- `tdc update --check --fail-if-update-available`
- `tdc update --dry-run`
- `tdc update`
- `tdc update --target-version v0.1.1`
- `tdc organization list-projects`
- `tdc organization list-projects --query 'projects[0].id'`
- `tdc organization list-projects --output text`
- `tdc db create-db-cluster --db-cluster-name demo`
- `tdc db create-db-cluster --db-cluster-name demo --wait`
- `tdc db create-db-cluster --db-cluster-name demo --dry-run`
- `tdc db create-db-cluster --db-cluster-name demo --project-id <project-id>`
- `tdc db list-db-clusters`
- `tdc db list-db-clusters --query 'clusters[].id'`
- `tdc db describe-db-cluster --db-cluster-id <cluster-id>`
- `tdc db update-db-cluster --db-cluster-id <cluster-id> --db-cluster-name new-name`
- `tdc db update-db-cluster --db-cluster-id <cluster-id> --monthly-spending-limit-usd-cents 1000 --dry-run`
- `tdc db delete-db-cluster --db-cluster-id <cluster-id>`
- `tdc db delete-db-cluster --db-cluster-id <cluster-id> --wait`
- `tdc db delete-db-cluster --db-cluster-id <cluster-id> --dry-run`
- `tdc db create-db-cluster-branch --db-cluster-id <cluster-id> --db-cluster-branch-name dev`
- `tdc db create-db-cluster-branch --db-cluster-id <cluster-id> --db-cluster-branch-name dev --wait`
- `tdc db create-db-cluster-branch --db-cluster-id <cluster-id> --db-cluster-branch-name dev --dry-run`
- `tdc db list-db-cluster-branches --db-cluster-id <cluster-id>`
- `tdc db list-db-cluster-branches --db-cluster-id <cluster-id> --query 'branches[].id'`
- `tdc db list-db-cluster-branches --db-cluster-id <cluster-id> --output text`
- `tdc db describe-db-cluster-branch --db-cluster-id <cluster-id> --db-cluster-branch-id <branch-id>`
- `tdc db delete-db-cluster-branch --db-cluster-id <cluster-id> --db-cluster-branch-id <branch-id>`
- `tdc db delete-db-cluster-branch --db-cluster-id <cluster-id> --db-cluster-branch-id <branch-id> --dry-run`
- `tdc db create-db-sql-users --db-cluster-id <cluster-id>`
- `tdc db create-db-sql-users --db-cluster-id <cluster-id> --dry-run`
- `tdc db format-db-connection-string --db-cluster-id <cluster-id>`
- `tdc db format-db-connection-string --db-cluster-id <cluster-id> --read-write --format mysql-uri`
- `tdc db format-db-connection-string --db-cluster-id <cluster-id> --read-only --format env`
- `tdc db format-db-connection-string --db-cluster-id <cluster-id> --admin --format jdbc`
- `tdc db execute-sql-statement --db-cluster-id <cluster-id> --sql "select 1"`
- `tdc db execute-sql-statement --db-cluster-id <cluster-id> --read-write --sql "select 1"`
- `tdc db execute-sql-statement --db-cluster-id <cluster-id> --read-only --sql "select 1"`
- `tdc db execute-sql-statement --db-cluster-id <cluster-id> --admin --sql "select 1"`
- `tdc db execute-sql-statement --db-cluster-id <cluster-id> --transport https --sql "select 1"`
- `tdc db execute-sql-statement --db-cluster-id <cluster-id> --transport mysql --sql "select 1"`
- `tdc fs create-file-system --file-system-name workspace`
- `tdc fs create-file-system --file-system-name workspace --wait`
- `tdc fs create-file-system --file-system-name workspace --dry-run`
- `tdc fs create-file-system --file-system-name scratch`
- `tdc fs delete-file-system --file-system-name workspace`
- `tdc fs delete-file-system --file-system-name workspace --dry-run`
- `tdc fs list-file-systems`
- `tdc fs describe-file-system --file-system-name workspace`
- `tdc fs check-file-system`
- `tdc fs check-file-system --file-system-name workspace`
- `tdc fs copy-file --from-local ./README.md --to-remote /workspace/README.md`
- `tdc fs copy-file --from-remote /workspace/README.md --to-local ./README.copy.md --create-parents`
- `tdc fs copy-file --from-remote /workspace/README.md --to-remote /workspace/README.copy.md`
- `tdc fs read-file --path /workspace/README.md`
- `tdc fs read-file --path /workspace/README.md --offset 0 --length 1024`
- `tdc fs copy-file --from-local ./tail.log --to-remote /workspace/app.log --append`
- `tdc fs copy-file --from-remote /workspace/large.bin --to-local ./large.bin --resume`
- `tdc fs copy-file --from-local ./large.bin --to-remote /workspace/large.bin --resume`
- `tdc fs copy-file --from-local ./src-dir --to-remote /workspace/src-dir --recursive`
- `tdc fs copy-file --from-remote /workspace/src-dir --to-local ./src-dir.copy --recursive`
- `tdc fs copy-file --from-remote /workspace/src-dir --to-remote /workspace/src-dir.copy --recursive`
- `tdc fs copy-file --from-stdin --to-remote /workspace/stdin.txt --tag source=stdin --description "stdin upload"`
- `tdc fs copy-file --from-remote /workspace/stdin.txt --to-stdout`
- `tdc fs list-files --path /workspace`
- `tdc fs list-files --path /workspace --output text`
- `tdc fs describe-file --path /workspace/README.md`
- `tdc fs move-file --from-remote /workspace/README.copy.md --to-remote /workspace/archive/README.md`
- `tdc fs delete-file --path /workspace/archive/README.md`
- `tdc fs delete-file --path /workspace --recursive`
- `tdc fs create-directory --path /workspace/archive --mode 0755`
- `tdc fs chmod-file --path /workspace/README.md --mode 0600`
- `tdc fs create-symlink --target README.md --link-path /workspace/README.link`
- `tdc fs create-hardlink --source-path /workspace/README.md --link-path /workspace/README.hard`
- `tdc fs search-file-content --path /workspace --pattern "hello"`
- `tdc fs search-file-content --path /workspace --pattern "hello" --layer-id layer-1`
- `tdc fs find-files --path /workspace --file-name-pattern "*.md"`
- `tdc fs find-files --path /workspace --file-name-pattern "*.md" --layer-id layer-1`
- `tdc fs create-layer --layer-id layer-1 --base-root-path /workspace --layer-name task --durability-mode restore-safe --tag task=auth`
- `tdc fs list-layers`
- `tdc fs list-layers --output text`
- `tdc fs describe-layer --layer-id layer-1`
- `tdc fs diff-layer --layer-id layer-1`
- `tdc fs copy-file --from-local ./README.md --to-remote /workspace/layered.md --layer-id layer-1`
- `tdc fs create-layer-checkpoint --layer-id layer-1 --checkpoint-id cp-1 --label before-commit`
- `tdc fs rollback-layer --layer-id layer-1`
- `tdc fs commit-layer --layer-id layer-1`
- `tdc fs pack-file-system --local-root ~/.tdc/local/fs/demo --remote-root /workspace --mount-profile portable`
- `tdc fs pack-file-system --mount-path ./workspace`
- `tdc fs unpack-file-system --local-root ~/.tdc/local/fs/demo --remote-root /workspace --mount-profile portable`
- `tdc fs mount-file-system --file-system-name workspace --mount-path ./workspace`
- `tdc fs mount-file-system --file-system-name workspace --mount-path ./workspace --driver fuse`
- `tdc fs mount-file-system --file-system-name workspace --mount-path ./workspace --driver webdav`
- `tdc fs mount-file-system --file-system-name workspace --mount-path ./workspace --mount-profile coding-agent`
- `tdc fs mount-file-system --file-system-name workspace --mount-path ./workspace --mount-profile portable --pack-path /`
- `tdc fs mount-file-system --file-system-name workspace --mount-path ./workspace --driver fuse --read-cache-size-mb 256 --read-cache-max-file-mb 16`
- `tdc fs mount-file-system --file-system-name workspace --mount-path ./workspace --driver fuse --cache-dir ~/.tdc/cache/workspace --write-back-cache=false`
- `tdc fs drain-file-system --mount-path ./workspace`
- `tdc fs drain-file-system --mount-path ./workspace --timeout 30s`
- `tdc fs unmount-file-system --mount-path ./workspace`
- `tdc fs unmount-file-system --mount-path ./workspace --ignore-absent`
- `tdc fs-vault create-secret --secret-name db-prod --field DB_URL=mysql://example --field PASSWORD=@./password.txt`
- `tdc fs-vault replace-secret --secret-path /n/vault/db-prod --from-directory ./secret-fields`
- `tdc fs-vault read-secret --secret-name db-prod`
- `tdc fs-vault read-secret --secret-name db-prod --field PASSWORD --format raw`
- `tdc fs-vault read-secret --secret-name db-prod --field DB_URL --format env`
- `tdc fs-vault list-secrets`
- `tdc fs-vault delete-secret --secret-name db-prod`
- `tdc fs-vault create-grant --agent-id deploy-agent --scope db-prod/DB_URL --permission read --ttl 10m`
- `tdc fs-vault delete-grant --grant-id <grant-id> --reason rotated`
- `tdc fs-vault list-audit-events --secret-name db-prod --limit 20`
- `tdc fs-vault run-with-secret --secret-path /n/vault/db-prod -- env`
- `tdc fs-vault mount-vault --mount-path ./vault --vault-token "$TDC_VAULT_TOKEN"`
- `tdc fs-vault unmount-vault --mount-path ./vault`
- `tdc fs-journal create-journal --journal-id jrn-demo --journal-kind agent --title "demo task" --actor agent:tdc --label env=dev`
- `tdc fs-journal append-journal-entries --journal-id jrn-demo --entry-json '{"type":"task.started"}'`
- `tdc fs-journal read-journal-entries --journal-id jrn-demo --after-seq 0 --limit 100`
- `tdc fs-journal search-journal-entries --entry-type task.started --label env=dev --include-entries`
- `tdc fs-journal verify-journal --journal-id jrn-demo --output text`
- `tdc fs-git clone-git-workspace --repo-url https://github.com/pingcap/tidb.git --target-path ./workspace/tidb`
- `tdc fs-git clone-git-workspace --repo-url https://github.com/pingcap/tidb.git --target-path ./workspace/tidb --blobless --hydrate sync`
- `tdc fs-git hydrate-git-workspace --target-path ./workspace/tidb --timeout 30m`
- `tdc fs-git add-git-worktree --base-path ./workspace/tidb --worktree-path ./workspace/tidb-feature --branch-name feature-x`
- `tdc fs-git remove-git-worktree --worktree-path ./workspace/tidb-feature --force`

Registered command surface:

- `tdc update --check`
- `tdc update`
- `tdc organization list-projects`
- `tdc db create-db-cluster`
- `tdc db list-db-clusters`
- `tdc db describe-db-cluster`
- `tdc db update-db-cluster`
- `tdc db delete-db-cluster`
- `tdc db create-db-cluster-branch`
- `tdc db list-db-cluster-branches`
- `tdc db describe-db-cluster-branch`
- `tdc db delete-db-cluster-branch`
- `tdc db create-db-sql-users`
- `tdc db format-db-connection-string`
- `tdc db execute-sql-statement`
- `tdc fs create-file-system`
- `tdc fs delete-file-system`
- `tdc fs list-file-systems`
- `tdc fs describe-file-system`
- `tdc fs check-file-system`
- `tdc fs copy-file`
- `tdc fs read-file`
- `tdc fs list-files`
- `tdc fs describe-file`
- `tdc fs move-file`
- `tdc fs delete-file`
- `tdc fs create-directory`
- `tdc fs chmod-file`
- `tdc fs create-symlink`
- `tdc fs create-hardlink`
- `tdc fs search-file-content`
- `tdc fs find-files`
- `tdc fs create-layer`
- `tdc fs list-layers`
- `tdc fs describe-layer`
- `tdc fs diff-layer`
- `tdc fs create-layer-checkpoint`
- `tdc fs rollback-layer`
- `tdc fs commit-layer`
- `tdc fs pack-file-system`
- `tdc fs unpack-file-system`
- `tdc fs mount-file-system`
- `tdc fs drain-file-system`
- `tdc fs unmount-file-system`
- `tdc fs cp` aliases `tdc fs copy-file`
- `tdc fs cat` aliases `tdc fs read-file`
- `tdc fs ls` aliases `tdc fs list-files`
- `tdc fs stat` aliases `tdc fs describe-file`
- `tdc fs mv` aliases `tdc fs move-file`
- `tdc fs rm` aliases `tdc fs delete-file`
- `tdc fs mkdir` aliases `tdc fs create-directory`
- `tdc fs chmod` aliases `tdc fs chmod-file`
- `tdc fs symlink` aliases `tdc fs create-symlink`
- `tdc fs hardlink` aliases `tdc fs create-hardlink`
- `tdc fs grep` aliases `tdc fs search-file-content`
- `tdc fs find` aliases `tdc fs find-files`
- `tdc fs mount` aliases `tdc fs mount-file-system`
- `tdc fs drain` aliases `tdc fs drain-file-system`
- `tdc fs umount` aliases `tdc fs unmount-file-system`
- `tdc fs-vault create-secret`
- `tdc fs-vault replace-secret`
- `tdc fs-vault read-secret`
- `tdc fs-vault list-secrets`
- `tdc fs-vault delete-secret`
- `tdc fs-vault create-grant`
- `tdc fs-vault delete-grant`
- `tdc fs-vault list-audit-events`
- `tdc fs-vault run-with-secret`
- `tdc fs-vault mount-vault`
- `tdc fs-vault unmount-vault`
- `tdc fs-journal create-journal`
- `tdc fs-journal append-journal-entries`
- `tdc fs-journal read-journal-entries`
- `tdc fs-journal search-journal-entries`
- `tdc fs-journal verify-journal`
- `tdc fs-git clone-git-workspace`
- `tdc fs-git hydrate-git-workspace`
- `tdc fs-git add-git-worktree`
- `tdc fs-git remove-git-worktree`

Do not rename commands without updating specs, README, e2e tests, and AGENTS.
Any code change that changes user-visible behavior must keep README.md in sync.
It must also update the matching English tdc pages under
`docs/pingcap-docs/docs/` when the published behavior changes. Use globally
unique `tdc-` basenames, preserve the standard Preview note on every page, and
update `TOC-ai.md` and `ai/_index.md` when adding or removing a document.
Validate command names and flags against the compiled CLI help, not historical
specs or demos.

## Configuration And Credentials

All tdc local state belongs under `~/.tdc/`.

- `~/.tdc/config` stores profile-scoped non-sensitive TOML values.
- `~/.tdc/credentials` stores profile-scoped sensitive TOML values.
- Both files use profile sections such as `[default]` and `[stage]`.
- `~/.tdc/.preferences` is optional hidden global TOML configuration and is
  never selected by profile. Fresh installs and `tdc configure` do not create
  it. Do not create or migrate the unshipped intermediate `~/.tdc/settings`
  path.
- `~/.tdc/.telemetry-installation-id` is a machine-generated pseudonymous
  telemetry identity, not TOML configuration. It is created lazily with mode
  `0600` only for an eligible, effectively enabled invocation with a
  build-configured endpoint. Users can delete it to reset the identity.
- The profile name `logging` is reserved so legacy global logging configuration
  cannot be confused with a profile.
- The default profile name is `default`.
- The global `--profile` flag selects a profile when explicitly provided.
- The global `--region` flag selects command-scope placement when explicitly
  provided and must reject an explicit empty value.
- `tdc configure` writes canonical `region_code`, discovers the unique
  `tidbx_virtual` project as `project_id`, and writes `tdc_public_key` and
  `tdc_private_key`.
- `tdc configure --non-interactive` must not prompt. It reads values from flags
  first, then `TDC_REGION_CODE`, `TDC_PUBLIC_KEY`, and `TDC_PRIVATE_KEY`.
  Missing values fail with an actionable error.
- For CI/CD, prefer environment variables for private keys over command-line
  secret flags.
- Interactive `tdc configure` must respond to Ctrl+C and surface an
  `interrupted` error with exit code 130.
- The credentials file is restricted to owner read/write permissions where
  POSIX mode bits are meaningful.

Typical configured profile keys:

```toml
# ~/.tdc/config
[default]
region_code = "aws-us-east-1"
project_id = "..."

# ~/.tdc/credentials
[default]
tdc_public_key = "..."
tdc_private_key = "..."
```

`project_id` is written by `tdc configure` but is not required to create a
Starter cluster. If it is absent and `--project-id` is not provided, the create
request omits the project label and TiDB Cloud selects the account default.

One profile can own multiple tdc fs resources. The main config stores neither a
default resource name nor resource credentials.

Each resource stores metadata and credentials separately:

```text
~/.tdc/fs_resources/<profile-key>/<resource-key>/config
~/.tdc/fs_resources/<profile-key>/<resource-key>/credentials
```

Resource config files contain `file_system_name`, `tenant_id`,
`cloud_provider`, `region_code`, and `created_at`. Resource credentials files
contain only `api_key`, use mode `0600`, and must never be written to the main
`~/.tdc/credentials` file. Profile and resource path segments are safely
encoded; always use the stored `file_system_name` for user-facing output.

`tdc fs create-file-system` returns the stored owner credential as `fs_token`;
this is the only ordinary command result that may reveal it. Treat `fs_token`
as a secret and never include it in logs, telemetry, debug output, errors,
mount locators, non-secret config, or test diagnostics.

Legacy flat `fs_resource_name`, `fs_tenant_id`, `fs_cloud_provider`,
`fs_region_code`, and `fs_api_key` fields are migration input only. The first fs
command migrates a complete legacy resource into the registry and clears the
flat fields. Incomplete legacy state fails with
`fs.resource_credentials_incomplete`.

DB SQL user credentials live outside the main credentials file:

```text
~/.tdc/db_users/<cluster-id>/credentials
```

That file uses role sections:

```toml
[read_only]
username = "prefix.tdc_ro"
password = "..."

[read_write]
username = "prefix.tdc_rw"
password = "..."

[admin]
username = "prefix.tdc_admin"
password = "..."
```

Do not ask users to provide TiDB Cloud API endpoints, filesystem metadata
database URLs, or server URLs. Endpoint selection is an internal resolver
responsibility based on canonical `region_code`. Test-only endpoint
overrides, if added later, must be hidden from ordinary user workflows and must
not be required by MVP usage.

TiDB Cloud control-plane API calls use HTTP Digest auth through
`internal/api/transport`; never send `tdc_private_key` as Basic Auth for those
APIs. SQL HTTPS API execution and tdc fs data-plane auth are separate
authentication schemes. SQL HTTPS API execution uses the prepared DB SQL
username/password as Basic Auth against
`https://http-<cluster-host>/v1beta/sql`; TiDB Cloud API keys must not be used
for SQL execution Basic Auth.

Use `internal/api/endpoints` for Starter, IAM/account, and fs endpoint
selection. Do not add service URLs to user config. The default Starter host is
`https://serverless.tidbapi.com`; the default IAM host is
`https://iam.tidbapi.com`. The tdc fs host is resolved from the hosted tdc fs
region manifest at
`https://drive9.ai/manifest/regions/drive9-regions.json`, matching the active
profile's cloud provider and region against `tidb_cloud_native` entries. If the
manifest does not contain the profile placement, return a clear unsupported
endpoint error; do not add a user-facing raw server URL flag or config key.
Tests may override the IAM base URL with `TDC_TEST_IAM_BASE_URL` and the fs
manifest URL with `TDC_TEST_FS_MANIFEST_URL`, only when
`TDC_ALLOW_TEST_ENDPOINTS=1`; these are hidden test controls, not supported
user configuration.

Local profile namespace lookup order for authenticated commands:

1. If `--profile <name>` is explicitly provided, use that profile name.
2. If `TDC_PROFILE` is set, use that profile name.
3. Otherwise use `default`.

TiDB Cloud API key lookup order:

1. If either `TDC_PUBLIC_KEY` or `TDC_PRIVATE_KEY` is set, read the API key pair
   from environment variables. Both are required in this mode.
2. Otherwise read `tdc_public_key` and `tdc_private_key` from the selected
   local profile in `~/.tdc/credentials`.

Placement lookup order for authenticated commands:

1. If `--region <canonical-region-code>` is explicitly provided, use it for
   this command only.
2. If `TDC_REGION_CODE` is set, use it for this command only.
3. Otherwise use the selected profile's `region_code`.

Starter DB cluster creation project lookup order is:

1. Explicit non-empty `--project-id`.
2. The selected profile's `project_id`, discovered by `tdc configure` from the
   unique accessible project whose type is `tidbx_virtual`.
3. Otherwise omit the `tidb.cloud/project` label and let the Starter API select
   the account's default project.

An explicitly empty `--project-id` is an error and must not use the profile
or server fallback. When no project ID resolves, omit the label entirely; do
not send `tidb.cloud/project` with an empty value. Other DB commands identify
existing resources by cluster or branch ID and do not send `project_id`.
Drive9-backed tdc fs commands do not consume this DB project default.

Environment credentials are a credential source only; they must not change the
local profile namespace and must not cause tdc to write local `[env]` sections.
Generated tdc fs state is always stored under the selected local profile:
`--profile`, `TDC_PROFILE`, or `default`.

tdc fs resource selection order is:

1. Explicit `--file-system-name`.
2. `TDC_FS_FILE_SYSTEM_NAME`.
3. Otherwise fail with `fs.missing_file_system_name` before credential loading,
   endpoint resolution, companion startup, or a remote request.

Never infer a tdc fs resource from profile state, registry cardinality,
creation order, or deletion side effects. `TDC_FS_FILE_SYSTEM_NAME` is an
explicit process-scoped selector and must not be persisted.

Remote tdc fs, fs-git, fs-journal, and owner fs-vault commands use this FS
credential lookup order:

1. Explicit command-local `--fs-token`.
2. `TDC_FS_TOKEN`.
3. The selected resource's `api_key` in its resource-scoped credentials file.

Those commands do not require TiDB Cloud public/private keys. A clean machine
can use an existing resource with a file-system name, canonical region, and FS
token supplied independently through flags or environment variables. Do not
persist ephemeral flag/environment credentials or create a synthetic `[env]`
profile. `tdc fs create-file-system` and `tdc fs delete-file-system` remain
TiDB Cloud-authenticated; deletion also requires the selected locally
registered resource and its owner token.

The selector is available on tdc fs data-plane/runtime commands and all
`fs-git`, `fs-journal`, and `fs-vault` subcommands. Creation, deletion, and
description require an explicit resource name where their command contract
declares it. Drain and unmount resolve an existing mount through its mount path
and locator instead of selecting a resource again.

When implementing command handlers, detect whether `--profile` was explicitly
set before calling `config.Load`; the root flag has a default value, but that
default must not suppress `TDC_PROFILE`. Also pass the explicit `--region`
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
`~/.tdc/logs/tdc.jsonl`. They are local audit/debug summaries, not telemetry.
`TDC_LOGGING=off` disables them for the current process, and global settings
can disable them with:

```toml
# ~/.tdc/.preferences
schema_version = 1

[logging]
enabled = false
```

Environment values `off`, `false`, `0`, and `no` disable logging; `on`,
`true`, `1`, and `yes` enable it. The environment variable takes precedence
over settings. Do not add a `tdc logging status` command. Invalid settings or
environment values fail closed for logging without failing the requested
command. Legacy `[logging]` in `~/.tdc/config` is migrated atomically into
`~/.tdc/.preferences`; config and credentials remain profile-only afterward.
Every `tdc update` form must bypass settings, migration, profiles, credentials,
operation logs, and all other `~/.tdc/` state. The operation log may
record command paths, flag names, profile names, region codes, duration, exit
code, app error code/category, service name, HTTP method/status, operation, and
request id. It must never record flag values, SQL text, SQL results, file
contents, raw request/response bodies, connection strings, local paths, tdc fs
raw paths, API keys, DB passwords, or tdc fs API keys.

Generated DB SQL usernames and passwords live in
`~/.tdc/db_users/<cluster-id>/credentials`, not in the main
`~/.tdc/credentials` file. Do not add nested
`[profile.db_users."<cluster-id>".role]` TOML sections to
`~/.tdc/credentials`. TiDB Cloud cluster IDs are globally unique, so DB SQL
credentials are cluster-scoped rather than profile-scoped. `tdc db
create-db-sql-users` owns those credentials and must be idempotent: it
creates or repairs the stable tdc-managed read-only, read-write, and admin
users for a cluster instead of creating a new group every time.

Generated `tdc fs` resource API keys live only in the per-resource credentials
files under `~/.tdc/fs_resources/`. User-facing docs and commands must call
these `tdc fs` API keys or resource credentials, never reference implementation
API keys. Filesystem data-plane
commands route through the installer-managed Drive9 companion binary named
`tdc-drive9`. tdc owns profile loading, region resolution, credential storage,
preflight errors, output/query handling, and command naming; Drive9 owns the
filesystem runtime semantics for data-plane file operations, FUSE/WebDAV mount,
FUSE mount drain, layer behavior, pack/unpack, Git workflows, journal, and vault.
Do not reintroduce a runtime fallback to tdc-native fs behavior. Public fs
service methods must route through the Drive9 companion path unconditionally;
do not add switches such as `UseDrive9Companion` or hidden environment flags
that select old tdc HTTP/FUSE/WebDAV implementations.
The companion runs with resource-scoped isolated state under
`~/.tdc/drive9-home/<profile-key>/<resource-key>`; do not write or require user
edits to `~/.drive9`. Never use a shared Drive9 `current_context` as the source
of truth for tdc resource selection.

Background FS and vault mounts write only a non-secret locator under
`~/.tdc/mounts/`. Drain and unmount must route through that locator to the
original resource-scoped companion HOME without requiring the FS token again.
Successful unmount removes the locator; failed unmount preserves it for retry.

Do not implement or expose tdc commands for Drive9 internal APIs that are not
part of Drive9's public CLI. In particular, do not reintroduce low-level layer
entry/object/event commands, low-level Git workspace/tree/state/object-pack/
overlay commands, or legacy vault token commands unless Drive9 exposes a
matching public command and the tdc command surface is intentionally updated in
README, specs, tests, and AGENTS. Use `TDC_DRIVE9_BIN` only as a developer/test
override for a compatible companion; normal installs should rely on the
installer-managed `tdc-drive9`.

`tdc fs create-directory --mode` is a compatibility flag only in the Drive9
companion path: validate the octal value, but do not emulate directory chmod
with a non-public backend call. `tdc fs chmod-file` remains the explicit chmod
command and should follow Drive9 public CLI behavior.

`tdc fs-vault mount-vault` requires a delegated vault token from
`tdc fs-vault create-grant`; the selected resource's owner API key is used for
`create-secret`, `read-secret`, `list-secrets`, `replace-secret`,
`delete-secret`, grants, audit, and `run-with-secret`, but not for the vault
mount consumption path.

`tdc fs drain-file-system` is meaningful only for FUSE mounts where the
companion records a drain control socket. WebDAV mounts flush through normal
file close semantics and should not be expected to support drain.

When invoking the companion, resolve exactly one registry resource and build a
sanitized environment: `HOME` from that resource's scoped companion directory,
`DRIVE9_SERVER` from its resolved endpoint, `DRIVE9_REGION_CODE` from its
canonical resource region, `DRIVE9_API_KEY` from its per-resource credentials,
and TiDB Cloud public/private keys only for provision/delete flows. Strip
inherited `DRIVE9_*` values so user shell state cannot override tdc selection.
Debug and error output must redact TiDB Cloud keys, tdc fs API keys, vault tokens, SQL
credentials, file contents, and secret values.

`tdc db format-db-connection-string` and `tdc db execute-sql-statement` use
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
tdc [ERROR]: <actionable message>
```

Library code returns errors instead of printing or exiting. Only the CLI
boundary writes to stdout/stderr and maps errors to exit codes.

## Telemetry Rules

The product-owned telemetry backend is implemented as the independent
`tdc-telemetry-backend` process. The CLI collection and delivery path is
implemented by `internal/telemetry` under
`docs/spec/done/0022-telemetry.md`. Release archives contain a build-configured
product endpoint; local builds and CI default to disabled. `TDC_TELEMETRY`
overrides `[telemetry].enabled` in `~/.tdc/.preferences`. Help, version,
commandless usage, and all `tdc update` forms must be excluded before telemetry
reads preferences or installation state. Allowed fields:

- command and subcommand invoked
- flag names used, never flag values
- error codes and execution time
- TiDB Cloud provider and canonical region
- CLI version, OS, architecture, and install source
- anonymous installation ID and profile source category, never profile name
- an explicit process-scoped `TDC_TELEMETRY_TAG` value, bounded to 128 UTF-8 bytes
- an explicit process-scoped `TDC_TELEMETRY_EXTRA` JSON value, accepted only when complete, bounded to 2 KiB, depth-limited, and free of prohibited field names

Never collect credentials, tokens, resource IDs, file contents, SQL text, query
output, flag values, raw errors, profile names, local or remote paths, host
identity, command output, or API request/response payloads. Delivery is one
best-effort HTTPS POST with a three-second hard timeout, no foreground retry, no
redirect following, and no local durable queue. Any failure must preserve the
command's stdout, stderr, output format, and exit status.

`TDC_TELEMETRY_TAG` and `TDC_TELEMETRY_EXTRA` are read only after telemetry is
eligible and enabled, are never persisted under `~/.tdc/`, and must never appear
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
  `~/.tdc/`.
- Unit/e2e helpers should set `TDC_LOGGING=off` by default unless the test is
  explicitly verifying operation logging.
- API client tests should use mock HTTP servers once API specs are implemented.
- Live cloud tests are opt-in, skipped by default, and run through
  the focused `make live-e2e-<family>` targets or the aggregate
  `make live-e2e`. They must use the `live-e2e` profile and verify the real
  API/command surface for every implemented spec. Implemented mutating commands
  must have real live mutation coverage with resource names scoped to the test
  run and cleanup that only targets resources created by that run.

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
