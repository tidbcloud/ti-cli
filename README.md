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

The macOS/Linux installer downloads both binaries and their checksum files from CDN-fronted sources and verifies the checksums before installation.

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

- Authentication: a TiDB Cloud Public Key and a Private Key from the [TiDB Cloud API Keys](https://tidbcloud.com/org-settings/api-keys) console. When either credential is missing, `ti` links to this page in its authentication error before suggesting `ti configure` or the corresponding environment variables.
- Default region: a canonical region code such as aws-us-east-1 or aws-ap-southeast-1.
    - Regions supporting TiDB Cloud Filesystem are published by TiDB Cloud Filesystem, not compiled into `ti`. List them with `ti fs list-regions`; creating and importing a File System records its regional `service_address` locally, and data-plane commands route from that address without any catalog request.
    - Regions supporting TiDB Cloud Starter are aws-us-east-1, aws-us-west-2, aws-eu-central-1, aws-ap-northeast-1, aws-ap-southeast-1, and alicloud-ap-southeast-1. `gcp-us-east1` and `azure-centralus` are Filesystem-only placements, so `ti db` commands reject them during local placement validation.

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

During the v0.2 migration window, the ingestion backend accepts both current `ti/<version>` and legacy `tdc/<version>` User-Agent values. All other User-Agent prefixes are rejected before payload validation.

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

Mount commands start the companion runtime in the background, wait until the mount is ready, and then return a structured result. Use `ti fs unmount-file-system` or `ti fs-vault unmount-vault` to end a mount. The public CLI does not expose a foreground mount mode.

FUSE mounts choose their write durability with `--durability`: `auto` lets the companion measure round-trip time, `interactive` (the default) keeps writes durable in the local staging journal and uploads them after flush, `fsync` makes `fsync(2)` wait for the remote commit, `close-sync` makes `close(2)` remote-durable, and `write-sync` makes every write remote-durable before it returns. Use `close-sync` or `write-sync` when a caller must not lose an acknowledged write in the client; the interactive default leaves that window to the background uploader. The mount result reports the effective tier as `durability`. WebDAV mounts keep the engine's native write behavior: ti forwards no tier, reports `auto`, and rejects every other explicit tier. `--write-back-cache` remains as a legacy alias where `true` means `interactive` and `false` means `auto`. Checkpoint mounts are read-only, reject any tier other than `auto`, and report `auto`.

```shell
ti fs mount-file-system --file-system-id <file-system-id> --mount-path ~/my-workspace --driver fuse --durability close-sync
ti fs drain-file-system --mount-path ~/my-workspace
ti fs unmount-file-system --mount-path ~/my-workspace
```

`ti fs drain-file-system` reports the companion's drain result, including the pending counters (`open_handles`, `commit_queue_*`, `uploader_*`), so a caller can tell "fully uploaded" from "the background upload never finished"; it also refuses WebDAV and vault mounts, which have no drain, instead of failing with a raw companion error. `ti fs unmount-file-system` runs that drain itself before detaching a FUSE file-system mount and fails with `fs.drain_pending_work` while writes are still pending, keeping the mount locator so the command can be retried. `--force` is the only way to accept the undrained writes (the result then records `drain.status: "skipped"`); `--ignore-absent` stays a cleanup idempotency flag and continues only past a drain failure that reports no pending work - no report at all, or a zero-counter report with a non-pending classification (`drain.status: "failed"`) - never past pending work. WebDAV mounts keep their native write behavior and are not gated, and `ti fs-vault unmount-vault` is not gated either because the engine drains only file-system mounts.

**Copy semantics (`ti fs copy-file`, `ti fs cp`).** The command names exactly one source and one destination: naming two destinations is rejected with `fs.ambiguous_copy_target` (except `--to-stdout` without `--from-remote`, which the earlier flag check answers with `fs.invalid_copy_flags`), and any other incomplete or doubled pair with `fs.invalid_copy_flags`. Unless `--overwrite` is explicit, ti probes the resolved destination before it invokes the companion and fails with `fs.target_exists` naming the destination that already exists. What replaces it depends on the direction: an upload writes the remote path and a download creates the local file, so with `--overwrite` ti simply skips its own probe and the companion's plain write replaces the destination (ti forwards no overwrite flag; the companion's `drive9 fs cp` has `-f|--force`, honored for object-store URIs only); a server-side copy (`--from-remote` with `--to-remote`) cannot do that, because the service inserts a new node at the destination and rejects the duplicate path, so ti performs the replacement itself. Without `--overwrite` the copy is refused with `fs.target_exists` naming the resolved destination (and saying so when the destination is the source path itself, which a directory destination can resolve to). With `--overwrite`, ti copies the source to a sibling `<destination>.ti-tmp-<8 hex>` path and then moves it onto the destination, so the rename replaces the target in one transaction: a reader sees either the old file or the complete new one, and a failed copy leaves the destination untouched. That staging path is briefly visible in a listing, and it survives when the replace fails and the token cannot delete it: the error then names the staging path and the `ti fs delete-file --path` that removes it - a command that itself needs delete on that path, so the same grant has to be widened before a leftover can be cleaned up; the `.ti-tmp-` suffix is how a leftover is found later. When the rename's answer is lost and the staging file is already gone, ti reports `fs.copy_outcome_unknown` and points at `ti fs describe-file` for both the destination and the source (a server-side copy shares the source file id), plus re-running with `--overwrite`, rather than at a delete for a path that no longer exists. The failure codes are `fs.copy_staging_left_behind` (ti saw the staging file and could not remove it, so the error names it), `fs.copy_outcome_unknown` (the staging file is gone, so the copy may have landed) and `fs.copy_staging_unchecked` (ti could not look and could not remove - a token that can neither read nor delete the staging sibling, a backend error, an expired 30s bound or a second interrupt - so the error says which `.ti-tmp-` entry to look for instead of claiming one). The token needs read on the source and on the destination, plus read, write and delete in the destination's directory: the staged copy reads its own sibling's metadata and the rename deletes the staging source, so a grant scoped to the destination file alone is refused at the staged copy, and a token without delete leaves the staging file behind. A destination that is itself a directory is refused with `fs.remote_copy_target_is_directory`. A local path spelled with a location scheme (`file:///path`, and any other scheme the companion knows, `s3://` included) or with the `:` prefix the companion reads as a drive9 path is refused with `fs.local_path_scheme_unsupported`, because the companion resolves that spelling while ti probes the literal string, which would let the copy write somewhere else than the probe checked. The probe covers local or stdin to remote, remote to remote, and remote to local, and resolves the destination the way the companion does: an existing directory, a trailing separator or the root receives `dir/<base(source)>`, and on a local destination so does a symlink that resolves to a directory (a remote destination has no symlink resolution - the service treats a link as a file whose payload is the target path, so a remote link destination is the link itself), so an upload or a download into a populated directory replaces only that inner path, and a server-side copy refuses when that inner path exists unless `--overwrite` stages the replacement, and a directory destination with a nameless source (stdin) is refused with `fs.copy_target_requires_name`. Four cases are exempt. A recursive copy, where the companion accepts an existing destination root but aborts before transferring anything if a descendant path already exists, and `--overwrite` has no effect. A layer destination, where `fs stat` cannot see inside the layer and the layer write is an upsert. `--append` and `--resume`, which do not replace a file so much as extend or continue it - on a download `--resume` is not a resume at all: the companion's download branch ignores it and creates the file, so it replaces an existing local file without `--overwrite` and reports `"status": "resumed"`.

