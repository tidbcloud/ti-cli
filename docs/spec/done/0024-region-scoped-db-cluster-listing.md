# Region-scoped Starter DB Cluster Listing

## Goal

Make `tdc db list-db-clusters` return only verified Starter clusters in the command's effective tdc region. The TiDB Cloud Starter API lists clusters across regions, so tdc must apply its own region scope instead of exposing every Starter cluster accessible to the API key.

This spec changes listing only. Commands that receive an explicit globally unique cluster ID, including describe, update, delete, branch, SQL-user, connection-string, and SQL execution commands, remain outside this spec.

## User-facing Behavior

The command surface does not change:

```bash
tdc db list-db-clusters
tdc db list-db-clusters --region aws-us-west-2
tdc db list-db-clusters --filter 'state="ACTIVE"'
```

The effective region continues to resolve in the existing order:

1. Global `--region` flag.
2. `TDC_REGION_CODE`.
3. Selected profile `region_code`.

`tdc db list-db-clusters` returns only clusters whose provider and native region match that effective region. There is no `--all-regions` escape hatch in this spec. Users can select another region explicitly with global `--region` without changing the stored profile.

Examples:

- Effective region `aws-us-east-1` includes an AWS cluster in `us-east-1` and excludes AWS clusters in other regions and Alibaba Cloud clusters.
- Effective region `ali-ap-southeast-1` includes an Alibaba Cloud cluster in `ap-southeast-1` and excludes an AWS cluster in the identically named native region.

The JSON and text output schemas do not change. A successful list with no matching clusters returns an empty `clusters` array rather than an error.

## Region Identity And Mapping

tdc stores and accepts one canonical region code:

```text
<tdc-provider-prefix>-<native-region-code>
```

The region package resolves it into the values needed by the API and response verifier:

| tdc region code | tdc provider | API provider | native region | API region name |
| --- | --- | --- | --- | --- |
| `aws-us-east-1` | `aws` | `aws` | `us-east-1` | `regions/aws-us-east-1` |
| `aws-us-west-2` | `aws` | `aws` | `us-west-2` | `regions/aws-us-west-2` |
| `ali-ap-southeast-1` | `alibaba_cloud` | `alicloud` | `ap-southeast-1` | `regions/alicloud-ap-southeast-1` |

Use the existing `internal/config/region` parser and `internal/api/endpoints.APIProvider` mapping. Do not duplicate a second provider map inside the DB package.

The response verifier must tolerate the documented TiDB Cloud region representations without weakening the match:

- `region.name`, such as `regions/aws-us-east-1`;
- `regionId`, whether returned as a provider-qualified value or a native region value;
- `cloudProvider`, with `alicloud` normalized to tdc's internal `alibaba_cloud` value;
- legacy `provider` only as a fallback when `cloudProvider` is absent.

A cluster whose response omits enough metadata to verify both provider and native region is excluded. Do not infer a provider from a native region such as `ap-southeast-1`, because multiple providers can expose the same native region code.

## API Call Chain

1. `internal/cli` loads the selected profile through the existing config precedence path. The resulting profile already contains `PlacementRegionCode`, internal `CloudProvider`, and native `RegionCode`.
2. `internal/db.Service.ListClusters` resolves the expected API provider and API region name from that profile.
3. tdc adds an immutable region clause to the API list filter:

   ```text
   region.provider="<api-provider>" AND region.name="regions/<api-provider>-<native-region>"
   ```

4. If the user supplied `--filter`, combine it with the immutable region clause using `AND`. Region values come only from the validated placement allowlist and are never copied from unvalidated filter input.
5. `internal/api/starter.Client.ListClusters` sends the combined filter to `GET /v1beta1/clusters` with the existing pagination and order parameters.
6. tdc filters the returned page first to verified Starter resources and then to verified matching-region resources.
7. Return the API `nextPageToken` unchanged. Continue omitting API `totalSize`, because the server total can include resources removed by tdc's Starter or defensive region filters.

Server-side filtering preserves useful pagination behavior. Defensive response filtering is still required because tdc owns the product boundary and must not assume that a remote filter was honored perfectly.

## Filter And Pagination Semantics

- `--page-size` remains the number requested from the API, not a promise that the locally verified page contains that many clusters.
- `--page-token`, `--skip`, and `--order-by` retain their current API meanings.
- A user filter that conflicts with the effective region naturally returns an empty page. tdc does not parse and rewrite arbitrary user filter expressions beyond combining the complete expression with the immutable region clause.
- Preserve an API next-page token even when local verification removes every cluster from a page.
- `--query` and `--output json|text` run after Starter and region filtering.

## Package And Code Design

- `internal/config/region` remains the source of truth for canonical tdc placement parsing.
- `internal/api/endpoints` remains the source of truth for translating `alibaba_cloud` to API provider `alicloud`.
- `internal/db` owns combining the fixed region filter with the optional user filter and filtering returned cluster models.
- `internal/api/starter` remains a transport and wire-decoding package. It must preserve all region fields needed by `internal/db` but must not read profiles or impose tdc product policy.
- Keep Starter-plan and region verification as separate helpers so tests can cover each boundary independently.

Do not add a third-party dependency. The implementation uses existing Go packages and remains cross-platform with no cgo.

## Errors And Safety

- Invalid or missing effective region behavior remains owned by profile loading and fails before the API call.
- Region metadata missing from one returned cluster does not fail the entire list; that unverifiable cluster is omitted.
- API authentication, authorization, rate-limit, and transport failures retain their existing structured errors.
- Do not print warnings before JSON output. Filtering must not corrupt stdout or alter the JSON schema.

## Tests

Unit tests must cover:

- AWS effective region with mixed AWS regions;
- Alibaba Cloud and AWS clusters sharing `ap-southeast-1`;
- `alicloud` to `alibaba_cloud` provider normalization;
- response matching through `region.name`, provider-qualified `regionId`, and native `regionId` plus provider;
- exclusion when provider or region metadata is missing or conflicting;
- composition with a user `--filter`;
- preservation of next-page token on an empty locally filtered page;
- global `--region` overriding environment and profile placement;
- unchanged Starter-only filtering and output/query behavior.

Black-box e2e tests must return only clusters matching the configured fake API region. Live e2e must assert that every listed cluster has the effective profile or `--region` placement. Tests must not create or delete clusters in another region merely to prove exclusion.

## Documentation Updates

Update `README.md`, `AGENTS.md`, and the English PingCAP command page for `tdc db list-db-clusters` to state that the command is scoped to the effective region. Examples should show global `--region` as the way to inspect another region.

## After This Spec

Users and agents can treat `tdc db list-db-clusters` as a region-scoped inventory command. Selecting `aws-us-east-1` no longer exposes otherwise accessible Starter clusters from `aws-us-west-2` or Alibaba Cloud in the same output.
