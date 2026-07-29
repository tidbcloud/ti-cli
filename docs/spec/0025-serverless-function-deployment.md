# Serverless Function Deployment

## Goal

Add an agent-friendly serverless function workflow that can package a local HTTP
function and deploy it to supported external runtimes such as Vercel Functions
and AWS Lambda.

tdc should not try to become a serverless platform in this spec. It should be a
predictable packaging and deployment adapter: local source plus an explicit
manifest becomes a provider-specific deployment, and the CLI reports the
resulting function URL and metadata in structured output.

## User-facing Commands

Add a new top-level command namespace:

```bash
tdc function init-function
tdc function validate-function
tdc function package-function
tdc function deploy-function
tdc function describe-function
tdc function list-functions
tdc function delete-function
tdc function invoke-function
tdc function get-function-url
tdc function logs-function
```

Initial MVP commands may implement only `init-function`, `validate-function`,
`package-function`, `deploy-function`, `invoke-function`, and
`get-function-url`; the remaining commands can be registered as placeholders
until provider state management is implemented.

Example:

```bash
tdc function init-function --function-name hello --runtime nodejs --target vercel-node
tdc function validate-function --function-file tdc.function.toml
tdc function package-function --function-file tdc.function.toml --output-dir .tdc/functions/hello
tdc function deploy-function --function-file tdc.function.toml --target vercel-node
tdc function get-function-url --function-name hello --target vercel-node
tdc function invoke-function --function-url https://example.vercel.app/api/hello
```

## Behavior

- Functions are HTTP request/response functions in the MVP.
- The user must choose an explicit deployment target. Do not auto-detect whether
  a function should deploy to Vercel Node.js, Vercel Edge, AWS Lambda ZIP, or
  AWS Lambda container.
- The default local function signature should be Web Fetch style where the
  runtime supports it:

```ts
export default {
  async fetch(request: Request) {
    return new Response("hello");
  },
};
```

- `validate-function` performs non-mutating checks: manifest schema, file
  existence, runtime/target compatibility, required external CLI availability,
  provider CLI authentication status, and forbidden secret placement.
- `package-function` creates provider-specific build output without deploying.
- `deploy-function` validates, packages, deploys, records local deployment
  metadata, and outputs structured JSON by default.
- `invoke-function` performs a single HTTP request against the function URL.
- `get-function-url` returns the recorded function URL when available.
- Mutating commands support `--dry-run`: `deploy-function` and
  `delete-function` must validate local inputs and planned provider operations
  without calling provider mutation APIs.
- Read-only commands reject `--dry-run`.
- Successful structured commands support `--output json|text` and `--query`.
- Do not prompt except inside existing `tdc configure`. Missing provider CLI
  installation or authentication must fail with actionable errors.
- `tdc function help` and `tdc function deploy-function help` must list each
  supported target and its required external tools, authentication expectation,
  and major cloud resources.

## Inputs And Config

Each function is described by a local manifest:

```toml
# tdc.function.toml
name = "hello"
runtime = "nodejs"
entrypoint = "src/hello.ts"
handler = "fetch"
target = "vercel-node"

[http]
path = "/api/hello"
methods = ["GET", "POST"]

[env]
DATABASE_URL = "secret:database_url"
```

Supported MVP manifest fields:

- `name`: stable function name.
- `runtime`: `nodejs`, `python`, or `go` depending on target support.
- `entrypoint`: local source entrypoint.
- `handler`: target-specific handler name or `fetch`.
- `target`: explicit deployment target.
- `[http].path`: HTTP path when the target supports routing.
- `[http].methods`: allowed methods for generated adapter code when supported.
- `[env]`: environment variable bindings. Values starting with `secret:` refer
  to provider-side secrets or tdc-managed secret references and must not be
  inlined into package artifacts.

Provider credentials are not stored in the function manifest, local deployment
metadata, or `~/.tdc/credentials`. The MVP delegates provider authentication to
official provider CLIs and their standard auth mechanisms.

