-- Roll back API key usage counters (000094).
ALTER TABLE api_keys
    DROP COLUMN IF EXISTS request_count_24h_window_start,
    DROP COLUMN IF EXISTS request_count_24h,
    DROP COLUMN IF EXISTS request_count_total;
