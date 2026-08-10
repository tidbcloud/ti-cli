# Ti CLI Rename and Migration

## Goal

Rename the repository and user-facing command-line product from `tdc` to `ti`
without silently losing existing local configuration, credentials, filesystem
registrations, SQL credentials, or telemetry identity.

This is an explicit breaking change released as `v0.2.0`. The new release does
not provide a `tdc` executable alias, compatibility symlink, wrapper command, or
bridge release. Existing users install `ti` with the new installer and validate
the migrated state before manually removing the old installation.

The formal product names remain:

- TiDB Cloud Command Line Interface
- TiDB Cloud CLI
- `ti` when referring to the executable, commands, or implementation

## Canonical Names

After this spec, new code, build artifacts, installation paths, and active
documentation use the following names:

| Concern | Canonical value |
| --- | --- |
| GitHub repository | `github.com/tidbcloud/ti-cli` |
| Go module | `github.com/tidbcloud/ti-cli` |
| Primary executable | `ti` or `ti.exe` |
| Installed filesystem companion | `ti-drive9` or `ti-drive9.exe` |
| User state directory | `~/.ti/` |
| Profile config | `~/.ti/config` |
| Profile credentials | `~/.ti/credentials` |
| Global preferences | `~/.ti/.preferences` |
| Telemetry installation identity | `~/.ti/.telemetry-installation-id` |
| Operation log | `~/.ti/logs/ti.jsonl` |
| Telemetry backend executable | `ti-telemetry-backend` |
| Telemetry migration executable | `ti-telemetry-migrate` |
| Default install directory | `~/.ti/bin` |

Historical completed specs and release records may continue to use `tdc` when
describing behavior before `v0.2.0`. They are historical records, not current
product naming.

## User-facing Command Changes

Every current command changes only its executable prefix unless another active
spec explicitly changes that command's contract. For example:

```bash
ti configure
ti update --check
ti organization list-projects
ti db create-db-cluster --db-cluster-type starter --db-cluster-name demo
ti fs create-file-system --file-system-name workspace
ti fs mount-file-system --file-system-name workspace --mount-path ./workspace
ti fs-git clone-git-workspace --repo-url https://github.com/pingcap/tidb.git --target-path ./workspace/tidb
ti fs-journal create-journal --journal-id task-log --journal-kind agent --title "task log"
ti fs-vault list-secrets
```

The old executable must fail as absent after users remove it. `ti` must not
register `tdc` as a top-level command, alias, hidden compatibility command, or
subcommand.

Help and errors use `ti` consistently:

```text
usage: ti <command> <subcommand> [parameters]
```

```text
ti [ERROR]: authentication required: ...
```

## Repository And Source Layout

Implementation changes include:

- Change `go.mod` to `module github.com/tidbcloud/ti-cli`.
- Rewrite imports owned by this repository to the new module path.
- Rename `cmd/tdc` to `cmd/ti`.
- Rename `cmd/tdc-telemetry-backend` to `cmd/ti-telemetry-backend`.
- Rename `cmd/tdc-telemetry-migrate` to `cmd/ti-telemetry-migrate`.
- Build `bin/ti`, `bin/ti-telemetry-backend`, and
  `bin/ti-telemetry-migrate` locally.
- Package `ti` in release archives and install the checksum-verified Drive9
  companion as `ti-drive9` through the existing installer/update flow.
- Rename build variables, test-only environment variables, Makefile variables,
  workflow artifact names, installer variables, and GoReleaser artifact names
  where they describe this product rather than an external API.
- Update the Git remote examples and release URLs to
  `github.com/tidbcloud/ti-cli`.

The installed companion remains an implementation detail. User-facing output
continues to describe TiDB Cloud Filesystem rather than presenting Drive9 as a
separate product.

## Canonical Environment Variables

New documentation and output use only these canonical variables:

