# Runtime Exec

## Verification Status

Implementation and local regression coverage are complete. Release validation
is pending: record the distributed companion version and checksums, and pass
`make live-e2e-runtime` and the aggregate `make live-e2e` with that artifact.
Fake-companion tests and skipped live tests do not satisfy this gate. Keep this
spec outside `done/` until the release evidence is recorded.

## Goal

Expose Drive9 Runtime through one concise top-level command:

```text
ti exec [flags] [--] command [arg...]
ti exec [flags] 'shell command line'
```

The public interaction must match `drive9 exec` from
`mem9-ai/drive9#1022` at exact head
`ce0bb29bf1ad24e0e8e1e0107c138061fdea42b6`. `ti` continues to own
profile, placement, File System selection, credential isolation, companion
discovery, telemetry, and operation-log privacy. The bundled `ti-drive9`
companion owns Runtime request, streaming, local-mount synchronization,
cancellation, sandbox reuse, and outcome classification.

This is an intentional exception to the usual noun-first top-level command
and long-only flag rules. Adding a `runtime` parent would make the common path
less direct, while changing the established Runtime flag names would create
two incompatible interfaces for the same operation. The exception applies
only to `ti exec`.

## Command And Flags

`ti exec` accepts the same Runtime flags as the pinned Drive9 contract:

| Flag | Meaning |
| --- | --- |
| `-w`, `--workspace PATH` | Filesystem directory to run in; infer an enclosing matching local FUSE mount when omitted, otherwise use the drive root |
| `-C`, `--cwd DIR` | Subdirectory of the selected workspace |
| `-e`, `--env KEY[=VALUE]` | Set a remote variable; a bare key copies the local value; repeatable |
| `-t`, `--timeout DURATION` | Caller deadline; the server may impose a shorter limit |
| `--fresh` | Request a new sandbox instead of warm reuse |
| `--layer REF` | Run against a LayerFS layer ID, name, or tag reference |
| `--read-only` | Mount the workspace read-only |
| `-v`, `--verbose` | Show sandbox, working directory, and duration details |

It also accepts the ti-owned long flags `--file-system-id` and `--fs-token`,
plus the existing global `--profile`, `--region`, and `--debug` flags. Prefer
`TI_FS_TOKEN` over `--fs-token` so secrets do not enter process arguments.

Flag parsing stops at the first command argument. Therefore this runs the
remote command with `-v` as its argument rather than enabling ti verbosity:

```bash
ti exec npm test -- -v
```

An explicit `--` remains supported. A single command argument containing
spaces or shell syntax is passed unchanged to Drive9, which runs it through
`/bin/sh -c`; multiple arguments are executed directly.

`--output`, `--query`, and `--dry-run` are not supported. Runtime output is a
live byte stream, not a structured ti result. `ti exec help` and `ti exec
--help` show command help without loading profiles, credentials, settings
beyond the normal help exclusions, or starting the companion. The bare `help` alias applies only without exec
flags or `--`; `ti exec -w repo help` and `ti exec -- help` are remote commands.

## Resource And Credential Resolution

Before loading a profile or resolving any credential, run the local
`ti-drive9 exec --help` command with a sanitized environment that contains no
profile, endpoint, placement, or credential. Exit status zero is the command
capability contract; do not parse human help text. An older companion must
fail here before any workspace synchronization or remote request. Use a
disposable HOME, no stdin, no visible output, and a ten-second deadline.
Distinguish interruption, timeout, startup failure, and incompatibility.

After the local capability gate, resolve exactly one File System using the
existing data-plane contract:

1. explicit `--file-system-id` or `TI_FS_FILE_SYSTEM_ID`;
2. otherwise derive the ID from explicit `--fs-token` or `TI_FS_TOKEN`;
3. otherwise fail with `fs.missing_file_system_id` before endpoint resolution,
   credential-bearing companion startup, or a remote call.

Resolve the credential from explicit `--fs-token`, then `TI_FS_TOKEN`, then
the selected resource's local credential. Resolve placement from explicit
`--region`, `TI_REGION_CODE`, then the selected resource. Runtime requires a
Filesystem API-key credential. A delegated Vault token cannot run commands;
service authorization remains authoritative for a scoped API key.

Run the companion with the existing resource-scoped HOME:

```text
~/.ti/drive9-home/<profile-key>/<resource-key>
```

Strip inherited `DRIVE9_*`, TiDB Cloud keys, FS tokens, and Vault tokens, then
set only the resolved `DRIVE9_SERVER`, `DRIVE9_REGION_CODE`, and
`DRIVE9_API_KEY`. Do not expose the credential in argv, logs, telemetry,
errors, or command output.

The companion invocation is equivalent to:

```text
ti-drive9 exec <runtime flags> -- <command and arguments>
```

The wrapper must never invoke a native ti Runtime client, another transport,
or a retry fallback. An incompatible companion fails before execution with an
actionable reinstall/update error.

