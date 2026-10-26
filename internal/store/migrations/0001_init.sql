-- 0001_init.sql —— M1 初始 schema
-- 注意：PRAGMA 在 store.Open 里设置，不要在迁移事务内改动 journal_mode。

CREATE TABLE IF NOT EXISTS platforms (
    key         TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    base_url    TEXT NOT NULL DEFAULT '',
    enabled     INTEGER NOT NULL DEFAULT 1,
    proxy_url   TEXT NOT NULL DEFAULT '',
    cookie_enc  TEXT NOT NULL DEFAULT '',
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS performers (
    id               INTEGER PRIMARY KEY AUTOINCREMENT,
    platform         TEXT NOT NULL,
    performer_id     TEXT NOT NULL,
    name             TEXT NOT NULL,
    url              TEXT NOT NULL,
    avatar_url       TEXT NOT NULL DEFAULT '',
    bio              TEXT NOT NULL DEFAULT '',
    video_count      INTEGER,
    subscriber_count INTEGER,
    raw              TEXT NOT NULL DEFAULT '{}',
    last_fetched_at  TEXT,
    created_at       TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (platform, performer_id)
);

CREATE TABLE IF NOT EXISTS subscriptions (
    id                  INTEGER PRIMARY KEY AUTOINCREMENT,
    performer_id        INTEGER NOT NULL UNIQUE REFERENCES performers(id) ON DELETE CASCADE,
    enabled             INTEGER NOT NULL DEFAULT 1,
    backfill_done       INTEGER NOT NULL DEFAULT 0,
    backfill_limit      INTEGER NOT NULL DEFAULT 0,   -- 0 = 全量
    backfill_page       INTEGER NOT NULL DEFAULT 0,   -- 已完成到第几页
    backfill_total_pages INTEGER,
    last_sync_at        TEXT,
    next_sync_at        TEXT,
    last_error          TEXT NOT NULL DEFAULT '',
    deleted_at          TEXT,
    created_at          TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_subscriptions_due ON subscriptions(deleted_at, enabled, next_sync_at);

CREATE TABLE IF NOT EXISTS videos (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    platform          TEXT NOT NULL,
    platform_video_id TEXT NOT NULL,
    title             TEXT NOT NULL,
    url               TEXT NOT NULL,
    thumb_url         TEXT NOT NULL DEFAULT '',
    preview_url       TEXT NOT NULL DEFAULT '',
    duration_sec      INTEGER NOT NULL DEFAULT 0,
    published_at      TEXT,
    view_count        INTEGER,
    rating            REAL,
    tags              TEXT NOT NULL DEFAULT '[]',
    description       TEXT NOT NULL DEFAULT '',
    is_new            INTEGER NOT NULL DEFAULT 1,
    first_seen_at     TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (platform, platform_video_id)
);
CREATE INDEX IF NOT EXISTS idx_videos_published ON videos(published_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_videos_new ON videos(is_new);

CREATE TABLE IF NOT EXISTS video_performers (
    video_id     INTEGER NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
    performer_id INTEGER NOT NULL REFERENCES performers(id) ON DELETE CASCADE,
    PRIMARY KEY (video_id, performer_id)
);
CREATE INDEX IF NOT EXISTS idx_vp_performer ON video_performers(performer_id, video_id);

CREATE TABLE IF NOT EXISTS video_streams (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    video_id   INTEGER NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
    height     INTEGER NOT NULL DEFAULT 0,
    width      INTEGER NOT NULL DEFAULT 0,
    bandwidth  INTEGER NOT NULL DEFAULT 0,
    codec      TEXT NOT NULL DEFAULT '',
    url        TEXT NOT NULL,
    fetched_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (video_id, url)
);
CREATE INDEX IF NOT EXISTS idx_streams_video ON video_streams(video_id, height DESC, bandwidth DESC);

CREATE TABLE IF NOT EXISTS libraries (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    name           TEXT NOT NULL,
    root_path      TEXT NOT NULL UNIQUE,
    enabled        INTEGER NOT NULL DEFAULT 1,
    is_default     INTEGER NOT NULL DEFAULT 0,
    upload_policy  TEXT NOT NULL DEFAULT 'strm',
    openlist_path  TEXT NOT NULL DEFAULT '',
    naming_template TEXT NOT NULL DEFAULT '{title}',
    created_at     TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS jobs (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    type        TEXT NOT NULL,
    status      TEXT NOT NULL DEFAULT 'queued',
    priority    INTEGER NOT NULL DEFAULT 100,
    payload     TEXT NOT NULL DEFAULT '{}',
    progress    REAL NOT NULL DEFAULT 0,
    message     TEXT NOT NULL DEFAULT '',
    error       TEXT NOT NULL DEFAULT '',
    attempts    INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    started_at  TEXT,
    finished_at TEXT,
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
);
CREATE INDEX IF NOT EXISTS idx_jobs_claim ON jobs(status, type, priority, id);
CREATE INDEX IF NOT EXISTS idx_jobs_type ON jobs(type, created_at DESC);

CREATE TABLE IF NOT EXISTS settings (
    key        TEXT PRIMARY KEY,
    value      TEXT NOT NULL DEFAULT '',
    encrypted  INTEGER NOT NULL DEFAULT 0,
    updated_at TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS sessions (
    token      TEXT PRIMARY KEY,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    expires_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_sessions_exp ON sessions(expires_at);

CREATE TABLE IF NOT EXISTS logs (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    level      TEXT NOT NULL,
    scope      TEXT NOT NULL DEFAULT '',
    message    TEXT NOT NULL,
    meta       TEXT NOT NULL DEFAULT '{}',
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