| Purpose | Canonical variable | Legacy v0.2.x fallback |
| --- | --- | --- |
| Selected profile | `TI_PROFILE` | `TDC_PROFILE` |
| Effective region | `TI_REGION_CODE` | `TDC_REGION_CODE` |
| TiDB Cloud API public key | `TIDB_CLOUD_PUBLIC_KEY` | `TDC_PUBLIC_KEY` |
| TiDB Cloud API private key | `TIDB_CLOUD_PRIVATE_KEY` | `TDC_PRIVATE_KEY` |
| Filesystem-scoped token | `TI_FS_TOKEN` | `TDC_FS_TOKEN` |
| Filesystem name | `TI_FS_FILE_SYSTEM_NAME` | `TDC_FS_FILE_SYSTEM_NAME` |
| Local operation logging override | `TI_LOGGING` | `TDC_LOGGING` |
| Telemetry enablement override | `TI_TELEMETRY` | `TDC_TELEMETRY` |
| Telemetry tag | `TI_TELEMETRY_TAG` | `TDC_TELEMETRY_TAG` |
| Telemetry JSON metadata | `TI_TELEMETRY_EXTRA` | `TDC_TELEMETRY_EXTRA` |
| Filesystem vault token | `TI_VAULT_TOKEN` | `TDC_VAULT_TOKEN` |
| Installer destination | `TI_INSTALL_DIR` | `TDC_INSTALL_DIR` |

The v0.2 release line temporarily accepts the corresponding legacy `TDC_*`
variables. Compatibility behavior is deterministic:

1. If only the canonical variable exists, use it.
2. If only the legacy variable exists, use it.
3. If both exist with the same value, use the canonical variable.
4. If both exist with different values, fail before any remote or local
   mutation with `config.environment_conflict`.

Legacy environment variable use must not add warnings to stdout or stderr,
because that could corrupt structured command output. The operation log may
record the deprecated variable name, but never its value. Legacy `TDC_*`
support is removed in `v0.3.0`.

Configuration flags become:

```text
--tidb-cloud-public-key <string>
--tidb-cloud-private-key <string>
```

The old `--tdc-public-key` and `--tdc-private-key` flags are removed and are not
registered as aliases. This keeps the public help surface unambiguous.

All internal CI and test variables owned by this repository also move to
`TI_*` or `TIDB_CLOUD_*` names. Internal-only compatibility aliases are not
kept.

## New Local State Model

The new state directory is independent from the old installation:

```text
~/.ti/
  config
  credentials
  .preferences
  .telemetry-installation-id
  db_users/
  fs_resources/
  fs_credentials/
  bin/
  logs/
  cache/
  local/
  mounts/
  drive9-home/
```

The credentials TOML keys become:

```toml
[default]
tidb_cloud_public_key = "..."
tidb_cloud_private_key = "..."
```

New writes use only `tidb_cloud_public_key` and
`tidb_cloud_private_key`. During the v0.2 compatibility window, readers accept
legacy `tdc_public_key` and `tdc_private_key`. If both forms exist and values
differ, loading fails instead of selecting one silently.

The profile, filesystem registry, SQL credential, preferences, and telemetry
formats otherwise retain their existing semantics. This rename must not turn
secret files into non-secret files or weaken existing file modes.

## State Directory Selection

Before loading a profile, `ti` resolves the local state directory using this
table:

| `~/.ti` | `~/.tdc` | Behavior |
| --- | --- | --- |
| absent | absent | Start with a fresh `~/.ti` state directory. |
| present | absent | Use `~/.ti` normally. |
| absent | present | Validate and perform the one-way migration. |
| present | present | Use `~/.ti` only when it contains a valid migration marker for this exact `~/.tdc`; otherwise fail and require explicit manual resolution. |

Successful migration writes `~/.ti/.migrated-from-tdc` inside the staged tree
before atomic publication. The owner-only marker contains a schema version and
the exact legacy source path. It lets later commands distinguish the expected
post-migration state, where the preserved old directory coexists with the new
one, from two independently created state trees. An absent, malformed, unsafe,
or mismatched marker does not bypass the conflict. `ti` must never merge the
two trees, choose the newest one, or overwrite either directory.

