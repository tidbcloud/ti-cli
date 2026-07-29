# tdc

tdc ([TiDB Cloud](https://tidbcloud.com) CLI) is a unified tool to manage your TiDB Cloud Filesystem (FS) and Starter services.

- TiDB Cloud Filesystem is a serverless distributed file system designed specifically for AI coding agent workloads.
- TiDB Cloud Starter provides serverless distributed database clusters that are fully compatible with MySQL.

> `tdc` is currently in preview. Subcommands labeled as preview are subject to change without prior notice.

## 3-Command Superpower for Your Agent

### Always-On File System for Sandboxes — Zero Infrastructure Required

With `tdc`, an agent can persist state between sessions, share files across sandboxes, snapshot its workspace before attempting a risky operation, and roll back on failure — all through a CLI with POSIX compatibility.

1. Create a file system and obtain the file system token (performed once, outside the sandbox).

```shell
export TDC_FS_TOKEN="$(tdc fs create-file-system --file-system-name agent-workspace --region <REGION_CODE> --wait --query fs_token --output text)"
```

2. Mount the filesystem to a local path and use it as a normal POSIX-compliant filesystem (performed within the sandbox)

```shell
export TDC_FS_TOKEN="<FS_TOKEN>"
tdc fs mount-file-system --file-system-name agent-workspace --mount-path /path-to-workspace --region <REGION_CODE>
echo "Hello Sandbox Workspace!" >> /path-to-workspace/hello.txt
```

3. Unmount the file system to release the workspace before passing it to another sandbox (performed within the sandbox).

```shell
tdc fs unmount-file-system --mount-path /path-to-workspace --region <REGION_CODE>
```

### Always-On MySQL — Zero Infrastructure Required

With `tdc`, an agent can go from zero to live HTAP SQL (Hybrid Transaction / Analytical Processing) in three commands:

1. Provision a serverless MySQL-compatible cluster, wait until it is active, and capture its ID

```shell
export CLUSTER_ID="$(tdc db create-db-cluster --db-cluster-name my-app-db --wait --query id --output text)"
```

2. Create the SQL users it needs to connect

```shell
tdc db create-db-sql-users --db-cluster-id "$CLUSTER_ID"
```

3. Retrieve the database connection string for your agent and share it across sandboxes as needed

```shell
export DATABASE_URL="$(tdc db format-db-connection-string --db-cluster-id "$CLUSTER_ID" --read-write --query connection_string --output text)"
```

## Install

macOS and Linux users:

```bash
curl -fsSL https://github.com/tidbcloud/tdc/releases/latest/download/install.sh | sh -s -- --yes
```

After installation, add tdc to the current shell and verify it:

```bash
export PATH="$HOME/.tdc/bin:$PATH"
tdc --version
```

The installer writes `tdc` and `tdc-drive9` to `~/.tdc/bin` without sudo. Add the `export PATH=...` line to your shell profile to make it persistent.

Windows users:

```powershell
$script = "$env:TEMP\install-tdc.ps1"
iwr https://github.com/tidbcloud/tdc/releases/latest/download/install.ps1 -OutFile $script
powershell -ExecutionPolicy Bypass -File $script -Yes
```

After installation, add tdc to the current PowerShell session and verify it:

```powershell
$env:Path = "$HOME\.tdc\bin;$env:Path"
tdc --version
```

Add `$HOME\.tdc\bin` to your user `PATH` to keep tdc available in new PowerShell sessions.

## Quick Start Guide

### Configure

- Authentication: a TiDB Cloud Public Key and a Private Key from the [TiDB Cloud API Keys](https://tidbcloud.com/org-settings/api-keys) console.
- Default region: one of aws-us-east-1, aws-us-west-2, aws-eu-central-1, aws-ap-northeast-1, aws-ap-southeast-1, or ali-ap-southeast-1.
    - Regions support TiDB Cloud Filesystem: aws-us-east-1, aws-ap-southeast-1.
    - Regions support TiDB Cloud Starter: aws-us-east-1, aws-us-west-2, aws-eu-central-1, aws-ap-northeast-1, aws-ap-southeast-1, or ali-ap-southeast-1.

Set up a default profile with one command:

```shell
tdc configure --non-interactive --region-code <TDC_REGION_CODE> --tdc-public-key <TDC_PUBLIC_KEY> --tdc-private-key <TDC_PRIVATE_KEY>
```

Alternatively, set up a default profile interactively by running the command below. You will be prompted to enter your TiDB Cloud Public Key, Private Key, and the default region:

```shell
tdc configure
```

`tdc configure` stores non-sensitive profile configuration in `~/.tdc/config` and API credentials in `~/.tdc/credentials`.

### Global Settings and Operation Logs

Process-wide preferences are separate from profiles. The optional, hidden `~/.tdc/.preferences` file applies to every profile and is not created on fresh installs or by `tdc configure`. Local operation logs are enabled by default at `~/.tdc/logs/tdc.jsonl`; they contain redacted command and API summaries, not command values or user data.

To disable operation logging persistently, create `~/.tdc/.preferences`:

```toml
schema_version = 1

[logging]
enabled = false
max_file_mb = 10
max_files = 5
```

Use `TDC_LOGGING=off` to disable logging for one process. Accepted values are `on`, `true`, `1`, `yes`, `off`, `false`, `0`, and `no`. An existing `[logging]` section in `~/.tdc/config` is migrated automatically to `~/.tdc/.preferences`; profiles and credentials are preserved. `tdc update` does not read or write settings, profiles, credentials, operation logs, or other `~/.tdc/` state.

### Anonymous Telemetry

Release builds collect minimal command usage and reliability telemetry through the tdc-owned ingestion service. Events contain canonical command and explicitly supplied flag names, stable exit and error codes, duration, region, tdc version, OS, architecture, install source, and a random installation ID. They never contain flag values, credentials, tokens, SQL text, paths, file contents, command output, API payloads, profile names, or cloud resource IDs.

Telemetry is disabled by default for development builds and recognized CI environments. To disable it persistently for release builds, create or edit `~/.tdc/.preferences`:

```toml
schema_version = 1

[telemetry]
enabled = false
```

Use `TDC_TELEMETRY=off` to disable telemetry for one process, or `TDC_TELEMETRY=on` to explicitly enable it for an eligible release or development invocation whose build contains the product endpoint. Accepted values are `on`, `true`, `1`, `off`, `false`, `0`. Help, version, commandless usage, and every `tdc update` mode never send telemetry. The pseudonymous ID is stored with owner-only permissions at `~/.tdc/.telemetry-installation-id`; deleting that file resets the identity without changing the preference.

### TiDB Cloud Filesystem

```shell
mkdir ~/my-workspace
tdc fs create-file-system --file-system-name my-workspace --wait
tdc fs mount-file-system --file-system-name my-workspace --mount-path ~/my-workspace
```

Automatic mounting uses FUSE on Linux and WebDAV on macOS and Windows. macOS users can install macFUSE and explicitly add `--driver fuse` for the full FUSE experience.

One profile can manage multiple file systems. tdc never infers which resource a command targets, so provide `--file-system-name` for one-off commands or set `TDC_FS_FILE_SYSTEM_NAME` for repeated commands:

```shell
tdc fs create-file-system --file-system-name scratch
tdc fs list-file-systems
tdc fs describe-file-system --file-system-name scratch
export TDC_FS_FILE_SYSTEM_NAME=scratch
tdc fs list-files
```

`create-file-system` returns an file system token (`fs_token`) in its JSON result. This is the file system owner credential and should be handled as a secret. A configured machine can provision a file system and capture the token without printing the full result:

```shell
export TDC_FS_TOKEN="$(tdc fs create-file-system --file-system-name agent-workspace --wait --query fs_token --output text)"
```

An agent sandbox can then use that existing file system without running `tdc configure` or providing TiDB Cloud API keys:

```shell
export TDC_FS_TOKEN="<FS_TOKEN>"
tdc fs mount-file-system --file-system-name agent-workspace --mount-path /path_to_workspace --region aws-us-east-1
```

### TiDB Cloud Starter

```shell
tdc db create-db-cluster --db-cluster-name my-distributed-mysql --wait
```

## Get Help

- `tdc`
- `tdc help`
- `tdc <command> help`
- `tdc <command> <subcommand> help`

<details>
<summary>All commands</summary>

```text
tdc configure
tdc update
tdc organization list-projects

tdc db create-db-cluster
tdc db list-db-clusters
tdc db describe-db-cluster
tdc db update-db-cluster
tdc db delete-db-cluster
tdc db create-db-cluster-branch
tdc db list-db-cluster-branches
tdc db describe-db-cluster-branch
tdc db delete-db-cluster-branch
tdc db create-db-sql-users
tdc db format-db-connection-string
tdc db execute-sql-statement

tdc fs create-file-system
tdc fs delete-file-system
tdc fs list-file-systems
tdc fs describe-file-system
tdc fs check-file-system
tdc fs copy-file
tdc fs read-file
tdc fs list-files
tdc fs describe-file
tdc fs move-file
tdc fs delete-file
tdc fs create-directory
tdc fs chmod-file
tdc fs create-symlink
tdc fs create-hardlink
tdc fs search-file-content
tdc fs find-files
tdc fs create-layer
tdc fs list-layers
tdc fs describe-layer
tdc fs diff-layer
tdc fs create-layer-checkpoint
tdc fs rollback-layer
tdc fs commit-layer
tdc fs pack-file-system
tdc fs unpack-file-system
tdc fs mount-file-system
tdc fs drain-file-system
tdc fs unmount-file-system

tdc fs-git clone-git-workspace
tdc fs-git hydrate-git-workspace
tdc fs-git add-git-worktree
tdc fs-git remove-git-worktree

tdc fs-journal create-journal
tdc fs-journal append-journal-entries
tdc fs-journal read-journal-entries
tdc fs-journal search-journal-entries
tdc fs-journal verify-journal

tdc fs-vault create-secret
tdc fs-vault replace-secret
tdc fs-vault read-secret
tdc fs-vault list-secrets
tdc fs-vault delete-secret
tdc fs-vault create-grant
tdc fs-vault delete-grant
tdc fs-vault list-audit-events
tdc fs-vault run-with-secret
tdc fs-vault mount-vault
tdc fs-vault unmount-vault
```

Filesystem aliases are `cp`, `cat`, `ls`, `stat`, `mv`, `rm`, `mkdir`, `chmod`, `symlink`, `hardlink`, `grep`, `find`, `mount`, `drain`, and `umount`. Aliases keep the canonical command's long flags.

</details>

## Update

```bash
tdc update --check
tdc update --dry-run
tdc update
tdc update --target-version v0.1.1
```

`tdc update` downloads and verifies both `tdc` and its `tdc-drive9` companion before replacing either binary in the user-writable install directory. It never requests sudo. Legacy installations under `/usr/local/bin` must run the installer once to migrate to `~/.tdc/bin`.

## Documentation

- [Preview Documentation](docs/pingcap-docs/docs/ai/tdc/tdc-overview.md)

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
bin/tdc
```

Build the independently deployed telemetry ingestion service:

```bash
make build-telemetry-backend
```

The backend binary is written to `bin/tdc-telemetry-backend`. Its API, privacy contract, TiDB/PostHog batching behavior, and Docker deployment are documented in [Telemetry Backend Design](docs/telemetry-backend-design.md).

## Test

Run local unit and black-box tests without live cloud credentials:

```bash
make test
make e2e
```

Run one live command family against the `live-e2e` profile:

```bash
make live-e2e-configure
make live-e2e-organization
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
