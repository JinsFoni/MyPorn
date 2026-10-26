-- 0002_m2_downloads.sql —— M2：下载任务与本地库文件索引
-- 注意：ALTER TABLE ADD COLUMN 非幂等，靠 schema_migrations 保证只执行一次。

-- 任务扩展：运行中取消/暂停需要标志位，进度需要速度与字节数（前端显示 ETA）
ALTER TABLE jobs ADD COLUMN cancel_requested INTEGER NOT NULL DEFAULT 0;
ALTER TABLE jobs ADD COLUMN speed_bps INTEGER NOT NULL DEFAULT 0;
ALTER TABLE jobs ADD COLUMN bytes_done INTEGER NOT NULL DEFAULT 0;
ALTER TABLE jobs ADD COLUMN bytes_total INTEGER NOT NULL DEFAULT 0;
ALTER TABLE jobs ADD COLUMN target_id INTEGER NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS idx_jobs_target ON jobs(type, target_id);

-- 本地库文件索引：一部影片在一个库里最多一份
CREATE TABLE IF NOT EXISTS library_files (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    video_id     INTEGER NOT NULL REFERENCES videos(id) ON DELETE CASCADE,
    library_id   INTEGER NOT NULL REFERENCES libraries(id) ON DELETE CASCADE,
    rel_dir      TEXT NOT NULL,
    video_name   TEXT NOT NULL DEFAULT '',
    video_path   TEXT NOT NULL DEFAULT '',
    nfo_path     TEXT NOT NULL DEFAULT '',
    poster_path  TEXT NOT NULL DEFAULT '',
    thumb_path   TEXT NOT NULL DEFAULT '',
    fanart_path  TEXT NOT NULL DEFAULT '',
    strm_path    TEXT NOT NULL DEFAULT '',
    size_bytes   INTEGER NOT NULL DEFAULT 0,
    width        INTEGER NOT NULL DEFAULT 0,
    height       INTEGER NOT NULL DEFAULT 0,
    duration_sec INTEGER NOT NULL DEFAULT 0,
    created_at   TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (video_id, library_id)
);
CREATE INDEX IF NOT EXISTS idx_lf_library ON library_files(library_id);

-- 播放进度（FR-20）：M1 只做了 is_new，这里补续播位置
ALTER TABLE videos ADD COLUMN resume_sec INTEGER NOT NULL DEFAULT 0;