`ti update`, `ti update --check`, and `ti update --dry-run` are exceptions.
They preserve the updater contract and must not inspect, create, or migrate
either state directory.

## Migration Triggers

Migration is available through one shared Go implementation, not duplicated in
shell and PowerShell scripts:

- The new installer invokes an internal, undocumented migration execution mode
  from the verified staged executable before creating the final install
  directory.
- A normal non-update `ti` command lazily invokes the same migration path before
  loading configuration.
- Installer and lazy migration produce the same files, validation errors, and
  rollback behavior.

This lets an archive user run `ti` directly while still receiving the same safe
migration as an installer user.

The migration implementation lives in `internal/config/homemigration`. CLI
wiring and installer integration remain thin callers.

## Durable State Migration

Only durable state is copied from `~/.tdc`:

- `config`
- `credentials`
- `.preferences`
- `.telemetry-installation-id`
- `db_users/`
- `fs_resources/`
- `fs_credentials/`

The following runtime, cache, and historical data is not migrated:

- `logs/`
- `cache/`
- `local/`
- `mounts/`
- `drive9-home/`

Migration follows these invariants:

1. Never modify or delete `~/.tdc`.
2. Reject symbolic links and special files in the migration source.
3. Reject credential files with unsafe permissions rather than copying them.
4. Create a temporary sibling directory under `~/.ti`'s parent.
5. Copy only the allowlisted durable paths with their required permissions.
6. Parse and validate copied TOML and credential structures.
7. Rewrite legacy TiDB Cloud credential field names to canonical names.
8. Write and validate the owner-only migration source marker.
9. Verify the complete destination before atomically renaming it to `~/.ti`.
10. On any failure, remove only the temporary destination and leave `~/.tdc`
   unchanged.

There must never be a partially initialized `~/.ti` accepted as a successful
migration.

## Mount Migration Boundary

An active mount started by the old executable is owned by the old companion
home and process. `ti` must not adopt, terminate, drain, or unmount that process.

Before migration, inspect the old mount routing state and reject migration when
an old mount is still active. The error tells the user to use the old binary:

```bash
tdc fs drain-file-system --mount-path <path>
tdc fs unmount-file-system --mount-path <path>
```

After every old mount is unmounted, migration can proceed. New mounts use only
the new `~/.ti/drive9-home/` namespaces and the bundled `ti-drive9` executable.

## Installation And PATH Behavior

The release installer URL becomes:

```bash
curl -fsSL https://github.com/tidbcloud/ti-cli/releases/latest/download/install.sh | sh -s -- --yes
```

The installer defaults to `~/.ti/bin` and tells users to prepend it to PATH:

```bash
export PATH="$HOME/.ti/bin:$PATH"
```

PowerShell uses the equivalent `$HOME\.ti\bin` path.

Installer behavior:

- Install `ti` and `ti-drive9` together.
- Replace only artifacts previously installed by the `ti` installer in the
  selected destination.
- Never replace an unrelated executable named `ti` found elsewhere on PATH.
- When another `ti` shadows the installed executable, complete installation but
  report both the installed path and the path currently resolved by the shell.
- Never invoke `sudo`, create a system-directory symlink, or modify shell
  profile files automatically.
- Never delete `tdc`, `tdc-drive9`, or `~/.tdc`.
- Show explicit migration status and manual old-install cleanup guidance.

An unrelated `ti` executable is not treated as an updatable installation.
`ti update` accepts only archive/script installations carrying the expected
`ti` ownership metadata.

## Release And Update Contract

The first renamed release is `v0.2.0`.

The old `tdc update` command looks at the old repository and cannot safely
replace itself with a differently named executable. It is not a supported
migration path. Users must run the new `ti` installer.

