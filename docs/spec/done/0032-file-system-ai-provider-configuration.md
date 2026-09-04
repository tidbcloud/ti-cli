# File System AI Provider Configuration

## Goal

Expose the TiDB Cloud Filesystem server's tenant-scoped media extraction and embedding provider configuration through `ti`. A user with TiDB Cloud organization credentials can inspect, enable, update, and disable the AI processing configuration of one File System without invoking the Drive9 admin CLI directly.

This spec also extends existing File System inventory output with the media and video quota fields already returned by the server. It is a `ti` client integration of the backend behavior introduced by `tidbcloud/fs` and Drive9 changes corresponding to media extraction configuration, media quota accounting, and embedding configuration. It does not add AI execution to `ti` itself.

## Scope

Add four commands:

```text
ti fs describe-file-system-extract-configuration
ti fs update-file-system-extract-configuration
ti fs describe-file-system-embedding-configuration
ti fs update-file-system-embedding-configuration
```

Extend the quota objects returned by these existing commands:

```text
ti fs list-file-systems
ti fs describe-file-system
```

The new commands call the hosted File System control-plane APIs directly through typed clients under `internal/api/fs`. They must not shell out to `ti-drive9 admin`, parse companion output, or make `ref/` a runtime, build, or test dependency.

## User-Facing Commands

Inspect image extraction configuration:

```bash
ti fs describe-file-system-extract-configuration \
  --file-system-id <file-system-id> \
  --media-type image
```

Enable image extraction with OpenAI:

```bash
TI_FS_AI_PROVIDER_API_KEY="<provider-api-key>" \
ti fs update-file-system-extract-configuration \
  --file-system-id <file-system-id> \
  --media-type image \
  --enabled true \
  --provider-api-base https://api.openai.com/v1 \
  --provider-model <vision-model> \
  --provider-protocol openai
```

Enable OpenAI-compatible audio transcription:

```bash
TI_FS_AI_PROVIDER_API_KEY="<provider-api-key>" \
ti fs update-file-system-extract-configuration \
  --file-system-id <file-system-id> \
  --media-type audio \
  --enabled true \
  --provider-api-base https://api.openai.com/v1 \
  --provider-model gpt-4o-transcribe \
  --provider-protocol openai
```

Enable Alibaba Cloud Model Studio Qwen ASR:

```bash
TI_FS_AI_PROVIDER_API_KEY="<dashscope-api-key>" \
ti fs update-file-system-extract-configuration \
  --file-system-id <file-system-id> \
  --media-type audio \
  --enabled true \
  --provider-api-base https://dashscope.aliyuncs.com/compatible-mode/v1 \
  --provider-model qwen3-asr-flash \
  --provider-protocol qwen-asr
```

Update only the extraction prompt of an already enabled configuration:

```bash
ti fs update-file-system-extract-configuration \
  --file-system-id <file-system-id> \
  --media-type image \
  --prompt "Describe the image and return searchable attributes."
```

Disable extraction for one media type:

```bash
ti fs update-file-system-extract-configuration \
  --file-system-id <file-system-id> \
  --media-type image \
  --enabled false
```

Inspect embedding configuration:

```bash
ti fs describe-file-system-embedding-configuration \
  --file-system-id <file-system-id>
```

Enable app-managed embedding:

```bash
TI_FS_AI_PROVIDER_API_KEY="<provider-api-key>" \
ti fs update-file-system-embedding-configuration \
  --file-system-id <file-system-id> \
  --enabled true \
  --provider-api-base https://api.openai.com/v1 \
  --provider-model text-embedding-3-small
```

Disable app-managed embedding:

```bash
ti fs update-file-system-embedding-configuration \
  --file-system-id <file-system-id> \
  --enabled false
```

All four commands require an explicit `--file-system-id`. They do not infer the target from an FS token or a historical profile default. The global `--region` precedence remains unchanged.

## Provider Support

The backend selects an implementation by wire protocol, not by a vendor enum. `ti` therefore exposes `--provider-protocol` only where the backend API has a protocol field and does not add `--provider openai|alibaba`.

| Provider or interface | Embedding | Image extraction | Audio extraction | Video extraction |
| --- | --- | --- | --- | --- |
| OpenAI | Supported through `openai` | Supported through `openai` | Supported through `openai` | Supported through `openai` |
| OpenAI-compatible API | Supported when the endpoint satisfies the exact contract and returns 1024 dimensions | Supported when the endpoint implements the required Chat Completions vision contract | Supported when the endpoint implements the required transcription contract | Supported when the endpoint implements the required Chat Completions vision contract |
| Alibaba Cloud Model Studio Qwen ASR | No native provider contract | No native provider contract | Supported through `qwen-asr` | No native provider contract |

Supported protocols and request shapes are:

- `openai` embedding: `POST /v1/embeddings` with a fixed requested and returned vector width of 1024.
- `openai` image and video extraction: `POST /v1/chat/completions` with image input.
- `openai` audio extraction: `POST /v1/audio/transcriptions`.
- `qwen-asr` audio extraction: Alibaba Cloud Model Studio compatible-mode `POST /v1/chat/completions` with Qwen ASR audio input.

For extraction, `--provider-protocol` defaults to `openai`. Image and video accept only `openai`; audio accepts `openai` and `qwen-asr`. Embedding is always OpenAI-compatible in this version and therefore does not expose `--provider-protocol`.

Anthropic Messages, Google Gemini or Vertex AI native APIs, AWS Bedrock native APIs, and Azure OpenAI native authentication and deployment URL forms are not supported. A provider not listed above may work only if it exposes the exact OpenAI-compatible request and response contract and passes server-side validation. Documentation must describe that as conditional compatibility, not as officially supported vendor integration.

## Authentication And Secret Input

The four configuration commands use TiDB Cloud public/private API credentials and organization-level authorization. An FS owner or scoped token is neither required nor sufficient for these admin operations.

The provider API key is accepted only through:

```text
TI_FS_AI_PROVIDER_API_KEY
```

Do not add a plaintext `--provider-api-key` flag because command-line arguments are commonly retained in shell history and process listings. Do not persist the provider key in `~/.ti/config`, `~/.ti/credentials`, `~/.ti/.preferences`, mount locators, or another local file.

The environment variable is read only when an update supplies a provider credential set. It is ignored for disable and prompt-only operations, so an ambient value cannot accidentally turn a disable request into a provider replacement.

The provider key is sent in the HTTPS request body to the File System backend. The backend validates it against the provider and stores it encrypted. GET responses return only a masked key. `ti` must register the plaintext with the existing redactor before request encoding so it cannot appear in debug output, operation logs, telemetry, dry-run output, errors, or test failure diagnostics.

Provider endpoints receive File System content for extraction and text or descriptions for embedding after the configuration is enabled. User documentation must state this data-sharing boundary and tell users to choose a provider account and retention policy appropriate for their data.

## Extract Configuration Contract

Supported media types are exactly:

```text
image
audio
video
```

`text` is reserved by the backend storage model but is not an available API media type. `ti` rejects `--media-type text` and unknown values locally.

The read command calls:

```http
GET /v1/admin/tenants/{file_system_id}/extract-config/{media_type}
```

The update command calls:

```http
PUT /v1/admin/tenants/{file_system_id}/extract-config/{media_type}
Content-Type: application/json
```

The typed response contains:

```json
{
  "enabled": true,
  "api_base": "https://api.openai.com/v1",
  "api_key": "sk-a********",
  "model": "<model>",
  "protocol": "openai",
  "prompt": "<prompt>",
  "source": "custom",
  "updated_at": "2026-08-26T10:00:00Z"
}
```

`source` is server-owned and can be `custom`, `default`, or `none`. `ti` preserves unknown future source values rather than rejecting an otherwise valid response.

Extract updates preserve the backend's partial-update semantics:

- `--enabled true` on an absent or disabled custom configuration requires `--provider-api-base`, `--provider-model`, and `TI_FS_AI_PROVIDER_API_KEY`.
- Replacing any member of that provider trio requires all three members in the same command.
- `--enabled false` sends only `enabled=false` and clears the custom provider fields on the server.
- `--prompt` can update the prompt of an already enabled configuration without resending credentials.
- An explicit empty `--prompt ""` clears the custom prompt and returns to the backend's default prompt behavior.
- Changing `--provider-protocol` for audio requires the complete provider trio.
- A command that supplies none of `--enabled`, `--provider-api-base`, `--provider-model`, `--provider-protocol`, or `--prompt` is a usage error.
- Explicit empty provider base, model, or key values are invalid.

`--enabled` requires an explicit `true` or `false` value. It must retain an unset state internally so omission is distinguishable from `false`.

## Embedding Configuration Contract

The read command calls:

```http
GET /v1/admin/tenants/{file_system_id}/embedding-config
```

The update command calls:

```http
PUT /v1/admin/tenants/{file_system_id}/embedding-config
Content-Type: application/json
```

The typed response contains:

```json
{
  "enabled": true,
  "api_base": "https://api.openai.com/v1",
  "api_key": "sk-a********",
  "model": "text-embedding-3-small",
  "source": "custom",
  "generation": 3,
  "updated_at": "2026-08-27T10:00:00Z"
}
```

`source` can be `custom`, `default`, `none`, or `database_auto`. `generation` is server-owned and changes when the vector contract changes. `ti` must not synthesize or mutate either field.

Embedding PUT is a full replacement:

- Enabling requires `--enabled true`, `--provider-api-base`, `--provider-model`, and `TI_FS_AI_PROVIDER_API_KEY` in the same command.
- Disabling requires `--enabled false` and rejects provider configuration flags.
- The vector width is fixed at 1024. Do not expose a dimensions flag.
- Native File Systems using TiDB database-managed `auto` embedding return `source=database_auto`; attempting app-managed configuration is not applicable and the backend returns HTTP 409.
- Shared File Systems and native File Systems whose effective embedding mode is `fts_only` support app-managed configuration.

## Provider Validation And Retry Boundary

When provider credentials are created, re-enabled, or replaced, the backend performs a real provider request before persisting the new configuration:

- Embedding validation requires exactly 1024 finite vector values.
- Image and video validation send a small built-in image through the configured vision model.
- Audio validation sends a bounded silent audio sample through the selected protocol.

The validation can incur a small charge on the user's provider account. `ti` must mention this in command help and documentation.

The mutating commands must not automatically retry a PUT after an ambiguous network failure, timeout, or lost response. A provider request may already have been charged and the configuration may already have been persisted. The error should direct the user to run the matching describe command before deciding whether to retry.

`ti` validates required fields, media/protocol compatibility, HTTPS URL syntax, and prompt size before sending the request. The server remains authoritative for endpoint safety, DNS resolution, provider capability, credentials, model compatibility, and response validation. `ti` never calls the provider directly.

## Quota And Usage Output

Extend `internal/api/fs.AdminTenantQuotaConfig` with:

```json
{
  "max_media_llm_files": 100,
  "max_video_llm_files": 30
}
```

Extend `internal/api/fs.AdminTenantQuotaUsage` with:

```json
{
  "media_file_count": 12,
  "video_file_count": 3
}
```

The exact values are server-owned and may vary by plan or deployment. `ti` must not hardcode the example limits as product defaults. The new fields appear in JSON output from `list-file-systems` and `describe-file-system` whenever the backend returns quota data.

The text list remains compact and does not add four quota columns. Text describe renders the new values with stable labels alongside the existing quota details. This spec does not expose an admin command for changing quota values.

## Output

Configuration commands return structured JSON by default and support `--output text` and `--query` through the shared control-plane rendering path. Output field names match the backend contract except that the public resource selector remains `file_system_id` rather than `tenant_id`.

Example extract text output:

```text
File system ID: <file-system-id>
Media type: image
Enabled: true
Source: custom
Provider API base: https://api.openai.com/v1
Provider API key: sk-a********
Provider model: <model>
Provider protocol: openai
Prompt: <prompt>
Updated at: 2026-08-26T10:00:00Z
```

Example embedding text output:

```text
File system ID: <file-system-id>
Enabled: true
Source: custom
Provider API base: https://api.openai.com/v1
Provider API key: sk-a********
Provider model: text-embedding-3-small
Generation: 3
Updated at: 2026-08-27T10:00:00Z
```

Missing optional fields render as `none`, not an empty unlabeled value. JSON preserves omission from the backend where practical. Neither output mode ever returns the plaintext provider key.

## Dry Run

Both update commands support `--dry-run`. Dry run performs local profile, TiDB Cloud credential, region, File System ID, media type, protocol, URL, required-field, and prompt validation. It does not call the File System backend or the external provider and does not write local state.

Dry-run output includes the target API path, non-secret request fields, and whether a provider API key was supplied. It renders the key only as a fixed redacted marker and never includes a masked prefix derived from the plaintext.

Describe commands are read-only and reject `--dry-run`.

## Errors

Use stable typed errors and preserve a safe backend reason:

| Condition | Behavior |
| --- | --- |
| Missing or invalid File System ID | Usage error before API execution |
| Unsupported media type or protocol | Usage error listing accepted values |
| Missing provider trio while enabling or replacing | Usage error naming all required inputs |
| Non-HTTPS provider API base | Usage error before API execution |
| Missing TiDB Cloud credentials | Existing authentication-required error and API-key creation URL |
| HTTP 401 | Existing TiDB Cloud authentication error |
| HTTP 403 | Existing organization authorization error |
| HTTP 404 | Existing File System resource-not-found behavior |
| HTTP 409 for database-managed embedding | `fs.embedding_configuration_not_applicable` with `source=database_auto` guidance |
| Provider rejects credentials, endpoint, model, or media capability | `fs.ai_provider_validation_failed` with the backend's sanitized reason |
| Provider rate limit, timeout, or availability failure | Runtime error that warns the user not to retry blindly and points to the describe command |
| Malformed successful response | API response-contract error without exposing credentials or provider payloads |

Do not include provider response bodies, headers, file content, prompts, or API keys in errors. The server's bounded validation category and HTTP status may be retained.

## Implementation Design

