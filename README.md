# ti

ti ([TiDB Cloud](https://tidbcloud.com) CLI) is a unified tool to manage your TiDB Cloud Filesystem (FS) and Starter services.

- TiDB Cloud Filesystem is a serverless distributed file system designed specifically for AI coding agent workloads.
- TiDB Cloud Starter provides serverless distributed database clusters that are fully compatible with MySQL.

> `ti` is currently in preview. Subcommands labeled as preview are subject to change without prior notice.

## 3-Command Superpower for Your Agent

### Always-On File System for Sandboxes — Zero Infrastructure Required

With `ti`, an agent can persist state between sessions, share files across sandboxes, snapshot its workspace before attempting a risky operation, and roll back on failure — all through a CLI with POSIX compatibility.

1. Create a file system and obtain the file system token (performed once, outside the sandbox).

```shell
export TI_FS_TOKEN="$(ti fs create-file-system --region <REGION_CODE> --wait --query fs_token --output text)"
```

2. Mount the filesystem to a local path and use it as a normal POSIX-compliant filesystem (performed within the sandbox)

```shell
export TI_FS_TOKEN="<FS_TOKEN>"
ti fs mount-file-system --mount-path /path-to-workspace --region <REGION_CODE>
echo "Hello Sandbox Workspace!" >> /path-to-workspace/hello.txt
```

3. Unmount the file system to release the workspace before passing it to another sandbox (performed within the sandbox).

```shell
ti fs unmount-file-system --mount-path /path-to-workspace --region <REGION_CODE>
```

### Always-On MySQL — Zero Infrastructure Required

With `ti`, an agent can go from zero to live HTAP SQL (Hybrid Transaction / Analytical Processing) in three commands:

1. Provision a serverless MySQL-compatible cluster, wait until it is active, and capture its ID

```shell
export CLUSTER_ID="$(ti db create-db-cluster --db-cluster-type starter --db-cluster-name my-app-db --wait --query id --output text)"
```

2. Create the SQL users it needs to connect

```shell
ti db create-db-sql-users --db-cluster-id "$CLUSTER_ID"
```

3. Retrieve the database connection string for your agent and share it across sandboxes as needed

```shell
export DATABASE_URL="$(ti db format-db-connection-string --db-cluster-id "$CLUSTER_ID" --read-write --query connection_string --output text)"
```

## Install

macOS and Linux users:

```bash
curl -fsSL https://github.com/tidbcloud/ti-cli/releases/latest/download/install.sh | sh -s -- --yes
```

After installation, add ti to the current shell and verify it:

```bash
export PATH="$HOME/.ti/bin:$PATH"
ti --version
```

The installer writes `ti` and `ti-drive9` to `~/.ti/bin` without sudo. Add the `export PATH=...` line to your shell profile to make it persistent.

Windows users:

```powershell
$script = "$env:TEMP\install-ti.ps1"
iwr https://github.com/tidbcloud/ti-cli/releases/latest/download/install.ps1 -OutFile $script
powershell -ExecutionPolicy Bypass -File $script -Yes
```

After installation, add ti to the current PowerShell session and verify it:

```powershell
$env:Path = "$HOME\.ti\bin;$env:Path"
ti --version
```

Add `$HOME\.ti\bin` to your user `PATH` to keep ti available in new PowerShell sessions.

### Upgrade from tdc v0.1.x

The first `ti` installer or non-update command migrates durable state from `~/.tdc` to `~/.ti` when `~/.ti` does not already exist. It copies profiles, credentials, preferences, telemetry identity, DB SQL credentials, and filesystem registrations; it leaves logs, caches, mount state, and the old Drive9 home behind. The migration never modifies or deletes `~/.tdc`. A hidden owner-only marker under `~/.ti` records that the two directories coexist because migration completed successfully.

Drain and unmount every mount started by `tdc` before installing `ti`. If both directories were created independently, or the migration marker is absent or invalid, migration fails instead of merging or overwriting either directory. Verify `ti` before manually removing the old binaries or state.

Automation should move to `TI_*` and `TIDB_CLOUD_*` environment variables. The v0.2.x release line accepts the corresponding legacy `TDC_*` names when only one form is set; conflicting old and new values fail before any mutation.

## Quick Start Guide

### Configure

- Authentication: a TiDB Cloud Public Key and a Private Key from the [TiDB Cloud API Keys](https://tidbcloud.com/org-settings/api-keys) console.
- Default region: one of aws-us-east-1, aws-us-west-2, aws-eu-central-1, aws-ap-northeast-1, aws-ap-southeast-1, or alicloud-ap-southeast-1.
    - Regions supporting TiDB Cloud Filesystem: aws-us-east-1, aws-ap-southeast-1, aws-us-west-2, or alicloud-ap-southeast-1. These endpoints are built into `ti`; endpoint resolution does not download a Drive9 region manifest.
    - Regions supporting TiDB Cloud Starter: aws-us-east-1, aws-us-west-2, aws-eu-central-1, aws-ap-northeast-1, aws-ap-southeast-1, or alicloud-ap-southeast-1.

Set up a default profile with one command:

```shell
ti configure --non-interactive --region-code <TI_REGION_CODE> --tidb-cloud-public-key <TIDB_CLOUD_PUBLIC_KEY> --tidb-cloud-private-key <TIDB_CLOUD_PRIVATE_KEY>
```

Alternatively, set up a default profile interactively by running the command below. You will be prompted to enter your TiDB Cloud Public Key, Private Key, and the default region:

```shell
ti configure
```

`ti configure` stores non-sensitive profile configuration in `~/.ti/config` and API credentials in `~/.ti/credentials`.

### Global Settings and Operation Logs

Process-wide preferences are separate from profiles. The optional, hidden `~/.ti/.preferences` file applies to every profile and is not created on fresh installs or by `ti configure`. Local operation logs are enabled by default at `~/.ti/logs/ti.jsonl`; they contain redacted command and API summaries, not command values or user data.

To disable operation logging persistently, create `~/.ti/.preferences`:

```toml
schema_version = 1

[logging]
enabled = false
max_file_mb = 10
max_files = 5
```

Use `TI_LOGGING=off` to disable logging for one process. Accepted values are `on`, `true`, `1`, `yes`, `off`, `false`, `0`, and `no`. An existing `[logging]` section in `~/.ti/config` is migrated automatically to `~/.ti/.preferences`; profiles and credentials are preserved. `ti update` does not read or write settings, profiles, credentials, operation logs, or other `~/.ti/` state.

### Anonymous Telemetry

Release builds collect minimal command usage and reliability telemetry through the ti-owned ingestion service. Events contain canonical command and explicitly supplied flag names, stable exit and error codes, duration, region, ti version, OS, architecture, install source, and a random installation ID. They never contain flag values, credentials, tokens, SQL text, paths, file contents, command output, API payloads, profile names, or cloud resource IDs.

Telemetry is disabled by default for development builds and recognized CI environments. To disable it persistently for release builds, create or edit `~/.ti/.preferences`:

```toml
schema_version = 1

[telemetry]
enabled = false
```

Use `TI_TELEMETRY=off` to disable telemetry for one process, or `TI_TELEMETRY=on` to explicitly enable it for an eligible release or development invocation whose build contains the product endpoint. Accepted values are `on`, `true`, `1`, `off`, `false`, `0`. Help, version, commandless usage, and every `ti update` mode never send telemetry. The pseudonymous ID is stored with owner-only permissions at `~/.ti/.telemetry-installation-id`; deleting that file resets the identity without changing the preference.

An integration can add optional process-scoped attribution without changing a profile or command: `TI_TELEMETRY_TAG` is a UTF-8 string limited to 128 bytes, and `TI_TELEMETRY_EXTRA` is one complete JSON value limited to 2 KiB after compaction. Invalid, prohibited, or oversized extra metadata is omitted; the command still runs normally. Never include credentials, tokens, SQL, paths, personal data, profile names, or cloud resource IDs in either value.

### TiDB Cloud Filesystem

The following example uses `jq` to extract the server-assigned ID and one-time token from one create response.

```shell
mkdir ~/my-workspace
umask 077
ti fs create-file-system \
  --display-name my-workspace \
  --label environment=development \
  --wait > ./filesystem.json
export FILE_SYSTEM_ID="$(jq -r '.file_system_id' ./filesystem.json)"
export TI_FS_TOKEN="$(jq -r '.fs_token' ./filesystem.json)"
rm ./filesystem.json
ti fs mount-file-system --file-system-id "$FILE_SYSTEM_ID" --mount-path ~/my-workspace
```

Automatic mounting uses FUSE on Linux and WebDAV on macOS and Windows. macOS users can install macFUSE and explicitly add `--driver fuse` for the full FUSE experience.

`ti fs list-file-systems` reads the region-scoped remote inventory through TiDB Cloud credentials. A profile can access multiple file systems, including resources created on another machine. Data-plane commands never infer a resource from the number of local credentials, so provide `--file-system-id` or set `TI_FS_FILE_SYSTEM_ID`:

```shell
ti fs list-file-systems
ti fs describe-file-system --file-system-id "$FILE_SYSTEM_ID"
export TI_FS_FILE_SYSTEM_ID="$FILE_SYSTEM_ID"
ti fs list-files
```

`--display-name` and repeatable `--label key=value` flags add organization-visible metadata to the remote inventory. They do not identify a resource for later operations: Drive9 still assigns the stable `file_system_id`, and describe, delete, data-plane, mount, and token commands select by that ID. Do not store passwords, tokens, connection strings, private paths, or personal data in labels. The create response returns the owner credential as `fs_token` once; treat it as a secret. The example above captures the ID and token from one provisioning request and removes the temporary owner-only JSON file immediately.

List results include authoritative display names, labels, status, region, quota and usage, plus the non-secret `has_local_token` hint. Filter the remote inventory by a display-name substring and one exact label without exposing token values:

```shell
ti fs list-file-systems \
  --display-name workspace \
  --label environment=development
```

One Filesystem can have multiple independently managed tokens for different machines, CI jobs, and sandboxes. Owner tokens authorize the complete Filesystem and can issue path-and-operation-limited `fs_scoped` tokens. The remote service is the source of truth for token inventory, while each local profile stores at most one selected token for each Filesystem. Generate an additional owner token and capture its one-time plaintext response:

```shell
umask 077
ti fs generate-file-system-token \
  --file-system-id "$FILE_SYSTEM_ID" \
  --token-name ci-deploy \
  --ttl 24h > ./ci-token.json
ti fs list-file-system-tokens --file-system-id "$FILE_SYSTEM_ID" --output text
```

Use an owner token to issue a finite scoped token. Repeat `--allow`; supported operations are `read`, `list`, `search`, `write`, and `delete`, and `search` requires `read`:

```shell
export TI_FS_TOKEN="<OWNER_FS_TOKEN>"
ti fs generate-file-system-scoped-token \
  --subject sandbox-agent \
  --ttl 24h \
  --allow /workspace:read,list,write \
  --allow /artifacts:read,list
```

`TI_FS_TOKEN` may contain either token kind. Scoped tokens work only for allowed paths and operations and can self-refresh; they cannot generate child tokens or manage token inventory. Explicit `--fs-token` takes precedence over the environment. Token list, enable, disable, and delete use an explicit/environment owner token when present, otherwise they use configured TiDB Cloud API keys. With owner Bearer authentication, enable and disable apply only to scoped targets; TiDB Cloud credentials can manage either token kind. Because the token JWT does not expose its kind or scopes, the FS backend is the final permission authority.

An owner FS token authorizes Filesystem use and token management, but it is not a TiDB Cloud administrative credential. Creating, listing, describing, and deleting Filesystem resources require TiDB Cloud API keys. In particular, `ti fs delete-file-system` always requires an explicit `--file-system-id`; `TI_FS_TOKEN` cannot select or authorize deletion of the Filesystem itself. For token list, enable, disable, and delete commands, `--file-system-id` is required only when the command uses TiDB Cloud API keys. When an owner token is supplied through `--fs-token` or `TI_FS_TOKEN`, `ti` derives the Filesystem ID from that token.

Generation does not modify local credentials by default. Add `--store-locally` to select the new token locally; if a selected token already exists, add `--replace` explicitly. Replacing local selection does not revoke the previous remote token. Use immutable `token_id` values from the list response to disable, enable, or permanently revoke a token:

```shell
ti fs disable-file-system-token --file-system-id "$FILE_SYSTEM_ID" --token-id <token-id>
ti fs enable-file-system-token --file-system-id "$FILE_SYSTEM_ID" --token-id <token-id>
ti fs delete-file-system-token --file-system-id "$FILE_SYSTEM_ID" --token-id <token-id>
```

Rotate a locally selected token with `ti fs refresh-file-system-token --file-system-id "$FILE_SYSTEM_ID"`. To rotate a token supplied by a secret manager, set `TI_FS_TOKEN` and `TI_REGION_CODE`; `ti` returns the replacement plaintext but cannot update the external secret manager. Refresh is not safely retryable if the response is lost. For shared environments, generate and distribute a replacement first, validate it, then disable and delete the old token. Authentication state can take approximately 10 seconds to converge. Before refreshing, disabling, or deleting a token used by a local mount, run `drain-file-system` and `unmount-file-system`.

An agent sandbox can then use that existing file system without running `ti configure` or providing TiDB Cloud API keys:

```shell
export TI_FS_TOKEN="<FS_TOKEN>"
export TI_REGION_CODE="aws-us-east-1"
ti fs mount-file-system --mount-path /path_to_workspace
```

The token contains its file system ID, so a clean sandbox does not need `TI_FS_FILE_SYSTEM_ID`. Set that variable only as an optional consistency assertion. To persist an existing token on another configured or unconfigured machine, run `ti fs import-file-system-token --from-file ./fs-token`; subsequent commands can select its ID without resupplying the token.

### TiDB Cloud Starter

`ti db` currently implements TiDB Cloud Starter only. Commands that do not identify an existing cluster require `--db-cluster-type starter`; there is no implicit default. Commands with `--db-cluster-id` discover the authoritative service plan and route internally without accepting a type flag. Essential, Premium, Dedicated, unknown, and conflicting plan metadata are rejected before the requested product operation.

Cluster lists include only verified Starter clusters in the effective region and omit other service plans, cross-region resources, and resources whose region cannot be verified. Use global `--region`, for example `ti --region aws-us-west-2 db list-db-clusters --db-cluster-type starter`, to inspect another region without changing the stored profile. Listing incrementally fills the requested result page from TiDB Cloud API pages. Its opaque `next_page_token` belongs to `ti` and can be passed only to a later call with the same profile, type, region, filter, and ordering.

`ti configure` stores the selected profile's region and API keys locally without making a TiDB Cloud request. Cluster creation omits project selection and lets TiDB Cloud select its server-side default project. Project metadata returned by TiDB Cloud remains visible in the cluster response.

```shell
ti db create-db-cluster --db-cluster-type starter --db-cluster-name my-distributed-mysql --wait
```

## Get Help

- `ti`
- `ti help`
- `ti <command> help`
- `ti <command> <subcommand> help`

Structured commands output JSON by default. Use `--output text` for command-specific tables or readable key-value output; it never falls back to JSON. When combined with `--query`, scalar lists are printed one item per line and object lists are printed as tables. Commands that intentionally stream raw bytes preserve those bytes and reject `--query`.

<details>
<summary>All commands</summary>

```text
ti configure
ti update

ti db create-db-cluster --db-cluster-type starter
ti db list-db-clusters --db-cluster-type starter
ti db describe-db-cluster
ti db update-db-cluster
ti db delete-db-cluster
ti db create-db-cluster-branch
ti db list-db-cluster-branches
ti db describe-db-cluster-branch
ti db delete-db-cluster-branch
ti db create-db-sql-users
ti db format-db-connection-string
ti db execute-sql-statement

ti fs create-file-system
ti fs import-file-system-token
ti fs generate-file-system-token
ti fs generate-file-system-scoped-token
ti fs list-file-system-tokens
ti fs enable-file-system-token
ti fs disable-file-system-token
ti fs delete-file-system-token
ti fs refresh-file-system-token
ti fs delete-file-system
ti fs list-file-systems
ti fs describe-file-system
ti fs check-file-system
ti fs copy-file
ti fs read-file
ti fs list-files
ti fs describe-file
ti fs move-file
ti fs delete-file
ti fs create-directory
ti fs chmod-file
ti fs create-symlink
ti fs create-hardlink
ti fs search-file-content
ti fs find-files
ti fs create-layer
ti fs list-layers
ti fs describe-layer
ti fs diff-layer
ti fs create-layer-checkpoint
ti fs rollback-layer
ti fs commit-layer
ti fs pack-file-system
ti fs unpack-file-system
ti fs mount-file-system
ti fs drain-file-system
ti fs unmount-file-system

ti fs-git clone-git-workspace
ti fs-git hydrate-git-workspace
ti fs-git add-git-worktree
ti fs-git remove-git-worktree

ti fs-journal create-journal
ti fs-journal append-journal-entries
ti fs-journal read-journal-entries
ti fs-journal search-journal-entries
ti fs-journal verify-journal

ti fs-vault create-secret
ti fs-vault replace-secret
ti fs-vault read-secret
ti fs-vault list-secrets
ti fs-vault delete-secret
ti fs-vault create-grant
ti fs-vault delete-grant
ti fs-vault list-audit-events
ti fs-vault run-with-secret
ti fs-vault mount-vault
ti fs-vault unmount-vault
```

Filesystem aliases are `cp`, `cat`, `ls`, `stat`, `mv`, `rm`, `mkdir`, `chmod`, `symlink`, `hardlink`, `grep`, `find`, `mount`, `drain`, and `umount`. Aliases keep the canonical command's long flags.

</details>

## Update

```bash
ti update --check
ti update --dry-run
ti update
ti update --target-version v0.1.1
```

`ti update` downloads and verifies both `ti` and its `ti-drive9` companion before replacing either binary in the user-writable install directory. It never requests sudo. The old `ti update` command cannot migrate to a differently named executable; install `ti` once with the new installer instead.

## Documentation

- [Preview Documentation](docs/pingcap-docs/docs/ai/ti/ti-overview.md)

## Build From Source

Requirements:

- Go 1.26.5 or newer
- `make`
- GoReleaser, only for `make release-snapshot` or release publishing

Build the local binary:

```bash
make build
```

The binary is written to:

```text
bin/ti
```

Build the independently deployed telemetry ingestion service:

```bash
make build-telemetry-backend
```

The backend binary is written to `bin/ti-telemetry-backend`. Its API, privacy contract, TiDB/PostHog batching behavior, and Docker deployment are documented in [Telemetry Backend Design](docs/telemetry-backend-design.md).

## Test

Run local unit and black-box tests without live cloud credentials:

```bash
make test
make e2e
```

Run one live command family against the `live-e2e` profile:

```bash
make live-e2e-configure
make live-e2e-db
make live-e2e-fs
make live-e2e-fs-git
make live-e2e-fs-journal
make live-e2e-fs-vault
```

Run the complete live suite in one test process:

```bash
make live-e2e
```

Set `LIVE_E2E_PROFILE=<profile>` to use a profile other than `live-e2e`. The DB and FS suites perform real cloud mutations and clean up only resources created by the test run.