The release contains:

- `ti` archives for supported operating systems and architectures
- checksums and signatures required by the current release process
- `install.sh` and `install.ps1` pointing at `tidbcloud/ti-cli`

The installers and updater continue to download the companion from Drive9's
release endpoint, verify its separate checksum manifest, and install it beside
`ti` as `ti-drive9`. The primary GitHub release archive does not duplicate the
external companion artifact.

`ti update`, `ti update --check`, and `ti update --target-version` use releases
from `github.com/tidbcloud/ti-cli`. Update output, install-source metadata, and
artifact ownership markers use the new name.

The release and deployment order is:

1. Deploy telemetry backend compatibility for both old and new event names and
   installation ID prefixes.
2. Merge repository, module, executable, installer, workflow, and test renames.
3. Publish matching product documentation.
4. Release `v0.2.0`.
5. Test installation and state migration from a real `v0.1.7` installation.
6. Announce that the `tdc` release line is no longer updated.

## Package-manager Naming

Future package-manager distribution uses a package name distinct from the
short executable name:

- Homebrew formula: `ti-cli`
- Homebrew commands: `brew install tidbcloud/tap/ti-cli` and
  `brew upgrade tidbcloud/tap/ti-cli`
- Scoop package: `ti-cli`
- Winget package ID, if added later: `TiDBCloud.TiCLI`

The installed executable remains `ti`. This follows the same package-versus-
executable distinction as packages such as `aws-cli` installing `aws`.

## Telemetry Compatibility

New CLI command telemetry uses:

- event name `ti.command.finished`
- newly generated installation IDs prefixed with `ti_`

When migration copies an existing `.telemetry-installation-id`, preserve its
value exactly, including a legacy `tdc_` prefix. Generating a new ID would split
one installation into two identities and invalidate upgrade analysis.

The telemetry backend must accept during the migration window:

- `tdc.command.finished`
- `ti.command.finished`
- legacy `tdc_` installation IDs
- new `ti_` installation IDs

The telemetry public domain, TiDB database, Docker Compose service names, and
server-side deployment directory do not need a coordinated rename. They are
operational internals and retaining them reduces deployment risk. Dashboards
must group old and new event names when measuring usage across the v0.2.0
boundary.

## GitHub Actions Migration

Repository Actions configuration changes to the new repository and canonical
variables.

Canonical live-test configuration:

- Actions variable `TI_REGION_CODE`
- Actions secret `TIDB_CLOUD_PUBLIC_KEY`
- Actions secret `TIDB_CLOUD_PRIVATE_KEY`

Use the existing local legacy `live-e2e` profile as the source when migrating
those repository settings. Update secrets with `gh secret set` through stdin
and never print values to terminal output or logs. Keep old repository values
until a workflow using the renamed values passes, then remove them.

Workflow names, artifact paths, executable paths, cache keys owned by this
project, release repository references, test binary variables, and Makefile
targets must use `ti`. Generic external variables and provider-owned variables
keep their established names.

## Documentation Migration

Current product documentation changes as one coordinated update:

- Rename commands and examples from `tdc` to `ti`.
- Rename active configuration paths from `~/.tdc` to `~/.ti`.
- Rename current environment variables to canonical `TI_*` and
  `TIDB_CLOUD_*` names.
- Point GitHub links, installer commands, issue links, badges, and source links
  at `github.com/tidbcloud/ti-cli`.
- Rename PingCAP documentation directory `ai/tdc/` to `ai/ti/`.
- Rename current PingCAP documentation filenames prefixed with `tdc-` to
  `ti-`, and update TOC and internal links.
- Update README, AGENTS, `docs/priciples.md`, active specs, examples, installer
  output, and generated command reference.
- Explain in release and migration documentation that `tdc` is the name of the
  pre-v0.2 CLI and `ti` is the new executable.

