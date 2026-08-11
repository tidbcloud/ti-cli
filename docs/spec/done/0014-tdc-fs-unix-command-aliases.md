# tdc fs Unix Command Aliases

> **Latest identity update:** aliases use the ID/token selection contract in `0028-remote-fs-resource-inventory.md`; `--file-system-name` is no longer available.

## Goal

Add Unix-style aliases for common `tdc fs` file and mount operations while keeping the existing long, AWS-style command names as the canonical interface. The aliases make tdc easier for Linux and Drive9 users without changing flag names, output contracts, permissions, or implementation paths.

## Reference Basis

- Drive9 uses Unix-style filesystem commands such as `cp`, `cat`, `ls`, `stat`, `mv`, `rm`, `mkdir`, `chmod`, `symlink`, `hardlink`, `grep`, and `find`.
- GNU Coreutils groups familiar commands around file output, directory listing, basic file operations, special file types, file attributes, and file status: `cat`, `ls`, `cp`, `mv`, `rm`, `mkdir`, `ln`, `chmod`, and `stat`.
- tdc keeps the product rule that all parameters remain long flags. The aliases are command-name aliases only.

## User-facing Commands

The existing long commands remain valid and canonical:

```bash
tdc fs copy-file --from-local ./notes.md --to-remote /notes.md --overwrite
tdc fs read-file --path /notes.md
tdc fs list-files --path /
tdc fs describe-file --path /notes.md
tdc fs move-file --from-remote /old.md --to-remote /new.md --overwrite
tdc fs delete-file --path /new.md --recursive
tdc fs create-directory --path /team/docs --mode 0755
tdc fs chmod-file --path /team/docs --mode 0755
tdc fs create-symlink --target ../docs --link-path /team/current
tdc fs create-hardlink --source-path /team/docs/a.md --link-path /team/docs/b.md
tdc fs search-file-content --pattern "todo" --path /
tdc fs find-files --file-name-pattern "*.md" --path /
tdc fs mount-file-system --mount-path ./mnt --remote-path /
tdc fs drain-file-system --mount-path ./mnt
tdc fs unmount-file-system --mount-path ./mnt
```

Add these aliases:

| Canonical command | Alias | Notes |
| --- | --- | --- |
| `tdc fs copy-file` | `tdc fs cp` | Copy between local storage, tdc fs, stdin, and stdout through the existing long flags. |
| `tdc fs read-file` | `tdc fs cat` | Stream a remote file to stdout. |
| `tdc fs list-files` | `tdc fs ls` | List a remote directory. |
| `tdc fs describe-file` | `tdc fs stat` | Return remote file or directory metadata. |
| `tdc fs move-file` | `tdc fs mv` | Rename or move a remote path. |
| `tdc fs delete-file` | `tdc fs rm` | Delete a remote file or directory tree. |
| `tdc fs create-directory` | `tdc fs mkdir` | Create a remote directory. |
| `tdc fs chmod-file` | `tdc fs chmod` | Change remote file or directory permissions. |
| `tdc fs create-symlink` | `tdc fs symlink` | Create a symbolic link with explicit long flags. |
| `tdc fs create-hardlink` | `tdc fs hardlink` | Create a hard link with explicit long flags. |
| `tdc fs search-file-content` | `tdc fs grep` | Search remote file contents. |
| `tdc fs find-files` | `tdc fs find` | Find remote files by metadata filters. |
| `tdc fs mount-file-system` | `tdc fs mount` | Mount a tdc fs resource locally. |
| `tdc fs drain-file-system` | `tdc fs drain` | Drain a mounted filesystem runtime. |
| `tdc fs unmount-file-system` | `tdc fs umount` | Unmount a local tdc fs mount. |

Example alias usage:

```bash
tdc fs cp --from-local ./notes.md --to-remote /notes.md --overwrite
tdc fs cat --path /notes.md
tdc fs ls --path /
tdc fs stat --path /notes.md --output text
tdc fs mv --from-remote /notes.md --to-remote /archive/notes.md
tdc fs rm --path /archive/notes.md --recursive
tdc fs mkdir --path /team/docs
tdc fs grep --pattern "todo" --path /
tdc fs find --file-name-pattern "*.md" --path /
tdc fs mount --mount-path ./mnt --remote-path /
tdc fs umount --mount-path ./mnt
```

## Behavior

- Aliases invoke the exact same command handlers as their canonical commands.
- The canonical long command names remain the names used in specs, README command reference tables, API permission mappings, and internal service methods.
- Alias help works: `tdc fs cp help`, `tdc fs cat help`, and `tdc fs mount help`.
- Canonical help works unchanged: `tdc fs copy-file help`, `tdc fs read-file help`, and `tdc fs mount-file-system help`.
- Command help should show aliases where Cobra exposes them, but aliases must not replace canonical commands in the command tree.
- Flags remain identical and long-only. Do not add `-r`, `-l`, `-s`, positional Linux-style syntax, or other short forms in this spec.
- Output modes, `--query`, `--dry-run`, error categories, auth, authorization, and exit codes are identical between an alias and its canonical command.
- Structured dry-run envelopes and error fields may report the canonical command path. Do not create separate permission names or telemetry command names for aliases.
- Unknown aliases fail as normal unknown commands. Do not add broad command guessing or typo correction.

