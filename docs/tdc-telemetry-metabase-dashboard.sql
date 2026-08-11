-- tdc telemetry dashboard queries for TiDB + Metabase.
--
-- For scoped cards, configure these optional Metabase basic variables:
--   start_date  -> Date
--   end_date    -> Date
--   region_code -> Text
--   cli_version -> Text
--
-- Leave every variable unset to query the complete global dataset. The global
-- KPI card intentionally has no variables and is never narrowed by dashboard filters.
--
-- Use received_at, rather than occurred_at, as the dashboard time axis because
-- received_at is assigned by the backend and is not affected by client clock skew.
-- Success Rate and Wait Adoption are ratios from 0.0000 to 1.0000. Configure their
-- Metabase column type as Percentage; do not multiply them by 100 in SQL.
--
-- METABASE SETUP
-- 1. Save each CARD below as a separate native SQL question.
-- 2. For cards 2-13, configure start_date and end_date as optional Date variables.
-- 3. Configure region_code and cli_version as optional Text variables. Prefer a
--    dropdown populated from telemetry_events.region_code or cli_version.
-- 4. Add four dashboard filters: From date, Through date, Region, and CLI version.
--    Connect them to start_date, end_date, region_code, and cli_version respectively.
-- 5. Do not connect dashboard filters to card 1. It is the all-time global baseline.
-- 6. Use UTC for the dashboard reporting timezone because received_at is backend time.
--
-- RECOMMENDED DASHBOARD LAYOUT
-- Row 1: card 1 global KPI numbers and card 2 selected-scope KPI numbers.
-- Row 2: card 3 daily activity across the full width.
-- Row 3: card 4 command adoption, card 5 errors, and card 6 latency.
-- Row 4: card 7 Starter DB funnel and card 8 Filesystem funnel.
-- Row 5: card 9 repeat usage and card 10 version adoption.
-- Row 6: card 11 --wait adoption, card 12 platform distribution, and card 13 install
-- source distribution.

-- CARD 1: Global lifetime KPIs
-- Visualization: four Number cards, one for each returned metric. In Metabase,
-- duplicate the question and retain one SELECT expression in each copy. A single-row
-- Table is an acceptable compact alternative. Do not connect dashboard filters.
SELECT
  COUNT(DISTINCT anonymous_installation_id) AS `Active Installations`,
  COUNT(*) AS `Command Invocations`,
  COUNT(DISTINCT command_path) AS `Commands Used`,
  ROUND(1.0 * SUM(CASE WHEN exit_code = 0 THEN 1 ELSE 0 END) / NULLIF(COUNT(*), 0), 4) AS `Success Rate`
FROM `tdc_telemetry`.`telemetry_events`;

-- CARD 2: Scoped KPIs
-- Visualization: four Number cards, one for each returned metric. Connect all four
-- dashboard filters. With no variables set, this query also represents global data.
WITH scoped AS (
  SELECT * FROM `tdc_telemetry`.`telemetry_events`
  WHERE 1 = 1
    [[AND received_at >= {{start_date}}]]
    [[AND received_at < DATE_ADD({{end_date}}, INTERVAL 1 DAY)]]
    [[AND region_code = {{region_code}}]]
    [[AND cli_version = {{cli_version}}]]
)
SELECT
  COUNT(DISTINCT anonymous_installation_id) AS `Active Installations`,
  COUNT(*) AS `Command Invocations`,
  COUNT(DISTINCT command_path) AS `Commands Used`,
  ROUND(1.0 * SUM(CASE WHEN exit_code = 0 THEN 1 ELSE 0 END) / NULLIF(COUNT(*), 0), 4) AS `Success Rate`
FROM scoped;

-- CARD 3: Daily activity
-- Visualization: Combo chart. Use Activity Date as the X-axis, bars for Command
-- Invocations, a line for Active Installations, and optionally a second-axis line for
-- Success Rate. Format Success Rate as Percentage in Metabase.
WITH scoped AS (
  SELECT * FROM `tdc_telemetry`.`telemetry_events`
  WHERE 1 = 1
    [[AND received_at >= {{start_date}}]]
    [[AND received_at < DATE_ADD({{end_date}}, INTERVAL 1 DAY)]]
    [[AND region_code = {{region_code}}]]
    [[AND cli_version = {{cli_version}}]]
)
SELECT
  DATE(received_at) AS `Activity Date`,
  COUNT(DISTINCT anonymous_installation_id) AS `Active Installations`,
  COUNT(*) AS `Command Invocations`,
  ROUND(1.0 * SUM(CASE WHEN exit_code = 0 THEN 1 ELSE 0 END) / NULLIF(COUNT(*), 0), 4) AS `Success Rate`