Provider authentication inputs:

- Vercel:
  - `vercel` CLI must be installed.
  - Local interactive users authenticate with `vercel login`.
  - CI users set `VERCEL_TOKEN` through the CI secret store.
  - tdc verifies non-mutating availability/authentication through `vercel
    whoami` or an equivalent Vercel CLI command.
- AWS:
  - AWS CLI v2 must be installed.
  - Local interactive users authenticate through normal AWS CLI flows such as
    `aws configure`, `aws sso login`, or `AWS_PROFILE`.
  - CI users use standard AWS environment variables, OIDC role assumption, or
    another AWS CLI-supported credential source.
  - tdc verifies non-mutating availability/authentication through `aws sts
    get-caller-identity`.
  - `aws-lambda-container` additionally requires Docker or a compatible OCI
    builder because it builds and pushes a container image.

Provider deployment defaults are non-sensitive and live in `~/.tdc/config`
under the active profile. Keep tdc-wide, Vercel-specific, and AWS-specific
settings visually separated in examples:

```toml
[default]
function_default_target = "vercel-node"

function_vercel_project_id = "prj_..."
function_vercel_org_id = "team_..."

function_aws_profile = "default"
function_aws_region_code = "us-east-1"
function_aws_lambda_role_arn = "arn:aws:iam::123456789012:role/tdc-lambda-role"
function_aws_ecr_repository = "tdc-functions"
```

`function_default_target` is the default deployment target, not a provider
credential. Target selection precedence is:

1. `--target`
2. `target` in `tdc.function.toml`
3. `function_default_target` in `~/.tdc/config`

If none of those values is present, deployment and packaging commands fail and
ask the user to choose a target explicitly. Do not store both
`function_provider` and `function_default_target`; the provider is derived from
the target prefix.

Local deployment metadata lives under `~/.tdc/functions/`:

```text
~/.tdc/functions/<profile>/<function-name>/<target>/deployment.json
```

This metadata may store provider deployment IDs, function URLs, target, region,
runtime, source digest, and deploy time. It must not store provider tokens,
secret values, request bodies, response bodies, local absolute source paths, or
function source code.

## Target Matrix

MVP targets:

| Target | Runtime shape | Required tools | Auth owner | Notes |
| --- | --- | --- | --- | --- |
| `vercel-node` | Vercel Node.js Function | `vercel`; Node.js/npm/pnpm when the project build requires them | Vercel CLI | Best first target for TypeScript/JavaScript HTTP handlers. |
| `aws-lambda-zip` | AWS managed runtime ZIP | `aws`; language toolchain for the selected runtime; zip support | AWS CLI | Lighter than containers and does not require Docker. Runtime packaging differs by language. |
| `aws-lambda-container` | AWS Lambda container image | `aws`; Docker or compatible OCI builder | AWS CLI plus Docker registry auth through AWS/ECR | Most flexible AWS path for Go, Python, Node.js, and custom dependencies. Requires ECR. |

Future targets:

| Target | Runtime shape | Notes |
| --- | --- | --- |
| `vercel-edge` | V8 isolate Edge Function | Low-latency and lightweight, but limited APIs; not all Node.js modules work. |

Do not claim one source file is fully portable across all targets. The manifest
and validation must make runtime/target constraints explicit.

Help text for `tdc function` must include a compact target table equivalent to:

```text
Targets:
  vercel-node            requires: vercel CLI; auth: vercel login or VERCEL_TOKEN
  aws-lambda-zip         requires: aws CLI; auth: AWS CLI credentials/profile
  aws-lambda-container   requires: aws CLI, Docker/OCI builder, ECR; auth: AWS CLI credentials/profile
```

## Output And Errors

Example deploy JSON:

```json
{
  "function_name": "hello",
  "target": "vercel-node",
  "runtime": "nodejs",
  "status": "deployed",
  "url": "https://example.vercel.app/api/hello",
  "deployment_id": "dpl_...",
  "source_digest": "sha256:...",
  "metadata_stored": true
}
```

