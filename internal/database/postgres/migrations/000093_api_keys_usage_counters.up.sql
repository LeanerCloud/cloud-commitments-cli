-- API key usage counters (deferred sub-task from #340 / #344).
--
-- Surface per-key request volume to the Admin -> API Keys UI without
-- introducing a separate audit-log table. Two counters live directly on
-- the api_keys row, updated atomically by the same code path that
-- already maintains last_used_at:
--
--   * request_count_total       -- lifetime count since the key was created.
--   * request_count_24h         -- rolling 24-hour count; reset by the
--                                 increment path once the window age
--                                 crosses 24h (see store_postgres.go
--                                 RecordAPIKeyUsage).
--   * request_count_24h_window_start -- the timestamp the current 24h
--                                 window started at; NULL until the
--                                 first request after this migration
--                                 runs.
--
-- Counters default to 0 so all existing rows show "0 requests" rather
-- than "(null)" in the UI. The window-start column is nullable because
-- a row with zero-ever-usage shouldn't lie about when its window began.

ALTER TABLE api_keys
    ADD COLUMN IF NOT EXISTS request_count_total BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS request_count_24h BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS request_count_24h_window_start TIMESTAMPTZ;