## Inputs And Config

No config or credentials format changes are required. Aliases use the same global `--profile`, `~/.tdc/config`, `~/.tdc/credentials`, `~/.tdc/db_users/<cluster-id>/credentials`, tdc fs resource config and credentials, and command-specific long flags.

## Output And Errors

Successful alias output must match canonical output for the same flags.

```bash
tdc fs ls --path / --output json
tdc fs list-files --path / --output json
```

These must return the same JSON shape. Usage errors should remain actionable and may name the canonical command:

```text
tdc [ERROR]: --path is required
```

or:

```text
tdc [ERROR]: invalid flag for tdc fs copy-file: ...
```

Do not introduce alias-specific output schemas.

## After This Spec

Users can choose either style:

```bash
tdc fs copy-file --from-local ./a.txt --to-remote /a.txt
tdc fs cp --from-local ./a.txt --to-remote /a.txt
```

Agents and scripts can keep using long canonical names for maximum explicitness, while humans familiar with Linux and Drive9 can use shorter command names without learning a separate parameter model.

## Implementation Design

- Extend `internal/cli.controlPlaneCommandSpec` with an `Aliases []string` field and pass it into `newCommand`.
- Assign aliases in the existing `newFS*Command` builders instead of registering duplicate commands.
- Keep each alias on the same Cobra command object so flag definitions, help, output rendering, dry-run behavior, and command execution remain shared.
- Ensure `authz.ForCommand` continues to resolve permissions from canonical command paths. If Cobra reports alias paths in a future version, normalize aliases before permission lookup.
- Keep `controlPlaneCommandSpec.Permission` unchanged for each canonical command.
- Update README and AGENTS when the aliases are implemented.
- Add tests in `internal/cli` or e2e to verify aliases are registered on the expected canonical commands and expose the same long flags.

## API Call Chain

This spec adds no new remote API calls. Aliases dispatch to existing service methods:

| Alias | Existing service method |
| --- | --- |
| `cp` | `internal/fs.Service.CopyFile`, or `ReadFile` for `--to-stdout` |
| `cat` | `internal/fs.Service.ReadFile` |
| `ls` | `internal/fs.Service.ListFiles` |
| `stat` | `internal/fs.Service.DescribeFile` |
| `mv` | `internal/fs.Service.MoveFile` |
| `rm` | `internal/fs.Service.DeleteFile` |
| `mkdir` | `internal/fs.Service.CreateDirectory` |
| `chmod` | `internal/fs.Service.ChmodFile` |
| `symlink` | `internal/fs.Service.SymlinkFile` |
| `hardlink` | `internal/fs.Service.HardlinkFile` |
| `grep` | `internal/fs.Service.SearchFileContent` |
| `find` | `internal/fs.Service.FindFiles` |
| `mount` | `internal/fs.Service.MountFileSystem` |
| `drain` | `internal/fs.Service.DrainFileSystem` |
| `umount` | `internal/fs.Service.UnmountFileSystem` |

Remote HTTP paths and request bodies stay exactly as defined by earlier tdc fs specs.

## Dependencies And Platform

- Depends on `0001-cli-foundation.md`.
- Depends on implemented tdc fs commands from `0009`, `0010`, and `0011`.
- No new third-party Go dependency.
- No new cgo requirement.
- Platform-neutral for command parsing. Mount aliases use the existing platform-specific mount implementation.

## Acceptance Criteria

- `tdc fs <alias> help` works for every alias in this spec.
- `tdc fs <canonical> help` continues to work for every canonical command.
- `make test` verifies each alias maps to the intended canonical command.
- `make e2e` verifies alias parsing and help against the compiled binary.
- `make live-e2e` exercises the data-plane aliases against the `live-e2e` tdc fs resource without deleting unrelated data.
- Alias invocations support the same `--output`, `--query`, `--dry-run`, and command-specific long flags as canonical invocations where those flags apply.
- No short flags are added.
- README documents aliases after implementation.

## Out Of Scope

- Linux-style positional syntax such as `tdc fs cp ./a.txt :/a.txt`.
- Short flags such as `-r`, `-l`, `-s`, `-p`, or `-o`.
- A generic `tdc fs ln` command. `symlink` and `hardlink` are explicit aliases and avoid `ln` mode ambiguity.
- Aliases for tdc fs control-plane resource commands: `create-file-system`, `delete-file-system`, and `check-file-system`.
- Aliases for layer, pack, unpack, journal, vault, DB, organization, or CLI update commands.
- Top-level `tdc mount` or `tdc umount` commands.