Example text output:

```text
Function: hello
Target: vercel-node
Runtime: nodejs
Status: deployed
URL: https://example.vercel.app/api/hello
```

Example validation error:

```text
tdc [ERROR]: target vercel-edge does not support runtime nodejs with Node built-in module "fs"; use vercel-node or remove the dependency
```

Example missing credentials error:

```text
tdc [ERROR]: authentication required: Vercel CLI is not authenticated; run `vercel login` or set VERCEL_TOKEN
```

Example missing external dependency error:

```text
tdc [ERROR]: dependency required: target aws-lambda-container requires Docker; install Docker or choose --target aws-lambda-zip
```

## After This Spec

Users can create and deploy a function from a local project:

```bash
tdc function init-function --function-name hello --runtime nodejs --target vercel-node
tdc function validate-function
tdc function deploy-function
tdc function get-function-url --function-name hello
tdc function invoke-function --function-name hello --method GET
```

CI can package and deploy non-interactively:

```bash
tdc function validate-function --function-file tdc.function.toml
tdc function package-function --function-file tdc.function.toml --output-dir .tdc/build/function
tdc function deploy-function --function-file tdc.function.toml --target aws-lambda-zip --dry-run
tdc function deploy-function --function-file tdc.function.toml --target aws-lambda-zip
tdc function deploy-function --function-file tdc.function.toml --target aws-lambda-container --dry-run
tdc function deploy-function --function-file tdc.function.toml --target aws-lambda-container
```

## Implementation Design

- `internal/function/manifest` owns TOML parsing, validation, defaults, schema
  versioning, and target compatibility checks.
- `internal/function/package` owns build output creation and source digesting.
- `internal/function/provider` defines a provider interface:

```go
type Provider interface {
    Validate(context.Context, DeployRequest) error
    Package(context.Context, PackageRequest) (PackageResult, error)
    Deploy(context.Context, DeployRequest) (DeployResult, error)
    Delete(context.Context, DeleteRequest) (DeleteResult, error)
    Logs(context.Context, LogsRequest) (LogsResult, error)
}
```

- `internal/function/provider/vercel` shells out to the Vercel CLI and parses
  stable machine-readable output where available.
- `internal/function/provider/aws` shells out to AWS CLI v2 and parses stable
  machine-readable JSON output.
- `internal/function/state` stores local deployment metadata under
  `~/.tdc/functions/`.
- `internal/cli` registers `tdc function ...` commands and keeps handlers thin.
- The first implementation must use official provider CLIs for authentication
  and provider operations. Do not implement provider login flows in tdc. Avoid
  scraping human output; request JSON output from provider CLIs whenever
  supported.

Packaging strategy:

- `vercel-node` may generate a Vercel-compatible project or Build Output API
  directory, then deploy through the Vercel CLI.
- `aws-lambda-zip` builds a runtime-specific ZIP artifact, creates or updates a
  Lambda function with ZIP package type, and optionally creates a Lambda
  Function URL.
- `aws-lambda-container` builds an OCI image, pushes it to ECR, creates or
  updates a Lambda function, and optionally creates a Lambda Function URL.
- All packaging must happen in a deterministic output directory under `.tdc/`
  or a user-specified `--output-dir`.
- Generated adapters should be small and explicit. Do not rewrite user source
  files in place.

## Provider CLI Call Chain

Vercel deploy flow, high level:

1. Read `tdc.function.toml`.
2. Validate target/runtime compatibility.
3. Build provider output for Vercel.
4. Verify `vercel` is installed.
5. Verify `vercel` is authenticated through `vercel whoami` or equivalent. CI
   may provide `VERCEL_TOKEN`; tdc does not store it.
6. Run `vercel deploy` with non-interactive flags and machine-readable output
   where available.
7. Record deployment URL and metadata.

AWS Lambda ZIP deploy flow, high level:

1. Read `tdc.function.toml`.
2. Validate AWS CLI, AWS credentials, AWS region, Lambda role ARN, runtime, and
   handler settings.
