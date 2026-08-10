# Homebrew And Scoop Distribution

## Goal

Add optional package-manager distribution after the GitHub Releases installer workflow has proven stable. This spec covers Homebrew tap and Scoop bucket only. apt, yum, winget, Chocolatey, Mac App Store, Microsoft Store, notarization, and OS-specific repository review processes are out of scope.

## User-facing Commands

Homebrew:

```bash
brew install tidbcloud/tap/ti-cli
brew upgrade tidbcloud/tap/ti-cli
```

Scoop:

```powershell
scoop bucket add tidbcloud https://github.com/tidbcloud/scoop-bucket
scoop install ti-cli
scoop update ti-cli
```

Existing `ti` commands keep the same behavior:

```bash
ti update --check
ti update --dry-run
ti update
```

For Homebrew and Scoop installs, `ti update` must not replace the binary. It returns `update.managed_install` with the correct package-manager command.

## Behavior

- Homebrew distribution uses a separate tap repository, expected name `github.com/tidbcloud/homebrew-tap`.
- Scoop distribution uses a separate bucket repository, expected name `github.com/tidbcloud/scoop-bucket`.
- The main `github.com/tidbcloud/ti-cli` release remains the source of binary artifacts and checksums.
- GoReleaser updates the Homebrew formula and Scoop manifest as part of release publishing after this spec is implemented.
- The Homebrew formula and Scoop manifest consume GitHub Releases assets produced by `0012-install-and-update-distribution.md`.
- Package-manager installs embed `install_source=homebrew` or `install_source=scoop` through package build/wrapper metadata when practical.
- `ti update` also detects common Homebrew and Scoop install paths as a fallback, even if build metadata is missing.
- Users can still run `ti update --check` from Homebrew/Scoop installs; it reports release availability but does not mutate package-managed files.

## Inputs And Config

Repository secrets needed in the main repo:

- `GH_PAT` or a similarly named GitHub token with write access to `tidbcloud/homebrew-tap` and `tidbcloud/scoop-bucket`, if those repositories are separate from the main repo.

GoReleaser config additions:

- Homebrew publisher pointing at `tidbcloud/homebrew-tap`.
- Scoop publisher pointing at `tidbcloud/scoop-bucket`.
- Formula/manifest URLs pointing to GitHub Releases assets.
- SHA-256 values generated from the release artifacts.

No user `~/.ti/` config or credentials are required for package-manager installation. Package-manager metadata must not store TiDB Cloud API keys, DB credentials, fs API keys, SQL text, file paths, or telemetry identifiers.

## Output And Errors

`ti update` from a Homebrew install:

```text
ti [ERROR]: ti is managed by homebrew; update it with `brew upgrade tidbcloud/tap/ti-cli`
```

`ti update` from a Scoop install:

```text
ti [ERROR]: ti is managed by scoop; update it with `scoop update ti-cli`
```

The structured error code remains `update.managed_install`.

## After This Spec

Users can choose between direct GitHub Releases installers and package-manager installs:

```bash
curl -fsSL https://github.com/tidbcloud/ti-cli/releases/latest/download/install.sh | sh -s -- --yes
export PATH="$HOME/.ti/bin:$PATH"
brew install tidbcloud/tap/ti-cli
```

Windows users can choose between the PowerShell installer and Scoop:

```powershell
iwr https://github.com/tidbcloud/ti-cli/releases/latest/download/install.ps1 -OutFile $env:TEMP\install-ti.ps1
powershell -ExecutionPolicy Bypass -File $env:TEMP\install-ti.ps1 -Yes
$env:Path = "$HOME\.ti\bin;$env:Path"
scoop bucket add tidbcloud https://github.com/tidbcloud/scoop-bucket
scoop install ti-cli
```

Package-manager users update through the package manager, not `ti update`.

## Implementation Design

- Extend `.goreleaser.yaml` with Homebrew tap publishing and Scoop bucket publishing.
- Keep GitHub Releases artifacts from `0012` unchanged so install scripts and package managers use the same assets.
- Add release workflow secret usage for the cross-repository publishing token.
- Add README installation sections for Homebrew and Scoop.
- Add e2e/unit coverage for `install_source=homebrew`, `install_source=scoop`, and known path detection.
- Keep `ti update` refusal logic in `internal/update`; do not add package-manager-specific update code outside that package.

Homebrew tap repository work:

- Create `github.com/tidbcloud/homebrew-tap`.
- Let GoReleaser write a formula such as `Formula/ti-cli.rb`.
- Formula installs `ti` from GitHub Releases.
- Formula test should run `ti --version`.

Scoop bucket repository work:

- Create `github.com/tidbcloud/scoop-bucket`.
- Let GoReleaser write `bucket/ti-cli.json`.
- Manifest installs `ti.exe` from GitHub Releases.
- Manifest checkver/autoupdate should track GitHub Releases tags.

## API Call Chain

This spec adds no TiDB Cloud product API calls.

Release publishing call chain:

1. GoReleaser builds the same archives and checksum file defined in `0012`.
2. GoReleaser publishes GitHub Releases assets.
3. GoReleaser commits or updates the Homebrew formula in `tidbcloud/homebrew-tap`.
4. GoReleaser commits or updates the Scoop manifest in `tidbcloud/scoop-bucket`.

Runtime update call chain from package-managed installs:

1. `ti update --check` reads GitHub Releases metadata normally.
2. `ti update` reads local install-source metadata and known install path patterns.
3. `ti update` returns `update.managed_install` before downloading or replacing anything.

## Dependencies And Platform

- Depends on GoReleaser support for Homebrew and Scoop publishing.
- Requires separate GitHub repositories or an agreed monorepo layout for tap/bucket files.
- Requires a GitHub token with write access to the tap and bucket repositories.
- No new runtime dependency is expected.
- No cgo requirement is introduced.

## Dependencies

- `0012-install-and-update-distribution.md`
- `0027-ti-cli-rename-and-migration.md`
- `0013-github-actions-ci-cd.md` if package-manager publishing is automated through the same release workflow hardening.

## Acceptance Criteria

- `brew install tidbcloud/tap/ti-cli` installs a released `ti` binary on macOS.
- `brew upgrade tidbcloud/tap/ti-cli` upgrades to a newer release.
- `scoop bucket add tidbcloud https://github.com/tidbcloud/scoop-bucket` and `scoop install ti-cli` install `ti.exe` on Windows.
- `scoop update ti-cli` upgrades to a newer release.
- `ti update --check` works from Homebrew and Scoop installs.
- `ti update` refuses Homebrew and Scoop installs with `update.managed_install`.
- README documents Homebrew and Scoop as optional package-manager channels.

## Out Of Scope

- apt, yum, dnf, apk, pacman, winget, Chocolatey, Mac App Store, Microsoft Store, Snap, Flatpak, and other package registries.
- Package-manager install telemetry.
- Auto-update daemons or background update checks.
- Changing the `~/.ti/` config and credentials model.
