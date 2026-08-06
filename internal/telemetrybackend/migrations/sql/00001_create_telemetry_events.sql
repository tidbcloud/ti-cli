-- +goose Up
CREATE TABLE IF NOT EXISTS telemetry_events (
  event_id VARCHAR(64) NOT NULL,
  received_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6),
  occurred_at TIMESTAMP(6) NOT NULL,
  anonymous_installation_id VARCHAR(128) NOT NULL,
  event_name VARCHAR(64) NOT NULL,
  command_path VARCHAR(128) NOT NULL,
  flag_names_json JSON NOT NULL,
  exit_code TINYINT UNSIGNED NOT NULL,
  error_code VARCHAR(64) NOT NULL DEFAULT '',
  duration_ms INT UNSIGNED NOT NULL,
  cloud_provider VARCHAR(32) NOT NULL DEFAULT '',
  region_code VARCHAR(64) NOT NULL DEFAULT '',
  cli_version VARCHAR(64) NOT NULL,
  os VARCHAR(32) NOT NULL,
  arch VARCHAR(32) NOT NULL,
  install_source VARCHAR(32) NOT NULL DEFAULT '',
  profile_source VARCHAR(32) NOT NULL DEFAULT '',
  schema_version INT UNSIGNED NOT NULL,
  PRIMARY KEY (event_id),
  KEY idx_received_at (received_at),
  KEY idx_command_received (command_path, received_at),
  KEY idx_version_received (cli_version, received_at),
  KEY idx_region_received (cloud_provider, region_code, received_at)
);

-- +goose Down
SELECT 1;
