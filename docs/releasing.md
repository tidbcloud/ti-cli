# Releasing ti

Pushing a `v*` tag runs [the release workflow](../.github/workflows/release.yml). GoReleaser builds the CLI, creates archives and checksums, uploads both installers, and publishes the GitHub Release using `docs/release-notes/<tag>.md`. Merging a release PR alone does not publish a release.

The tag supplies the binary version through GoReleaser build flags. There is no source version constant or `go.mod` version to bump.

## When Drive9 publishes a new CLI

The ti archives contain `ti` and the README. The installers and updater download the `ti-drive9` companion separately, verify its SHA-256 checksum, and check required layer commands and mount flags before installation.

| Consumer | Companion binary source | Checksum file |
| --- | --- | --- |
| macOS/Linux installer | `https://releases.drive9.ai/latest/drive9-<os>-<arch>` | `https://releases.drive9.ai/latest/SHA256SUMS` |
| PowerShell installer | `https://drive9.ai/releases/drive9-windows-amd64.exe` | `https://drive9.ai/releases/checksums.txt` |
| `ti update` on macOS/Linux | `https://drive9.ai/releases/drive9-<os>-<arch>` | `https://drive9.ai/releases/checksums.txt` |

These are mutable endpoints. A ti tag does not pin a Drive9 build. Record the companion version and checksums verified during release preparation, and recheck them before publishing if the upstream release changes.

New installations already download the current companion. Existing users on the latest ti version cannot refresh only the companion with `ti update`: it stops when the ti version is unchanged, and `ti update --check` only compares ti versions. A patch release gives those users the normal update path. Alternatively, they can drain/unmount and rerun the installer at the same ti version, using `--yes` (or `-Yes` on Windows) and their existing installation directory.

Do not claim that a ti rollback also rolls back Drive9. Windows users must rerun the PowerShell installer; in-place self-update is unsupported there.

## Prepare the release PR

1. Check the latest published ti release and changes since that tag. Use the next patch version for a companion refresh with no ti interface change. Include only merged changes in the release notes.
2. Check the Drive9 `version` file and checksum manifest at each source above. Confirm that they agree and contain all five ti targets: Darwin and Linux on amd64/arm64, and Windows on amd64. Download and verify the companion on the validation host, then inspect `ti-drive9 version`, `ti-drive9 fs layer help`, and `ti-drive9 mount --help`. The required capabilities include layer `fork`, `chain`, `delete` and mount `--layer`, `--checkpoint`.
3. Add `docs/release-notes/<tag>.md`, following recent notes. For a companion refresh, identify the tested Drive9 build, link the relevant upstream fixes, explain the mutable download source, and include upgrade and reinstall commands. Pass the ti version explicitly to the installer: downloading an installer from a tagged URL alone still defaults to installing the latest ti release.
4. Validate the candidate with the repository commands:

   ```bash
   make test
   make e2e
   make release-snapshot
   ```

   `make release-snapshot` requires GoReleaser v2 and creates local artifacts under `dist/`; it does not publish. Inspect the five platform archives and `ti_checksums.txt`. Never commit `bin/` or `dist/`.

5. Before publishing, validate the real companion against cloud resources. Place the checksum-verified companion at `bin/ti-drive9` (`bin/ti-drive9.exe` on Windows) so the source-built `bin/ti` uses that binary. Configure the test profile and run the complete release suite:

   ```bash
   make live-e2e
   ```

   The default profile is `live-e2e`; use `LIVE_E2E_PROFILE=<profile>` to override it. These tests create and clean up temporary cloud resources. Check the output for skips caused by unavailable credentials, quota, or mount prerequisites. Ordinary unit/e2e tests use a fake companion and do not prove live Drive9 compatibility. Focused `make live-e2e-fs`, `make live-e2e-fs-git`, `make live-e2e-fs-journal`, and `make live-e2e-fs-vault` targets help investigate failures but do not replace the full release suite.
6. Open a release PR and record the tested companion version, checksums, successful checks, and any validation still required. Merge after review and required checks pass.

## Publish after merge

From a clean checkout, fetch the merged release commit and tag that exact commit. For v0.2.8:

```bash
git fetch origin --tags
git switch main
git pull --ff-only origin main
git status --short
git log -1 --oneline
git show HEAD:docs/release-notes/v0.2.8.md
```

Confirm that `HEAD` is the intended, validated release commit and the working tree is clean, then publish the tag:

```bash
git tag -a v0.2.8 -m "Release v0.2.8"
git push origin refs/tags/v0.2.8
gh run list --repo tidbcloud/ti-cli --workflow release.yml --limit 5
```

Watch the run for that tag with `gh run watch <run-id> --repo tidbcloud/ti-cli --exit-status`. The release workflow checks that the notes exist and runs GoReleaser; it does not run unit, e2e, or live tests. Complete those checks before pushing the tag. Do not move a published tag to fix a release.

## Verify the publication

```bash
gh release view v0.2.8 --repo tidbcloud/ti-cli
gh release view v0.2.8 --repo tidbcloud/ti-cli --json assets --jq '.assets[].name'
```

Expect five ti archives (`ti_darwin_amd64.tar.gz`, `ti_darwin_arm64.tar.gz`, `ti_linux_amd64.tar.gz`, `ti_linux_arm64.tar.gz`, `ti_windows_amd64.zip`), `ti_checksums.txt`, `install.sh`, and `install.ps1`. Check that the release notes match the committed file and the stable release is marked latest.

Use the new release notes' version-specific installer commands in a disposable environment, and exercise `ti update --target-version v0.2.8` from the previous release in a separate ti-owned installation. Verify both `ti --version` and the sibling `ti-drive9 version`; a successful ti version check alone does not identify the installed companion.