- Add typed extract and embedding request/response models and methods under `internal/api/fs`.
- Add a focused service package under `internal/fs` for AI configuration validation and use cases; do not place provider HTTP implementations in `ti`.
- Register all four commands through `controlPlaneCommandSpec` so auth, dry-run, query, JSON/text rendering, and error behavior remain consistent.
- Add explicit static permissions for extract configuration read/update and embedding configuration read/update. Do not infer permission from command names.
- Reuse the existing canonical File System endpoint resolver and Digest-authenticated TiDB Cloud profile loading.
- Add the provider API key to the API client's redactor before any request can be logged.
- Do not invoke or depend on the bundled companion for these four commands.
- Do not add local configuration files, credential fields, context entries, caches, or migration work.
- Update `README.md`, generated command documentation, PingCAP Preview documentation, `AGENTS.md`, and command-surface tests when implementation begins.

## Logging And Telemetry

Operation logs and telemetry may record:

- Command path.
- Region code.
- Media type as a bounded enum.
- Provider protocol as a bounded enum.
- Enabled/disabled action.
- Stable error code, exit code, and duration.

They must not record provider API base, API key, model, prompt, File System ID, provider response, extracted content, embedding input, or returned vectors. Flag names may be recorded under the existing policy, but flag values remain excluded.

## Test Plan

Unit tests must cover:

- Typed request paths, methods, headers, and JSON bodies for all four APIs.
- Extract partial updates and embedding full replacements.
- Explicit `--enabled true|false` handling and omission detection.
- Media type and protocol compatibility, including local rejection of `text`.
- Required provider trio and environment-only secret loading.
- HTTPS provider URL validation and the 8 KiB prompt limit.
- Redaction from errors, debug output, operation logs, dry-run output, and test diagnostics.
- Response decoding for every documented `source` value, masked keys, generation, and missing optional fields.
- HTTP 409 database-managed embedding behavior and provider-validation error mappings.
- No automatic retry of update requests.
- JSON, text, and `--query` rendering.
- The four new quota and usage fields in list and describe responses.

Black-box e2e uses a fake File System API and must verify complete command wiring without importing fixtures or code from `ref/`.

Live e2e must at minimum:

1. Create a uniquely named temporary File System through the existing lifecycle.
2. Describe all three extract media configurations and the embedding configuration.
3. Disable an applicable configuration, describe it again, and verify the server response without requiring an external provider account.
4. Verify the new quota and usage fields are preserved when returned.
5. Delete only the temporary File System through the existing cleanup path.

Successful enablement against a paid external provider is separately opt-in because it requires a provider credential and can incur charges. When configured, it must use dedicated ignored environment variables and must not become a prerequisite for `make test`, `make e2e`, or the ordinary live-e2e suite.

Before release, probe the read endpoints in all four supported File System regions. A region that has not deployed the required backend API must fail as unavailable; `ti` must not fall back to another region or emulate the operation through Drive9 local state.

## Acceptance Criteria

- Users can inspect and update image, audio, and video extraction configuration for one explicit File System.
- Users can inspect and update app-managed embedding configuration where the server reports it is applicable.
- Help and documentation clearly distinguish OpenAI, conditional OpenAI-compatible APIs, and Alibaba Cloud Model Studio Qwen ASR support.
- Unsupported native provider interfaces are not presented as supported.
- Provider API keys are environment-only, never stored locally, always redacted, and returned only in masked server form.
- Provider validation and its possible charge are disclosed, and mutating requests are not automatically retried after ambiguous failures.
- Read and update commands use direct typed File System APIs with TiDB Cloud credentials and never invoke `ti-drive9 admin`.
- Existing FS token-only data-plane workflows remain unchanged and cannot perform these organization-level operations.
- `list-file-systems` and `describe-file-system` preserve media/video quota limits and usage counters returned by the backend.
- Unit, black-box e2e, and applicable live-e2e coverage pass without depending on `ref/`.

## Out Of Scope

- Implementing embedding, image understanding, transcription, video processing, vector search, or provider validation inside `ti`.
- Supporting text extraction configuration.
- Supporting native Anthropic, Gemini, Vertex AI, Bedrock, or Azure OpenAI interfaces.
- Configurable embedding dimensions or separate providers for document and query embedding.
- Changing File System quota values from `ti`.
- Listing provider models or auto-detecting a provider from its model name or URL.
- Persisting provider credentials locally or adding an interactive credential prompt.
- Backfilling or reprocessing historical files from `ti`.
- Falling back across regions, providers, protocols, or File Systems.

## Dependencies

- `docs/spec/done/0031-fs-tenant-metadata-control-plane.md`
- Hosted File System extract-config endpoints for `image`, `audio`, and `video`.
- Hosted File System embedding-config endpoint.
- Hosted File System inventory responses containing media/video quota limits and usage counters.
