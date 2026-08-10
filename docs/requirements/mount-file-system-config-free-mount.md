# `ti fs mount-file-system` Configuration\-Free Mount

## Problem

Today `ti fs mount-file-system` requires an initialized `~/.ti/credentials` file, which in turn requires `ti configure` with TiDB Cloud API public/private keys\. This makes the mount command unusable in ephemeral environments \(CI/CD pipelines, E2B sandboxes, Docker containers\) where provisioning long\-lived API credentials is undesirable\. Users want a "mount and go" experience with only the information intrinsic to the filesystem itself\.

## Solution

Make `ti fs mount-file-system --file-system-name my-workspace --mount-path ~/my-workspace` work without any prior `ti configure` by accepting three pieces of information directly:

1. **file\-system\-name** — identifies the target File System

2. **\-\-region\-code** flag or **TI\_REGION\_CODE** environment variable — specifies the deployment region

3. **\-\-fs\-token** flag or **TI\_FS\_TOKEN** environment variable — the filesystem\-scoped authentication token

With these three variables, the CLI has everything it needs to mount the filesystem — region for the API endpoint, FS identity, and a scoped credential — without a credentials file or API key pair\.

## CLI Interface

```bash
# Minimal mount — everything via flags
ti fs mount-file-system \
  --file-system-name my-workspace \
  --mount-path ~/my-workspace \
  --region aws-us-east-1 \
  --fs-token drive9_abc123...xyz

# Mount using env vars for region and token
export TI_REGION_CODE=aws-us-east-1
export TI_FS_TOKEN=drive9_abc123...xyz
ti fs mount-file-system \
  --file-system-name my-workspace \
  --mount-path ~/my-workspace

# Mount with mixed sources — pick what suits the environment
ti fs mount-file-system \
  --file-system-name my-workspace \
  --mount-path ~/my-workspace \
  --region aws-us-east-1
```

## Variable Precedence

When multiple sources provide the same variable, the priority is \(highest wins\):

1. CLI flag \(`--region`, `--fs-token`\)

2. Environment variable \(`TI_REGION_CODE`, `TI_FS_TOKEN`\)

3. `~/.ti/credentials` \(existing config, if any\)

This means existing configured setups continue to work unchanged — the new flags and env vars are purely additive\.

## Behavior

1. CLI resolves `region-code` and `fs-token` from flags → env vars → credentials file \(in order of precedence\)

2. If either `region-code` or `fs-token` is missing after resolution, CLI prints a clear error telling the user which variable is missing and the available sources

3. CLI connects to the Drive9 API endpoint using the region code and authenticates directly with the FS token

4. Mount proceeds as normal — FUSE mount \(Linux/macOS\) or WebDAV \(fallback\)

5. No `~/.ti/credentials` file is **pre\-required** before this mount command

## Use Cases

- **E2B sandboxes:** pass `TI_FS_TOKEN` and `TI_REGION_CODE` as sandbox environment variables — no config step needed

- **CI/CD pipelines:** inject token and region via CI secrets, mount in one command

- **Docker containers:** pass via `--env` flags, no volume\-mounting credentials

- **Quick trials:** a new user with only a token can mount immediately without API key setup