Run a command against a remote Filesystem (Runtime is available only in AWS `us-east-1` and `us-west-2`):

```shell
ti exec --file-system-id <file-system-id> --read-only ls /workspace
ti exec --file-system-id <file-system-id> -w repo npm test
# With TI_FS_FILE_SYSTEM_ID or TI_FS_TOKEN already set:
ti exec -w repo 'npm ci && npm test'
ti exec -w repo -e CI=1 -e GITHUB_TOKEN -t 20m -- make -j8 test
```

The server chooses the backend. Supported commands run in the lightweight justbash executor; unsupported commands go to a sandbox before execution starts. Shell syntax alone does not determine the backend. Once a command starts, a failure does not cause it to run again on another backend.

The selected Filesystem is available at `/workspace`. Standard output and standard error stream live, and the remote command's exact exit status is preserved. Only files under `/workspace` persist; `--fresh` starts a new sandbox without erasing those files. Remote stdin and interactive terminals are not supported.

Select the Filesystem with `--file-system-id` / `TI_FS_FILE_SYSTEM_ID` or an explicit `--fs-token` / `TI_FS_TOKEN`, using the same profile and region resolution as other data-plane commands. `-w` selects a working directory within the Filesystem, not an isolation boundary. Inside a matching ti-created FUSE mount, omit `-w` to drain pending local writes and run in the corresponding directory. Explicit `-w` does not drain local writes. WebDAV mounts are not inferred; use `-w` there.