Do not mechanically rewrite historical completed spec prose. Preserve it as a
record of the behavior that was implemented under the old name. Update only
forward references that must resolve to renamed active files. AGENTS must state
that completed specs mentioning `tdc` refer to the pre-v0.2 product name.

The WIP branch `cheese/wip-remote-fs-wait-backend` remains untouched while this
rename is implemented on `main`. After the rename lands, merge the renamed main
branch into that WIP branch and resolve command, path, environment variable, and
spec-number conflicts there.

## After This Spec

New users install and invoke the renamed product directly:

```bash
curl -fsSL https://github.com/tidbcloud/ti-cli/releases/latest/download/install.sh | sh -s -- --yes
export PATH="$HOME/.ti/bin:$PATH"
ti configure
ti db list-db-clusters --db-cluster-type starter
```

Existing users run the same installer after draining old mounts. The installer
copies eligible durable state into `~/.ti`, and the user verifies `ti` before
manually deleting the old executable or `~/.tdc` directory.

Automation migrates to canonical environment variable names immediately. A
v0.2.x compatibility window lets existing jobs keep legacy variable names while
their owners update them without changing command output.

## Implementation Design

Keep migration and compatibility decisions at existing ownership boundaries:

- `internal/config/homemigration` validates and atomically copies durable old
  state.
- `internal/config` resolves canonical and legacy environment variables and
  rejects conflicting values.
- `internal/config/store` continues to own TOML parsing, file modes, and atomic
  file writes.
- `internal/settings` reads only the selected home and keeps legacy preference
  migrations within that home.
- `internal/telemetry` generates new identity prefixes and preserves migrated
  identities.
- `internal/update` recognizes only `ti` release ownership and never triggers
  home migration.
- `internal/cli` owns renamed commands, flags, help, and errors; business
  packages must not special-case the executable name.
- installer scripts call the Go migration implementation instead of copying
  configuration themselves.

Do not introduce cgo. The migration uses standard Go filesystem APIs and must
work on macOS, Linux, and Windows. Platform-specific permission checks may be
implemented behind small build-tagged helpers where Windows semantics differ.

## Dependencies And Platform

- Use Go standard-library filesystem and process APIs for home migration.
- Continue using the repository's existing TOML parser, Cobra command tree,
  output renderer, operation logger, and installer ownership metadata.
- Do not add a migration framework or a shell-only state copier.
- Do not add cgo or a platform-specific runtime dependency.
- macOS, Linux, and Windows installers must implement the same state-selection
  and conflict contract, with permission checks adapted to each platform.
- The installed filesystem companion remains required for filesystem commands and
  is installed and updated with the primary executable.

## Dependencies

- `0001-cli-foundation.md` for command, help, and binary behavior.
- `0002-local-config-and-credentials.md` for profile and credential ownership.
- `0012-install-and-update-distribution.md` for archive ownership and updater
  safety.
- `0013-github-actions-ci-cd.md` for release and live e2e workflows.
- `0015-drive9-companion-wrapper-for-tdc-fs.md` for companion installation and
  mount ownership.
- `0021-global-settings.md` and `0022-telemetry.md` for durable global state and
  telemetry identity.

## Call Chains

Normal command with existing new state:

1. Resolve the user home directory.
2. Observe `~/.ti` and no `~/.tdc` migration requirement.
3. Load canonical environment variables and `~/.ti` profile state.
4. Execute the existing command-specific API chain.

Normal command requiring migration:

1. Resolve the user home directory.
2. Observe `~/.tdc` and no `~/.ti`.
3. Validate source file types, permissions, and inactive mount state.
4. Copy and rewrite allowlisted durable state into a temporary directory.
5. Validate the temporary destination.
6. Atomically rename it to `~/.ti`.
7. Load the migrated profile and continue command execution.

Installer migration:

1. Download and verify `ti` from the new GitHub release and `ti-drive9` from
   the Drive9 release endpoint.
