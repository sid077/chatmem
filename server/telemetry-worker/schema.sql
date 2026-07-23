-- chatmem telemetry ingest schema (Cloudflare D1 / SQLite dialect)
-- Applied with: wrangler d1 execute chatmem-telemetry --remote --file=schema.sql

CREATE TABLE IF NOT EXISTS pings (
    id                     INTEGER PRIMARY KEY AUTOINCREMENT,
    received_at            TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
    install_id             TEXT NOT NULL,
    version                TEXT NOT NULL DEFAULT '',
    window_start           TEXT,
    window_end             TEXT,
    captures               INTEGER NOT NULL DEFAULT 0,
    searches               INTEGER NOT NULL DEFAULT 0,
    gets                   INTEGER NOT NULL DEFAULT 0,
    errors                 INTEGER NOT NULL DEFAULT 0,
    models                 TEXT,      -- JSON: {"claude-opus-4-7": 12, "gpt-5": 4}
    clients                TEXT,      -- JSON: {"windsurf": 8, "cursor": 8}
    latency_capture_p50    REAL,
    latency_capture_p95    REAL,
    latency_capture_p99    REAL,
    latency_capture_count  INTEGER,
    latency_search_p50     REAL,
    latency_search_p95     REAL,
    latency_search_p99     REAL,
    latency_search_count   INTEGER,
    latency_get_p50        REAL,
    latency_get_p95        REAL,
    latency_get_p99        REAL,
    latency_get_count      INTEGER,
    ip_country             TEXT       -- Cloudflare cf.country (2-letter code), never IP
);

CREATE INDEX IF NOT EXISTS pings_install_id_idx ON pings(install_id);
CREATE INDEX IF NOT EXISTS pings_received_at_idx ON pings(received_at);

-- Convenience view: last 30 days rollup per install.
CREATE VIEW IF NOT EXISTS install_summary AS
SELECT
    install_id,
    COUNT(*)                          AS pings,
    MIN(received_at)                  AS first_seen,
    MAX(received_at)                  AS last_seen,
    SUM(captures)                     AS total_captures,
    SUM(searches)                     AS total_searches,
    SUM(gets)                         AS total_gets,
    SUM(errors)                       AS total_errors
FROM pings
WHERE received_at >= datetime('now', '-30 days')
GROUP BY install_id;