Execution flags are `-w/--workspace`, `-C/--cwd`, repeatable `-e/--env`, `-t/--timeout`, `--fresh`, `--layer`, `--read-only`, and `-v/--verbose`. Put them before the remote command. `ti exec help` and `ti exec --help` show local help; `ti exec -w repo help` and `ti exec -- help` run a remote command named `help`. Arguments after the remote command, including `--help`, are passed unchanged. `--output`, `--query`, and `--dry-run` are not supported.

The first Ctrl-C or SIGTERM requests cancellation and waits for workspace drain. A second interruption detaches; the remote command may still be running. Timeout uses status `124`, cancellation `130`, and an unknown outcome `255`. A remote command can also return any of these statuses, or ti's own error statuses such as `2`, `3`, `4`, and `5`; the exit code alone cannot identify the source. Executions are never retried. After an unknown outcome, inspect the workspace before deciding whether to run the command again.

Use `ti update` or reinstall ti if the local capability check reports an incompatible companion. Release validation is described in [Releasing](docs/releasing.md).

Mount readiness means the mounted path is readable: `ti fs mount-file-system` and `ti fs-vault mount-vault` keep polling the mount path until directory listing succeeds, bounded by `--ready-timeout` (default `30s`). If the mount path never becomes readable in time, the command fails with `fs.mount_ready_timeout`, the background mount is left running, and the mount locator is preserved so the matching unmount command can stop it.