FROM scoped
GROUP BY DATE(received_at)
ORDER BY `Activity Date`;

-- CARD 4: Command adoption and reliability
-- Visualization: Table sorted by Installations. Apply conditional formatting to
-- Success Rate and Failures, and format Success Rate as Percentage. For an
-- adoption-only view, use a horizontal bar chart with Command as the category and
-- Installations as the value.
WITH scoped AS (
  SELECT * FROM `tdc_telemetry`.`telemetry_events`
  WHERE 1 = 1
    [[AND received_at >= {{start_date}}]]
    [[AND received_at < DATE_ADD({{end_date}}, INTERVAL 1 DAY)]]
    [[AND region_code = {{region_code}}]]
    [[AND cli_version = {{cli_version}}]]
)
SELECT
  command_path AS `Command`,
  COUNT(DISTINCT anonymous_installation_id) AS `Installations`,
  COUNT(*) AS `Invocations`,
  SUM(CASE WHEN exit_code <> 0 THEN 1 ELSE 0 END) AS `Failures`,
  ROUND(1.0 * SUM(CASE WHEN exit_code = 0 THEN 1 ELSE 0 END) / NULLIF(COUNT(*), 0), 4) AS `Success Rate`,
  ROUND(AVG(duration_ms), 0) AS `Average Duration (ms)`
FROM scoped
GROUP BY command_path
ORDER BY `Installations` DESC, `Invocations` DESC;

-- CARD 5: Top actionable errors
-- Visualization: Table with Failures and Affected Installations, or a horizontal
-- stacked bar chart using Command as the category, Failures as the value, and Error
-- Code as the series.
WITH scoped AS (
  SELECT * FROM `tdc_telemetry`.`telemetry_events`
  WHERE 1 = 1
    [[AND received_at >= {{start_date}}]]
    [[AND received_at < DATE_ADD({{end_date}}, INTERVAL 1 DAY)]]
    [[AND region_code = {{region_code}}]]
    [[AND cli_version = {{cli_version}}]]
)
SELECT
  command_path AS `Command`,
  COALESCE(NULLIF(error_code, ''), 'unclassified') AS `Error Code`,
  COUNT(*) AS `Failures`,
  COUNT(DISTINCT anonymous_installation_id) AS `Affected Installations`
FROM scoped
WHERE exit_code <> 0
GROUP BY command_path, COALESCE(NULLIF(error_code, ''), 'unclassified')
ORDER BY `Failures` DESC, `Affected Installations` DESC
LIMIT 30;

-- CARD 6: Successful-command latency, exact nearest-rank p50/p95
-- Visualization: Table sorted by P95 Duration (ms). Apply conditional formatting to
-- that column and retain Samples so low-volume commands are not overinterpreted.
WITH scoped AS (
  SELECT * FROM `tdc_telemetry`.`telemetry_events`
  WHERE exit_code = 0
    [[AND received_at >= {{start_date}}]]
    [[AND received_at < DATE_ADD({{end_date}}, INTERVAL 1 DAY)]]
    [[AND region_code = {{region_code}}]]
    [[AND cli_version = {{cli_version}}]]
), ranked AS (
  SELECT
    command_path,
    duration_ms,
    ROW_NUMBER() OVER (PARTITION BY command_path ORDER BY duration_ms) AS rank_no,
    COUNT(*) OVER (PARTITION BY command_path) AS sample_count
  FROM scoped
)
SELECT
  command_path AS `Command`,
  MAX(sample_count) AS `Samples`,
  ROUND(AVG(duration_ms), 0) AS `Average Duration (ms)`,
  MAX(CASE WHEN rank_no = CEIL(sample_count * 0.50) THEN duration_ms END) AS `P50 Duration (ms)`,
  MAX(CASE WHEN rank_no = CEIL(sample_count * 0.95) THEN duration_ms END) AS `P95 Duration (ms)`
FROM ranked
GROUP BY command_path
HAVING MAX(sample_count) >= 5
ORDER BY `P95 Duration (ms)` DESC;

