-- 0014_photo_root.sql —— 社交照片改存「设置里的图片库根目录」
--
-- 2026-11-04 用户要求：抓到的图**自动**落盘，不再由用户在墙上勾选存库；
-- 目录也从「视频库根/{平台}/{博主}/照片/{日期}_{帖子}/」改成「图片库根/{名字}/{年}/{月}/」。
-- 图片库根是单独的一项设置（`photo_root`），不再等于任何一个视频库的 root_path，
-- 所以这里要允许 `library_id = 0` 表示「图片库」，并去掉指向 libraries 的外键
-- （0 不是合法的 libraries 行，留着外键 insert 会被 PRAGMA foreign_keys 拒掉）。
--
-- SQLite 改不了列的 NOT NULL / 外键，只能重建表；老行原样搬过去（它们的 library_id
-- 指向真实存在过的视频库，只是现在不再产生这种行）。
-- UNIQUE (photo_id, library_id) 保留：0 是普通值，重复存同一张照样被挡住。

CREATE TABLE social_photo_files_new (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    photo_id   INTEGER NOT NULL REFERENCES social_photos(id) ON DELETE CASCADE,
    library_id INTEGER NOT NULL DEFAULT 0,   -- 0 = 设置里的图片库根目录
    rel_path   TEXT NOT NULL,
    size_bytes INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL DEFAULT (datetime('now')),
    UNIQUE (photo_id, library_id)
);

INSERT INTO social_photo_files_new (id, photo_id, library_id, rel_path, size_bytes, created_at)
SELECT id, photo_id, library_id, rel_path, size_bytes, created_at FROM social_photo_files;

DROP TABLE social_photo_files;
ALTER TABLE social_photo_files_new RENAME TO social_photo_files;

CREATE INDEX IF NOT EXISTS idx_social_photo_files_photo ON social_photo_files(photo_id);