When a mount unpacks a local overlay (auto-unpack with a non-`none` mount profile whose effective pack paths are non-empty — `portable` supplies `/` by default, and the companion's mount accepts no extra pack paths of its own — while `--no-auto-unpack` is not set, or an explicit `--unpack-archive-path`), it runs the same overlay symlink audit as `ti fs unpack-file-system`: ti resolves the local overlay before the companion runs, snapshots it, audits it after the mount becomes readable, and also when readiness times out, removes links that escape the overlay, stops the mount, and reports the error. The audited root is the companion's own default overlay root, so existing overlays keep their location; an explicit `--local-root` is passed through and audited as given. If the audit rejects the overlay or an unpack-capable mount never becomes ready, ti stops the mount instead of leaving it running (an exception to the usual timeout behavior, because unpack may still be completing). If the mount cannot be stopped, the error says so and the mount locator is kept so `ti fs unmount-file-system` can retry; before the locator exists there is nothing to keep, so the error instead names the companion's own `ti-drive9 umount` with the resource-scoped HOME. A mount that would unpack into an overlay ti cannot resolve is refused with `fs.mount_unpack_requires_profile`. A mount that does not unpack, including one that only names `--local-root`, is not audited; ordinary mounts keep their previous behavior. Links that land outside an audited run window are treated as pre-existing local state. The extra `--pack-path` values are recorded in the mount locator (together with the mount's local root) and replayed as repeated `drive9 umount --pack-path` when the mount is unmounted, because the companion's `mount` has no flag for them and `umount --pack-path` is what feeds the automatic pack; without the replay the mount succeeds while the archive silently omits those local overlay paths. They are overlay paths: a value that is absent from `<local-root>/overlay` at unmount is skipped with a check in the unmount output rather than failing the pack after the mount is already detached, and a WebDAV mount (which has no local overlay) refuses `--pack-path` at mount time. `--pack-archive-path` adds an archive instead of choosing the only one: without `--no-auto-pack` the companion packs both the profile's default archive and the named one; an explicit archive keeps the replay even together with `--no-auto-pack`, because the pack still runs; `--no-auto-pack` alone suppresses the replay along with the automatic pack. `--pack-path` is refused at mount time for a WebDAV mount and for a value the pack cannot accept (`\` or a `.`/`..` segment, and a relative value may not contain an empty segment; a blank value is dropped, and an absolute value may repeat slashes). Restoring the archive is still the mount profile's decision: only a profile that carries automatic unpack paths, such as `portable`, unpacks it at the next mount, a `coding-agent` mount never does, and `ti fs unpack-file-system` (or an explicit `--unpack-archive-path`) is how to restore it deliberately.

`ti fs-vault run-with-secret` reads the named secret through the companion and runs the command from ti itself. The child receives only the secret fields plus an environment with ti credentials removed: the filesystem bearer token, the vault token, the organization API key pair, and the filesystem AI provider key are never inherited. Field names are exported with the same environment-key conversion as `ti fs-vault read-secret --format env` (ASCII letters upper-cased, digits and `_` kept, a leading digit prefixed with `_`, and every byte outside `A-Z`, `a-z`, `0-9`, and `_` replaced with `_`), so accented characters produce one underscore per UTF-8 byte; two fields that convert to the same key fail with a usage error instead of overwriting each other. A field that converts to a process-configuration variable is rejected with `vault.env_key_forbidden` instead of injected, because whoever can write a secret's fields could otherwise choose what the child loads or runs; the error lists every rejected field and what it would have configured: `PATH`, `PATHEXT`, `COMSPEC`, `SHELL`, shell startup and evaluation names such as `ENV`, `BASH_ENV`, `SHELLOPTS`, `PS4`, and `IFS`, dynamic-loader names such as `LD_PRELOAD`, `LD_LIBRARY_PATH`, `DYLD_INSERT_LIBRARIES`, and `GCONV_PATH`, language-runtime names such as `PYTHONPATH`, `PERL5OPT`, `NODE_OPTIONS`, `RUBYOPT`, `JAVA_TOOL_OPTIONS`, and `LUA_INIT`, and helper-selection names such as `SSH_AUTH_SOCK`, `GIT_SSH_COMMAND`, `GIT_CONFIG_GLOBAL`, and `PAGER`. The check matches exact names after conversion, except that a few families whose suffix the field name chooses are matched by prefix (`DYLD_*`, `LD_32_*`, `LD_64_*`, `GIT_CONFIG_KEY_<n>`, `GIT_CONFIG_VALUE_<n>`), and that trailing all-digit version segments are stripped before the lookup, so versioned spellings such as `LUA_INIT_5_4`, `LUA_PATH_5_4`, `LUA_CPATH_5_4`, and `LD_PRELOAD_32` hit the same entries as their base names (Lua reads the versioned name first, and the Solaris loader reads the 32/64-bit one). Names that merely resemble a rejected one, such as `SSH_PRIVATE_KEY`, `GIT_TOKEN`, `LD_PRELOAD_BACKUP`, or `DB_URL_2`, remain injectable. `ti fs-vault read-secret --format env` still converts those names, because it only prints values and never spawns a child. On Windows, an inherited variable that differs from an injected field only by letter case is replaced, because the platform resolves environment names case-insensitively.

Filesystem layers can fork copy-on-write child timelines without copying a workspace. Layer and checkpoint mounts require FUSE; checkpoint mounts are always read-only. Drive9 does not support combining recursive copy with a layer, so seed a directory tree through a writable layer mount, drain it, and then create the checkpoint:

```shell
export TI_FS_FILE_SYSTEM_ID="<file-system-id>"
ti fs create-directory --path /research/q3-market
ti fs create-layer --base-root-path /research/q3-market --layer-name research-base
SEED_MOUNT="$(mktemp -d)"
ti fs mount-file-system --mount-path "$SEED_MOUNT" --remote-path /research/q3-market --driver fuse --layer-ref research-base
tar -C . -cf - . | tar -C "$SEED_MOUNT" -xf -
ti fs drain-file-system --mount-path "$SEED_MOUNT"
ti fs unmount-file-system --mount-path "$SEED_MOUNT"
rmdir "$SEED_MOUNT"
ti fs create-layer-checkpoint --layer-id research-base --checkpoint-id seed
ti fs fork-layer --parent-layer-ref research-base --layer-name experiment-a --checkpoint-id seed
ti fs list-layer-chain --layer-ref experiment-a --output text
```

Use `ti fs delete-layer --layer-ref experiment-a` to abandon a rejected leaf. Deleting a layer with live descendants fails unless `--cascade` is supplied explicitly. Layer names are not durable unique identifiers because abandoned layers remain visible; automation should capture returned layer IDs and use run-unique checkpoint IDs.

`ti fs list-regions` lists the Filesystem regions published by TiDB Cloud Filesystem, with their cloud provider, display name, state, and regional service address. It uses TiDB Cloud credentials, always asks the API, and refreshes the recorded `service_address` of locally stored File System credentials. The catalog request is Digest-authenticated with the organization API key pair, so `ti` follows a redirect only when it keeps the same scheme and host and otherwise fails with `api.fs_regions_redirect_refused`; the catalog endpoint itself must be `https` (plain `http` is accepted only for a loopback test server) and must not embed credentials, a query, or a fragment:

```shell
ti fs list-regions
ti fs list-regions --query 'regions[?eligible].region_code' --output text
```

`ti fs create-file-system` and `ti fs import-file-system-token` record the regional `service_address` with the File System credential, and data-plane commands route from that address without requesting the region catalog. A token-only machine works without TiDB Cloud credentials: import once (`ti fs import-file-system-token --from-file ./fs-token`) and `ti` derives the conventional `https://<region-id>.drive9.ai` address whenever the credential records none. With TiDB Cloud credentials the import also records the catalog-published address; without them it records none, so a later catalog change still applies.

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

An owner FS token authorizes Filesystem use and token management, but it is not a TiDB Cloud administrative credential. Creating, listing, describing, and deleting Filesystem resources require TiDB Cloud API keys. In particular, `ti fs delete-file-system` always requires an explicit `--file-system-id` plus `--confirm-file-system-name` matching the target Filesystem display name; deletion is asynchronous and irreversible because the backend keeps no recovery period, and `TI_FS_TOKEN` cannot select or authorize deletion of the Filesystem itself. For token list, enable, disable, and delete commands, `--file-system-id` is required only when the command uses TiDB Cloud API keys. When an owner token is supplied through `--fs-token` or `TI_FS_TOKEN`, `ti` derives the Filesystem ID from that token.

Filesystem AI provider configuration is optional. Without it, existing create, mount, file, search, layer, vault, journal, and Git workflows continue to work normally. Organization administrators can inspect or customize image, audio, and video extraction and OpenAI-compatible 1024-dimension embedding for an explicit Filesystem ID:

```shell
ti fs describe-file-system-extract-configuration \
  --file-system-id "$FILE_SYSTEM_ID" \
  --media-type image

TI_FS_AI_PROVIDER_API_KEY="<provider-api-key>" \
ti fs update-file-system-extract-configuration \
  --file-system-id "$FILE_SYSTEM_ID" \
  --media-type image \
  --enabled true \
  --provider-api-base https://api.openai.com/v1 \
  --provider-model "<vision-model>"
```

Extraction supports the `openai` protocol for image, audio, and video. Audio also supports Alibaba Cloud Model Studio Qwen ASR through `--provider-protocol qwen-asr`. Exact OpenAI-compatible endpoints can work when they implement the required API contract; native Anthropic, Gemini, Vertex AI, Bedrock, and Azure OpenAI interfaces are not supported. Enabling or replacing a provider sends a real validation request that can incur a small provider charge. After enablement, Filesystem content is sent to the selected extraction provider, and text or descriptions are sent to the selected embedding provider. The provider key is accepted only through `TI_FS_AI_PROVIDER_API_KEY`, is not stored locally, and is returned by the backend only in masked form.

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
ti exec [flags] [--] command [arg...]

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
ti db list-export-tasks --db-cluster-id
ti db download-exported-data --db-cluster-id

ti fs create-file-system
ti fs import-file-system-token
ti fs generate-file-system-token
ti fs generate-file-system-scoped-token
ti fs list-file-system-tokens
ti fs enable-file-system-token
ti fs disable-file-system-token
ti fs delete-file-system-token
ti fs refresh-file-system-token
ti fs delete-file-system --file-system-id <file-system-id> --confirm-file-system-name <display-name>
ti fs list-file-systems
ti fs describe-file-system
ti fs describe-file-system-extract-configuration
ti fs update-file-system-extract-configuration
ti fs describe-file-system-embedding-configuration
ti fs update-file-system-embedding-configuration
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
ti fs fork-layer
ti fs list-layer-chain
ti fs describe-layer
ti fs diff-layer
ti fs create-layer-checkpoint
ti fs delete-layer
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
- [Release Guide](docs/releasing.md)

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

The backend binary is written to `bin/ti-telemetry-backend`. Its API, privacy contract, TiDB batching behavior, and Docker deployment are documented in [Telemetry Backend Design](docs/telemetry-backend-design.md).

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
make live-e2e-runtime
```

Run the complete live suite in one test process:

```bash
make live-e2e
```

Set `LIVE_E2E_PROFILE=<profile>` to use a profile other than `live-e2e`. The DB and FS suites perform real cloud mutations and clean up only resources created by the test run. `make live-e2e-runtime` and the complete `make live-e2e` suite require Runtime to be enabled for the selected Filesystem; they create a uniquely named test directory, verify lightweight reads, sandbox execution, persistence, read-only protection, streams, arguments, environment variables, exact exit codes, timeout, and parent-only cancellation, then remove only that directory. Backend selection is observed from real response frames. A disabled Runtime or failed cancellation fails the suite; it is not proof of compatibility.