2. Extract `ti` into a temporary staging directory.
3. Invoke the shared migration execution mode from the staged executable.
4. Install both artifacts into the selected user-owned directory.
5. Print installation path, migration result, PATH guidance, and next steps.

This spec changes local product behavior and release endpoints. It introduces
no new TiDB Cloud control-plane or data-plane API calls.

## Output And Errors

Stable migration errors include:

- `config.home_migration_conflict`: both state directories exist without a
  valid marker proving that `~/.ti` was atomically migrated from this exact
  `~/.tdc` path.
- `config.home_migration_active_mount`: an old mount must be drained and
  unmounted with the old executable.
- `config.home_migration_unsafe_source`: a source file is a symlink, special
  file, or has unsafe secret permissions.
- `config.home_migration_failed`: copying, rewriting, validation, or atomic
  publication failed.
- `config.environment_conflict`: canonical and legacy environment variables or
  credential fields contain different values.

Errors must name the affected paths or variable names but never include API
keys, filesystem tokens, SQL passwords, vault tokens, telemetry extra values,
or complete credential file contents.

## Tests

Unit tests cover:

- every state-directory selection row
- complete migration of the durable allowlist
- exclusion of logs, caches, local runtime data, mounts, and companion home
- old source preservation after success and failure
- atomic cleanup after injected copy, parse, permission, and rename failures
- symlink and special-file rejection
- credential permission validation
- active old mount rejection
- canonical-only, legacy-only, equal dual, and conflicting dual environment
  variable resolution
- canonical and legacy credential field resolution
- update commands bypassing migration
- migrated telemetry identity preservation and new identity generation
- renamed help, errors, executable paths, and release metadata

Black-box e2e tests use temporary HOME directories and cover:

- a fresh `ti` installation
- installer migration from a representative `v0.1.7` home
- lazy migration when running an archive directly
- rejection when both homes exist
- continued command execution after successful migration
- structured JSON output remaining unpolluted when a legacy variable is used
- installer PATH-shadow reporting without replacing the shadowing executable

Release verification builds and tests every supported archive, confirms the
archive contains `ti`, confirms installers/updaters plan and verify `ti-drive9`,
and performs a real migration rehearsal
from an installed `v0.1.7` release before `v0.2.0` is announced.

## Acceptance Criteria

- `go.mod`, repository-owned imports, binaries, commands, help, errors,
  installers, archives, workflows, and current documentation use `ti` and
  `github.com/tidbcloud/ti-cli`.
- No `tdc` executable alias, wrapper, hidden command, symlink, or release bridge
  is shipped.
- A user with only a valid `~/.tdc` gets a complete, atomic, one-way migration
  to `~/.ti` without changes to the old directory.
- A user with two independently created state directories receives a
  deterministic error and no mutation; a valid completed-migration marker
  permits the expected preserved old directory to coexist with `~/.ti`.
- Active old mounts block migration with actionable old-command instructions.
- New state writes use canonical credential fields and environment variable
  names.
- Legacy environment variables and credential fields work through v0.2.x,
  conflict deterministically, and produce no output warnings.
- `ti update` uses the new repository and never reads or migrates local product
  state.
- New telemetry uses the new event and identity prefix, while migrated identity
  remains stable and the backend accepts both generations.
- Homebrew and Scoop planning uses package name `ti-cli` and executable `ti`.
- README, AGENTS, principles, active specs, PingCAP docs, and command reference
  agree with the implemented command and path contracts.

## Out Of Scope

- Removing old user files or old binaries automatically.
- Supporting `tdc` as an alias after `v0.2.0`.
- Merging independently modified `~/.tdc` and `~/.ti` trees.
- Adopting or terminating mounts started by the old companion.
- Renaming the telemetry database, production host, Docker Compose services, or
  server deployment directory solely for cosmetic consistency.
- Implementing Homebrew or Scoop distribution in this spec.
- Renaming or rebasing the remote-filesystem WIP branch as part of the main
  rename change.