The installers and updater must apply the same successful-command gate before
replacing either installed binary. A release containing this command is not
deliverable until the official Drive9 artifacts contain #1022, their exact
version and checksums are recorded, and the checksum-verified artifact passes
`make live-e2e-runtime` against a Runtime-enabled File System. The required
publication order is Drive9 #1022 artifact, live Runtime validation, then the
ti release.

## Local Mount Behavior

Because the companion uses the same resource-scoped HOME and credential as ti
mount commands, Drive9 may infer an enclosing live FUSE mount owned by the
same server and account. It drains pending local writes before starting the
remote command and runs in the corresponding workspace directory. A layer
mount retains its resolved layer.

Drive9 refuses unverifiable, cross-account, stale, or non-FUSE mounts. In
particular, WebDAV is the default on macOS and Windows, so callers there must
usually pass `-w`. Explicit `-w` selects the remote directory and does not
upload local changes first. `-w` changes the working directory, not the
Filesystem isolation boundary. Only `/workspace` persists; `--fresh` replaces
the sandbox without erasing those files.

## Streams, Exit Status, And Cancellation

- Connect stdout and stderr directly to the companion. The upstream protocol
  has no remote stdin channel or interactive terminal support.
- Keep stdout and stderr separate and live; do not buffer, combine, decode,
  query, or re-render remote bytes.
- A completed remote command exits with its exact status. Do not add a
  `ti [ERROR]` line for a nonzero remote status.
- Timeout exits `124`, first-interrupt cancellation exits `130`, and unknown
  outcome exits `255`, using the companion's actionable stderr message.
- Missing/not-executable commands keep shell statuses `127`/`126`. Remote
  commands can return any status, including ti and Runtime error codes; a
  numeric status alone cannot distinguish their sources.
- A busy or not-enabled rejection is a known pre-start failure. Lost,
  malformed, oversized, contradictory, redirected, or interrupted streams
  after possible acceptance are `outcome_unknown` and exit `255`.
- Never retry or resubmit an execution. The command may already have run.
- The companion owns the two-stage interrupt protocol: the first Ctrl-C
  requests remote cancellation and waits for workspace drain; the second
  detaches. Isolate the companion process group and forward signals exactly
  once, including parent-only SIGINT and SIGTERM. A programmatic context
  cancellation forwards one interrupt. The ti parent must not kill the
  companion on the first interrupt.

## Privacy And Observability

Operation logs and telemetry may record only `ti exec`, explicitly used flag
names, region/provider, duration, stable error code, and final exit status.
They must never record command/argument values, environment values, workspace
or cwd values, File System IDs, layer references, execution IDs, stdout,
stderr, API keys, or raw companion errors. Debug output may state that the
Runtime companion is being started, but must omit all Runtime arguments.

## Package Boundaries

- `internal/cli` owns command help, ti flag parsing, resource/profile
  selection, raw-output incompatibility checks, and the explicit short-flag
  exception.
- `internal/runtimeexec` owns deterministic construction of the companion
  arguments and one non-retrying execution call.
- `internal/fswrap` owns companion location, isolated HOME/environment
  construction, direct stream wiring, and silent process-exit propagation.
- `internal/apperr` owns the silent-exit marker used only when another process
  already rendered the result.

No Runtime HTTP/SSE protocol implementation belongs in ti-cli.

## Tests

Unit and compiled black-box tests must prove:

- exact `ti exec` help and all short/long Runtime flags;
- normal argv, a single shell line, `--`, and flags after the command;
- repeatable environment flags and ti-owned selection flags;
- stdout/stderr remain separate and unmodified;
- remote nonzero, `124`, `130`, and `255` exit statuses are passed through
  without an added `ti [ERROR]` line;
- one and only one execution attempt occurs, including stream failure;
- the local capability gate runs before profile/credential resolution, carries
  no endpoint or credential, and rejects a companion whose `exec --help`
  command exits nonzero without parsing its output;
- missing selection and incompatible companion fail before remote execution;
- resource-scoped HOME and sanitized credential environment are used;
- `--output`, `--query`, and `--dry-run` fail locally;
- short flags remain rejected everywhere except `ti exec`;
- command values, environment values, IDs, paths, and output do not enter
  telemetry, operation logs, or debug diagnostics.

Add an opt-in `live-e2e-runtime` target that executes a harmless command
against a Runtime-enabled real File System, verifies separate stdout/stderr
and a nonzero exit, and never deletes a resource it did not create. A skipped
or disabled Runtime test is not evidence that the live path works.

## Out Of Scope

- Runtime provisioning, provider/profile selection, sandbox listing, manual
  cancellation by execution ID, logs replay, or recovery commands;
- a `ti runtime` command family;
- a native Runtime SDK or HTTP client in ti-cli;
- retry, local execution, FUSE execution, or upload/download fallback;
- changing Drive9 Runtime server semantics or enabling Runtime in production.