3. Build runtime-specific ZIP artifact.
4. Run AWS CLI commands to create or update the Lambda function with ZIP
   package type.
5. Create or update Function URL when requested.
6. Record function ARN, URL, source digest, and metadata.

AWS Lambda container deploy flow, high level:

1. Read `tdc.function.toml`.
2. Validate AWS CLI, Docker/OCI builder, AWS credentials, AWS region, Lambda
   role ARN, and ECR repository settings.
3. Build OCI image.
4. Authenticate Docker to ECR through AWS CLI.
5. Push image to ECR.
6. Run AWS CLI commands to create or update the Lambda function with image
   package type.
7. Create or update Function URL when requested.
8. Record function ARN, URL, image digest, and metadata.

## Dependencies And Platform

Go dependencies:

- No provider SDK is required for the MVP if the CLI orchestration path remains
  sufficient.
- Use standard library process execution and JSON decoding for provider CLI
  integration.
- Add provider SDKs only in a later spec if official CLIs cannot provide stable
  non-interactive behavior.

External tools:

- `vercel-node` requires the Vercel CLI. It may also require Node.js/npm/pnpm
  depending on the project build.
- `aws-lambda-zip` requires AWS CLI v2 and the language toolchain needed to
  build the selected runtime artifact.
- `aws-lambda-container` requires AWS CLI v2, Docker or a compatible OCI
  builder, and network access to ECR.
- `vercel-edge` future support requires stricter runtime validation because
  Edge runtime APIs differ from full Node.js.

Platform notes:

- Packaging should work on macOS and Linux first.
- Windows support is desired but may be limited by Docker and shell behavior in
  the first implementation.
- Do not introduce cgo requirements into the tdc binary.

## Dependencies

- `0001-cli-foundation.md` for command patterns.
- `0002-local-config-and-credentials.md` for profile and local state rules.
- `0003-output-error-query-dry-run.md` for output, query, and dry-run behavior.
- `0014-tdc-fs-unix-command-aliases.md` is unrelated but should remain
  unaffected.

## Acceptance Criteria

- `tdc function init-function` creates a minimal `tdc.function.toml` and sample
  source file without overwriting existing files unless `--overwrite` is set.
- `tdc function validate-function` rejects invalid target/runtime combinations
  before any provider mutation.
- `tdc function help` and `tdc function deploy-function help` show the
  supported target table with required external tools and authentication notes.
- `tdc function package-function` writes deterministic provider output and
  reports a source digest.
- `tdc function deploy-function --dry-run` validates inputs and planned provider
  operations without mutating remote state.
- `tdc function deploy-function --target vercel-node` deploys a simple HTTP
  function and returns a URL.
- `tdc function deploy-function --target aws-lambda-zip` deploys a simple HTTP
  function to Lambda ZIP and returns a Function URL when configured.
- `tdc function deploy-function --target aws-lambda-container` deploys a simple
  HTTP function to Lambda container and returns a Function URL when configured.
- `tdc function invoke-function` can call the returned URL and print status,
  headers, and body according to output mode.
- Local deployment metadata is stored under `~/.tdc/functions/` without secrets.
- `make test` covers manifest validation, packaging decisions, state storage,
  and dry-run behavior without live provider credentials.
- Live provider tests, if added, are opt-in and isolated behind explicit
  environment variables. They must create uniquely named functions and clean up
  only resources created by the test run.
- README and AGENTS are updated when commands are implemented.

## Out Of Scope

- Running a self-hosted serverless platform.
- Automatic migration of arbitrary existing applications.
- Background jobs, queues, schedules, cron triggers, WebSockets, or streaming
  functions.
- Durable function state, retries, workflows, or orchestration.
- Provider billing management.
- Provider secret creation beyond references needed for deployment.
- Full cross-target source compatibility.
- Cloudflare Workers, Netlify Functions, Fly Machines, Google Cloud Run, Azure
  Functions, or other targets.
- Telemetry for function deployments.