-- CARD 7: Starter DB activation funnel
-- Visualization: Funnel. Use Step as the stage and Installations as the value; sort
-- by Step Order ascending and hide Step Order from the displayed result when possible.
WITH scoped AS (
  SELECT * FROM `tdc_telemetry`.`telemetry_events`
  WHERE 1 = 1
    [[AND received_at >= {{start_date}}]]
    [[AND received_at < DATE_ADD({{end_date}}, INTERVAL 1 DAY)]]
    [[AND region_code = {{region_code}}]]
    [[AND cli_version = {{cli_version}}]]
), creators AS (
  SELECT anonymous_installation_id, MIN(received_at) AS created_at
  FROM scoped
  WHERE command_path = 'tdc db create-db-cluster' AND exit_code = 0
  GROUP BY anonymous_installation_id
), prepared AS (
  SELECT c.anonymous_installation_id, MIN(e.received_at) AS prepared_at
  FROM creators c
  JOIN scoped e ON e.anonymous_installation_id = c.anonymous_installation_id
    AND e.command_path = 'tdc db create-db-sql-users'
    AND e.exit_code = 0
    AND e.received_at >= c.created_at
  GROUP BY c.anonymous_installation_id
), queried AS (
  SELECT p.anonymous_installation_id, MIN(e.received_at) AS queried_at
  FROM prepared p
  JOIN scoped e ON e.anonymous_installation_id = p.anonymous_installation_id
    AND e.command_path = 'tdc db execute-sql-statement'
    AND e.exit_code = 0
    AND e.received_at >= p.prepared_at
  GROUP BY p.anonymous_installation_id
)
SELECT 1 AS `Step Order`, 'Created Starter cluster' AS `Step`, COUNT(*) AS `Installations` FROM creators
UNION ALL
SELECT 2, 'Created SQL users', COUNT(*) FROM prepared
UNION ALL
SELECT 3, 'Executed SQL', COUNT(*) FROM queried
ORDER BY `Step Order`;

-- CARD 8: Filesystem activation funnel
-- Visualization: Funnel. Use Step as the stage and Installations as the value; sort
-- by Step Order ascending and hide Step Order from the displayed result when possible.
WITH scoped AS (
  SELECT * FROM `tdc_telemetry`.`telemetry_events`
  WHERE 1 = 1
    [[AND received_at >= {{start_date}}]]
    [[AND received_at < DATE_ADD({{end_date}}, INTERVAL 1 DAY)]]
    [[AND region_code = {{region_code}}]]
    [[AND cli_version = {{cli_version}}]]
), creators AS (
  SELECT anonymous_installation_id, MIN(received_at) AS created_at
  FROM scoped
  WHERE command_path = 'tdc fs create-file-system' AND exit_code = 0
  GROUP BY anonymous_installation_id
), accesses AS (
  SELECT
    c.anonymous_installation_id,
    COUNT(*) AS access_count
  FROM creators c
  JOIN scoped e ON e.anonymous_installation_id = c.anonymous_installation_id
    AND e.command_path IN (
      'tdc fs mount-file-system',
      'tdc fs copy-file',
      'tdc fs read-file',
      'tdc fs list-files'
    )
    AND e.exit_code = 0
    AND e.received_at >= c.created_at
  GROUP BY c.anonymous_installation_id
)
SELECT 1 AS `Step Order`, 'Created filesystem' AS `Step`, COUNT(*) AS `Installations` FROM creators
UNION ALL
SELECT 2, 'Accessed filesystem', COUNT(*) FROM accesses
UNION ALL
SELECT 3, 'Repeated filesystem access', COALESCE(SUM(CASE WHEN access_count >= 2 THEN 1 ELSE 0 END), 0) FROM accesses
ORDER BY `Step Order`;

-- CARD 9: Repeat usage within the selected period
-- Visualization: Vertical bar chart. Use Active Day Bucket as the X-axis and
-- Installations as the Y-axis. Preserve the SQL result order.
WITH scoped AS (
  SELECT * FROM `tdc_telemetry`.`telemetry_events`
  WHERE 1 = 1
    [[AND received_at >= {{start_date}}]]
    [[AND received_at < DATE_ADD({{end_date}}, INTERVAL 1 DAY)]]
    [[AND region_code = {{region_code}}]]
    [[AND cli_version = {{cli_version}}]]
), installation_activity AS (
  SELECT
    anonymous_installation_id,
    COUNT(DISTINCT DATE(received_at)) AS active_days
  FROM scoped
  GROUP BY anonymous_installation_id
), buckets AS (
  SELECT
    CASE
      WHEN active_days = 1 THEN '1 day'
      WHEN active_days BETWEEN 2 AND 3 THEN '2-3 days'
      WHEN active_days BETWEEN 4 AND 7 THEN '4-7 days'
      ELSE '8+ days'
    END AS active_day_bucket,
    CASE
      WHEN active_days = 1 THEN 1
      WHEN active_days BETWEEN 2 AND 3 THEN 2
      WHEN active_days BETWEEN 4 AND 7 THEN 3
      ELSE 4
    END AS bucket_order
  FROM installation_activity
)
SELECT active_day_bucket AS `Active Day Bucket`, COUNT(*) AS `Installations`
FROM buckets
GROUP BY active_day_bucket, bucket_order
ORDER BY bucket_order;

