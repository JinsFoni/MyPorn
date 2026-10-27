-- 0007_m5_social.sql —— M5 社交动态（X / Instagram 照片墙）
--
-- 三张主表 + 一张落库记录表。设计要点见 docs/M5-SOCIAL.md §4：
--   * 帖子只当照片的容器与正文来源，照片是墙上的「一格」，所以照片单独成表；
--   * posted_at 冗余到照片行，照片墙的 keyset 分页只查这一列，不 join 帖子；
--   * 删博主 = 级联删记录（已存进本地库的文件不动，那是用户的资产）。
--
-- 注意：`sync_cursor` 不能叫 `cursor`（SQLite 关键字），保持这个名字。

CREATE TABLE IF NOT EXISTS social_accounts (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    platform      TEXT NOT NULL CHECK (platform IN ('x','instagram')),
    handle        TEXT NOT NULL,                      -- 不带 @，小写
    display_name  TEXT NOT NULL DEFAULT '',
    avatar_url    TEXT NOT NULL DEFAULT '',
    profile_url   TEXT NOT NULL DEFAULT '',
    performer_id  INTEGER REFERENCES performers(id) ON DELETE SET NULL,
    enabled       INTEGER NOT NULL DEFAULT 1,
    backfill_done INTEGER NOT NULL DEFAULT 0,
    sync_cursor   TEXT NOT NULL DEFAULT '',
    post_count    INTEGER NOT NULL DEFAULT 0,
    photo_count   INTEGER NOT NULL DEFAULT 0,
    last_synced_at TEXT,
    last_error    TEXT NOT NULL DEFAULT '',
    created_at    TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (platform, handle)
);

CREATE TABLE IF NOT EXISTS social_posts (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id    INTEGER NOT NULL REFERENCES social_accounts(id) ON DELETE CASCADE,
    platform      TEXT NOT NULL,
    post_id       TEXT NOT NULL,
    url           TEXT NOT NULL DEFAULT '',
    caption       TEXT NOT NULL DEFAULT '',
    posted_at     TEXT NOT NULL DEFAULT '',
    photo_count   INTEGER NOT NULL DEFAULT 0,
    first_seen_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (platform, post_id)
);

CREATE TABLE IF NOT EXISTS social_photos (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    post_id       INTEGER NOT NULL REFERENCES social_posts(id) ON DELETE CASCADE,
    account_id    INTEGER NOT NULL REFERENCES social_accounts(id) ON DELETE CASCADE,
    platform      TEXT NOT NULL,
    seq           INTEGER NOT NULL DEFAULT 0,
    url           TEXT NOT NULL,
    width         INTEGER NOT NULL DEFAULT 0,
    height        INTEGER NOT NULL DEFAULT 0,
    posted_at     TEXT NOT NULL DEFAULT '',
    hidden        INTEGER NOT NULL DEFAULT 0,
    first_seen_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (platform, url)
);

CREATE TABLE IF NOT EXISTS social_photo_files (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    photo_id   INTEGER NOT NULL REFERENCES social_photos(id) ON DELETE CASCADE,
    library_id INTEGER NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
    rel_path   TEXT NOT NULL,
    size_bytes INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (photo_id, library_id)
);

CREATE INDEX IF NOT EXISTS idx_social_photos_wall    ON social_photos(posted_at DESC, id DESC) WHERE hidden = 0;
CREATE INDEX IF NOT EXISTS idx_social_photos_account ON social_photos(account_id, posted_at DESC);
CREATE INDEX IF NOT EXISTS idx_social_posts_account  ON social_posts(account_id, posted_at DESC);
