# Homebrew And Scoop Distribution

## Goal

Add optional package-manager distribution after the GitHub Releases installer workflow has proven stable. This spec covers Homebrew tap and Scoop bucket only. apt, yum, winget, Chocolatey, Mac App Store, Microsoft Store, notarization, and OS-specific repository review processes are out of scope.

## User-facing Commands

Homebrew:

```bash
brew install tidbcloud/tap/tdc
brew upgrade tidbcloud/tap/tdc
```

Scoop:

```powershell
scoop bucket add icemap https://github.com/tidbcloud/scoop-bucket
scoop install tdc
scoop update tdc
```

Existing `tdc` commands keep the same behavior:

```bash
tdc update --check
tdc update --dry-run
tdc update
```

For Homebrew and Scoop installs, `tdc update` must not replace the binary. It returns `update.managed_install` with the correct package-manager command.

## Behavior

- Homebrew distribution uses a separate tap repository, expected name `github.com/tidbcloud/homebrew-tap`.
- Scoop distribution uses a separate bucket repository, expected name `github.com/tidbcloud/scoop-bucket`.
- The main `github.com/tidbcloud/tdc` release remains the source of binary artifacts and checksums.
- GoReleaser updates the Homebrew formula and Scoop manifest as part of release publishing after this spec is implemented.
- The Homebrew formula and Scoop manifest consume GitHub Releases assets produced by `0012-install-and-update-distribution.md`.
- Package-manager installs embed `install_source=homebrew` or `install_source=scoop` through package build/wrapper metadata when practical.
- `tdc update` also detects common Homebrew and Scoop install paths as a fallback, even if build metadata is missing.
- Users can still run `tdc update --check` from Homebrew/Scoop installs; it reports release availability but does not mutate package-managed files.

## Inputs And Config

Repository secrets needed in the main repo:

- `GH_PAT` or a similarly named GitHub token with write access to `tidbcloud/homebrew-tap` and `tidbcloud/scoop-bucket`, if those repositories are separate from the main repo.

GoReleaser config additions:

- Homebrew publisher pointing at `tidbcloud/homebrew-tap`.
- Scoop publisher pointing at `tidbcloud/scoop-bucket`.
- Formula/manifest URLs pointing to GitHub Releases assets.
- SHA-256 values generated from the release artifacts.

No user `~/.tdc/` config or credentials are required for package-manager installation. Package-manager metadata must not store TiDB Cloud API keys, DB credentials, fs API keys, SQL text, file paths, or telemetry identifiers.

## Output And Errors

`tdc update` from a Homebrew install:

```text
tdc [ERROR]: tdc is managed by homebrew; update it with `brew upgrade tidbcloud/tap/tdc`
```

`tdc update` from a Scoop install:

```text
tdc [ERROR]: tdc is managed by scoop; update it with `scoop update tdc`
```

The structured error code remains `update.managed_install`.

## After This Spec

Users can choose between direct GitHub Releases installers and package-manager installs:

```bash
curl -fsSL https://github.com/tidbcloud/tdc/releases/latest/download/install.sh | sh -s -- --yes
export PATH="$HOME/.tdc/bin:$PATH"
brew install tidbcloud/tap/tdc
```

Windows users can choose between the PowerShell installer and Scoop:

```powershell
iwr https://github.com/tidbcloud/tdc/releases/latest/download/install.ps1 -OutFile $env:TEMP\install-tdc.ps1
powershell -ExecutionPolicy Bypass -File $env:TEMP\install-tdc.ps1 -Yes
$env:Path = "$HOME\.tdc\bin;$env:Path"
scoop bucket add icemap https://github.com/tidbcloud/scoop-bucket
scoop install tdc
```

Package-manager users update through the package manager, not `tdc update`.

## Implementation Design

- Extend `.goreleaser.yaml` with Homebrew tap publishing and Scoop bucket publishing.
- Keep GitHub Releases artifacts from `0012` unchanged so install scripts and package managers use the same assets.
- Add release workflow secret usage for the cross-repository publishing token.
- Add README installation sections for Homebrew and Scoop.
- Add e2e/unit coverage for `install_source=homebrew`, `install_source=scoop`, and known path detection.
- Keep `tdc update` refusal logic in `internal/update`; do not add package-manager-specific update code outside that package.

Homebrew tap repository work:

- Create `github.com/tidbcloud/homebrew-tap`.
- Let GoReleaser write a formula such as `Formula/tdc.rb`.
- Formula installs `tdc` from GitHub Releases.
- Formula test should run `tdc --version`.

Scoop bucket repository work:

- Create `github.com/tidbcloud/scoop-bucket`.
- Let GoReleaser write `bucket/tdc.json`.
- Manifest installs `tdc.exe` from GitHub Releases.
- Manifest checkver/autoupdate should track GitHub Releases tags.

## API Call Chain

This spec adds no TiDB Cloud product API calls.

Release publishing call chain:

1. GoReleaser builds the same archives and checksum file defined in `0012`.
2. GoReleaser publishes GitHub Releases assets.
3. GoReleaser commits or updates the Homebrew formula in `tidbcloud/homebrew-tap`.
4. GoReleaser commits or updates the Scoop manifest in `tidbcloud/scoop-bucket`.

Runtime update call chain from package-managed installs:

1. `tdc update --check` reads GitHub Releases metadata normally.
2. `tdc update` reads local install-source metadata and known install path patterns.
3. `tdc update` returns `update.managed_install` before downloading or replacing anything.

## Dependencies And Platform

- Depends on GoReleaser support for Homebrew and Scoop publishing.
- Requires separate GitHub repositories or an agreed monorepo layout for tap/bucket files.
- Requires a GitHub token with write access to the tap and bucket repositories.
- No new runtime dependency is expected.
- No cgo requirement is introduced.

## Dependencies

- `0012-install-and-update-distribution.md`
- `0013-github-actions-ci-cd.md` if package-manager publishing is automated through the same release workflow hardening.

## Acceptance Criteria

- `brew install tidbcloud/tap/tdc` installs a released `tdc` binary on macOS.
- `brew upgrade tidbcloud/tap/tdc` upgrades to a newer release.
- `scoop bucket add icemap https://github.com/tidbcloud/scoop-bucket` and `scoop install tdc` install `tdc.exe` on Windows.
- `scoop update tdc` upgrades to a newer release.
- `tdc update --check` works from Homebrew and Scoop installs.
- `tdc update` refuses Homebrew and Scoop installs with `update.managed_install`.
- README documents Homebrew and Scoop as optional package-manager channels.

## Out Of Scope

- apt, yum, dnf, apk, pacman, winget, Chocolatey, Mac App Store, Microsoft Store, Snap, Flatpak, and other package registries.
- Package-manager install telemetry.
- Auto-update daemons or background update checks.
- Changing the `~/.tdc/` config and credentials model.
