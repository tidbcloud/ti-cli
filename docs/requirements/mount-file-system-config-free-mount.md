# `ti fs mount-file-system` Configuration-Free Mount

## Problem

Ephemeral environments such as CI jobs, E2B sandboxes, and containers should not require `ti configure`, TiDB Cloud API keys, or a persistent `~/.ti` profile merely to mount an existing file system.

## Solution

An FS token contains the Drive9 tenant ID that is the canonical `file_system_id`. A clean environment therefore needs only:

1. `--fs-token` or `TI_FS_TOKEN` for filesystem-scoped authentication and identity.
2. `--region` or `TI_REGION_CODE` for endpoint routing.
3. `--mount-path` for the local mount target.

`--file-system-id` and `TI_FS_FILE_SYSTEM_ID` remain optional. When supplied with a token, they are consistency assertions and must match the tenant ID embedded in that token.

## CLI Interface

```bash
# Minimal sandbox mount using environment variables.
export TI_REGION_CODE=aws-us-east-1
export TI_FS_TOKEN=drive9_abc123...xyz
ti fs mount-file-system --mount-path ~/my-workspace

# Supply all authentication and routing values as flags.
ti fs mount-file-system \
  --mount-path ~/my-workspace \
  --region aws-us-east-1 \
  --fs-token drive9_abc123...xyz

# Optionally assert the expected file system ID.
ti fs mount-file-system \
  --file-system-id tnt_abc123 \
  --mount-path ~/my-workspace \
  --region aws-us-east-1 \
  --fs-token drive9_abc123...xyz
```

## Variable Precedence

For each value independently, the priority is:

1. Explicit CLI flag.
2. Canonical environment variable.
3. Locally stored ID-keyed credential in the selected profile.
4. Profile region for endpoint routing.

Canonical and legacy environment variables with different values fail explicitly. Values from different precedence layers can be combined, so a flag may provide the ID while the environment provides the token and the profile provides the region.

## Behavior

1. Resolve the optional explicit ID, token, and region independently.
2. Parse the token and derive its tenant ID.
3. Reject an explicit ID that does not match the token.
4. Resolve the Drive9 endpoint from the canonical region code.
5. Mount through the bundled `ti-drive9` companion without requiring or creating profile files.

## Use Cases

- **E2B sandboxes:** inject `TI_FS_TOKEN` and `TI_REGION_CODE` and mount immediately.
- **CI/CD pipelines:** keep the token in CI secrets without distributing TiDB Cloud account keys.
- **Docker containers:** pass two environment variables without mounting the host credentials directory.
- **Configured machines:** select an ID-keyed local credential and omit repeated token input.
