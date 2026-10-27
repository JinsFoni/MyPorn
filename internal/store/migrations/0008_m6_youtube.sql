-- M6 YouTube 订阅：频道 + 视频两层。
--
-- 为什么不复用 M5 的 social_* 三张表：那边的对象是「帖子 + 照片」，这边是「频道 + 视频」，
-- 而且视频还要能下载进本地库（落到 videos / library_files），
-- 硬塞进照片墙的表里只会让两边的查询到处长 `WHERE platform <> 'youtube'`。

CREATE TABLE IF NOT EXISTS yt_channels (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    channel_id     TEXT NOT NULL UNIQUE,          -- UC + 22 位，规范化后的唯一键
    handle         TEXT NOT NULL DEFAULT '',      -- @xxx，可能为空（老频道只有 id）
    title          TEXT NOT NULL DEFAULT '',
    avatar_url     TEXT NOT NULL DEFAULT '',
    profile_url    TEXT NOT NULL DEFAULT '',
    enabled        INTEGER NOT NULL DEFAULT 1,
    performer_id   INTEGER REFERENCES performers(id) ON DELETE SET NULL,  -- 预留，本期不用
    video_count    INTEGER NOT NULL DEFAULT 0,
    unseen_count   INTEGER NOT NULL DEFAULT 0,
    backfilled_at  TEXT NOT NULL DEFAULT '',      -- 深度回填最近一次时间，空 = 没跑过
    last_error     TEXT NOT NULL DEFAULT '',
    last_synced_at TEXT NOT NULL DEFAULT '',
    created_at     TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE TABLE IF NOT EXISTS yt_videos (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    channel_id    INTEGER NOT NULL REFERENCES yt_channels(id) ON DELETE CASCADE,
    video_id      TEXT NOT NULL,                  -- 11 位，全局唯一（搬运/转发很常见）
    title         TEXT NOT NULL DEFAULT '',
    description   TEXT NOT NULL DEFAULT '',
    thumbnail_url TEXT NOT NULL DEFAULT '',
    published_at  TEXT NOT NULL DEFAULT '',       -- 空 = 不知道（深挖的老条目，见 M6 文档 §5.3）
    date_source   TEXT NOT NULL DEFAULT '',       -- rss / ytdlp / ''（未知）
    views         INTEGER NOT NULL DEFAULT 0,
    duration_sec  INTEGER NOT NULL DEFAULT 0,     -- 0 = 未知
    is_live       INTEGER NOT NULL DEFAULT 0,
    seen          INTEGER NOT NULL DEFAULT 0,
    saved         INTEGER NOT NULL DEFAULT 0,
    hidden        INTEGER NOT NULL DEFAULT 0,
    created_at    TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (video_id)
);

CREATE INDEX IF NOT EXISTS idx_yt_videos_wall ON yt_videos(published_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_yt_videos_chan ON yt_videos(channel_id, published_at DESC);
CREATE INDEX IF NOT EXISTS idx_yt_videos_unseen ON yt_videos(channel_id, seen);

-- 下载后落进 videos 表（platform='youtube'），「关注」流与 Emby 需要一个 platform 行。
INSERT OR IGNORE INTO platforms (key, name) VALUES ('youtube', 'YouTube');
