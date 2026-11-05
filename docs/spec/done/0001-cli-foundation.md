# CLI Foundation

> **Runtime exception:** [`0036-runtime-exec.md`](../0036-runtime-exec.md) adds top-level `ti exec` and
> its exact `-w`, `-C`, `-e`, `-t`, and `-v` Drive9 compatibility flags. The
> noun-first and long-only rules below remain authoritative for every other
> command.

## Goal

Establish the tdc CLI executable, command tree, global flags, help behavior,
version behavior, and process-level contracts that every later feature depends
on.

## User-facing Commands

- `tdc help`
- `tdc --version`
- `tdc <command> --version`
- `tdc <command> <subcommand> --version`
- `tdc <command> help`
- `tdc <command> <subcommand> help`
- `tdc configure`
- `tdc update`
- `tdc db <subcommand>`
- `tdc fs <subcommand>`
- `tdc organization <subcommand>`

## Behavior

- Implement the executable as `tdc`.
- Enforce a maximum two-level command tree: `tdc <command> [subcommand]`.
- Treat `tdc configure` and `tdc update` as the only top-level verb exceptions.
  `tdc configure` is the only interactive command.
- Use nouns for every other top-level command.
- Use long flags only. Do not add short flags or aliases.
- Support global flags everywhere they make sense:
  - `--profile`
  - `--debug`
  - `--version`
  - `--output`
  - `--query`
- Make `--version` valid at root, command, and subcommand levels. The value may
  include component-specific version details when a component has them.
- Unknown commands and flags must return non-zero exit codes with actionable
  errors.
- Running `tdc` without a command returns the `cli.missing_command` usage error
  with exit code `2` and a compact two-level command synopsis. Users can run
  `tdc help` for the complete command and flag listing.
- Help text must be deterministic and suitable for agents to parse.
- Flags and Global Flags sections render value types as `<type>` and append
  `(required)` after flags declared required by the command contract.

## Inputs And Config

- No config file is required to display help or version.
- Command execution should accept context cancellation from the process signal
  path once signal handling exists.

## Output And Errors

- Help and version output may be human-readable text.
- Command errors must be rendered by the CLI boundary as:

```text
tdc [ERROR]: <actionable message>
```

- Library code must return errors and must not call `os.Exit`.

## After This Spec

Users and agents can discover the product surface without configuring
credentials:

```bash
tdc help
tdc db help
tdc fs help
tdc --version
tdc fs mount-file-system --version
```

This step adds only the executable shell, command routing, help/version behavior,
and global flag parsing. Service commands may return "not implemented" until
their own specs are completed.

## Implementation Design

- `cmd/tdc` contains `main`, signal-aware context setup, and final exit-code
  handling.
- `internal/cli` owns root command construction, global flags, help wiring, and
  command registration.
- `internal/version` exposes build version, commit, date, and component version
  data used by root/command/subcommand `--version`.
- `internal/apperr` defines typed CLI errors with code, category, exit code, and
  user-facing message. Rendering still happens only at the CLI boundary.
- Service packages must register commands through small `NewCommand(...)`
  functions so command construction stays testable.

## API Call Chain

No remote API is called by this spec. Help, version, command parsing, and error
rendering are local-only.

## Dependencies And Platform

- Add `github.com/spf13/cobra` for command routing and help generation.
- Cobra pulls `github.com/spf13/pflag`; use long flags only and disable short
  aliases in project code.
- No cgo is required.
- The foundation must build on macOS, Linux, and Windows before platform-specific
  mount code is introduced.

## Dependencies

- `go.mod` with module `github.com/tidbcloud/tdc`.
- `docs/priciples.md` for product rules.

## Acceptance Criteria

- `go test ./...` passes without live cloud credentials.
- `tdc help`, root/command/subcommand `--version`, and nested help commands
  work.
- No command exposes short flags.
- Tests cover missing and unknown command handling and help availability.

## Out Of Scope

- Concrete API calls.
- Local config file persistence.
- Telemetry sending.
