-- +goose Up
ALTER TABLE telemetry_events ADD COLUMN IF NOT EXISTS tag VARCHAR(128) NOT NULL DEFAULT '';
ALTER TABLE telemetry_events ADD COLUMN IF NOT EXISTS extra_json JSON NULL;
ALTER TABLE telemetry_events ADD INDEX IF NOT EXISTS idx_tag_received (tag, received_at);

-- +goose Down
ALTER TABLE telemetry_events DROP INDEX idx_tag_received;
ALTER TABLE telemetry_events DROP COLUMN extra_json;
ALTER TABLE telemetry_events DROP COLUMN tag;