-- CARD 10: CLI version adoption
-- Visualization: Horizontal bar chart with CLI Version as the category and
-- Installations as the value. Use the Table visualization when Success Rate also needs
-- to be compared, and format Success Rate as Percentage.
WITH scoped AS (
  SELECT * FROM `tdc_telemetry`.`telemetry_events`
  WHERE 1 = 1
    [[AND received_at >= {{start_date}}]]
    [[AND received_at < DATE_ADD({{end_date}}, INTERVAL 1 DAY)]]
    [[AND region_code = {{region_code}}]]
    [[AND cli_version = {{cli_version}}]]
)
SELECT
  cli_version AS `CLI Version`,
  COUNT(DISTINCT anonymous_installation_id) AS `Installations`,
  COUNT(*) AS `Invocations`,
  ROUND(1.0 * SUM(CASE WHEN exit_code = 0 THEN 1 ELSE 0 END) / NULLIF(COUNT(*), 0), 4) AS `Success Rate`
FROM scoped
GROUP BY cli_version
ORDER BY `Installations` DESC, `Invocations` DESC;

-- CARD 11: --wait adoption on supported create/delete commands
-- Visualization: Horizontal bar chart with Command as the category and Wait Adoption
-- as the value. Format Wait Adoption as Percentage. Show Invocations in the tooltip or
-- use a Table when sample size needs to remain visible.
WITH scoped AS (
  SELECT * FROM `tdc_telemetry`.`telemetry_events`
  WHERE command_path IN (
    'tdc db create-db-cluster',
    'tdc db create-db-cluster-branch',
    'tdc db delete-db-cluster',
    'tdc fs create-file-system'
  )
    [[AND received_at >= {{start_date}}]]
    [[AND received_at < DATE_ADD({{end_date}}, INTERVAL 1 DAY)]]
    [[AND region_code = {{region_code}}]]
    [[AND cli_version = {{cli_version}}]]
)
SELECT
  command_path AS `Command`,
  COUNT(*) AS `Invocations`,
  SUM(CASE WHEN JSON_CONTAINS(flag_names_json, JSON_QUOTE('wait')) = 1 THEN 1 ELSE 0 END) AS `Wait Invocations`,
  ROUND(
    1.0 * SUM(CASE WHEN JSON_CONTAINS(flag_names_json, JSON_QUOTE('wait')) = 1 THEN 1 ELSE 0 END)
    / NULLIF(COUNT(*), 0),
    4
  ) AS `Wait Adoption`
FROM scoped
GROUP BY command_path
ORDER BY `Invocations` DESC;

-- CARD 12: Platform distribution by operating system and architecture
-- Visualization: Row chart. Click Row, then the gear icon, then Display, and select
-- Stack. Use Operating System as the category, Architecture as the series, and
-- Installations as the value. Do not use Stack - 100% because this card compares
-- absolute adoption volume.
WITH scoped AS (
  SELECT * FROM `tdc_telemetry`.`telemetry_events`
  WHERE 1 = 1
    [[AND received_at >= {{start_date}}]]
    [[AND received_at < DATE_ADD({{end_date}}, INTERVAL 1 DAY)]]
    [[AND region_code = {{region_code}}]]
    [[AND cli_version = {{cli_version}}]]
)
SELECT
  os AS `Operating System`,
  arch AS `Architecture`,
  COUNT(DISTINCT anonymous_installation_id) AS `Installations`
FROM scoped
GROUP BY os, arch
ORDER BY `Operating System`, `Installations` DESC;

-- CARD 13: Installation-source distribution
-- Visualization: Row chart. Use Install Source as the category and Installations as
-- the value. This is intentionally separate from card 12: a stacked Row chart can
-- clearly represent one category plus one series, not OS, architecture, and install
-- source at once.
WITH scoped AS (
  SELECT * FROM `tdc_telemetry`.`telemetry_events`
  WHERE 1 = 1
    [[AND received_at >= {{start_date}}]]
    [[AND received_at < DATE_ADD({{end_date}}, INTERVAL 1 DAY)]]
    [[AND region_code = {{region_code}}]]
    [[AND cli_version = {{cli_version}}]]
)
SELECT
  COALESCE(NULLIF(install_source, ''), 'unknown') AS `Install Source`,
  COUNT(DISTINCT anonymous_installation_id) AS `Installations`
FROM scoped
GROUP BY COALESCE(NULLIF(install_source, ''), 'unknown')
ORDER BY `Installations` DESC;
