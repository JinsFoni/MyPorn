-- xHamster 影片 ID 统一成「URL 末尾那一段」，并把历史上因此产生的重复行合并掉。
--
-- 背景：同一部片子站点会给两种地址——
--   /videos/they-had-a-naughty-threesome-…-xhW3Ivw   （带 slug，规范地址）
--   /videos/xhW3Ivw                                  （裸 hash，301 跳到上面那条）
-- 老代码把「/videos/ 后面整段」当 ID，于是两种地址在库里各成一条记录，
-- 关注页上就是两张一模一样的卡片。实测库里的现场（2026-11-01 量的）：
--   Hot Pearl  543 行裸 hash + 542 行带 slug，mysaaat 42 行裸 hash + 42 行带 slug，
--   另有 2 对老式数字 ID（14840346、6042558）的同类重复，合计 586 行该合并。
--
-- 保留哪一条：保留**带 slug 的那条**（更早入库，带着清晰度缓存、艺人关联、已看标记），
-- 把裸 hash 那行上的关联搬过来之后删掉它，最后把保留行的 ID 改成规范 ID
-- （也就是原来裸 hash 那行的值）。UNIQUE(platform, platform_video_id) 不会撞——
-- 重复行是在改 ID 之前删掉的。
--
-- 只会跑一遍：迁移表记着名字。新代码也不会再产生两种 ID（见 videoIDFromURL）。

CREATE TEMP TABLE xh_dup_map AS
SELECT b.id AS dup_id, k.id AS keep_id
  FROM videos b
  JOIN videos k
    ON k.platform = b.platform
   AND k.id <> b.id
   AND k.platform_video_id GLOB '*-' || b.platform_video_id
 WHERE b.platform = 'xhamster'
   AND ( (b.platform_video_id GLOB 'xh*' AND b.platform_video_id NOT GLOB '*-*')
      OR (b.platform_video_id NOT GLOB '*[^0-9]*' AND length(b.platform_video_id) >= 6) );

-- 艺人关联、清晰度缓存先搬到保留行（唯一键冲突的直接忽略：保留行自己那份更好）
INSERT OR IGNORE INTO video_performers(video_id, performer_id)
SELECT m.keep_id, vp.performer_id
  FROM video_performers vp JOIN xh_dup_map m ON m.dup_id = vp.video_id;

INSERT OR IGNORE INTO video_streams(video_id, height, width, bandwidth, codec, url, fetched_at, approx_bw, audio_url)
SELECT m.keep_id, s.height, s.width, s.bandwidth, s.codec, s.url, s.fetched_at, s.approx_bw, s.audio_url
  FROM video_streams s JOIN xh_dup_map m ON m.dup_id = s.video_id;

DELETE FROM video_performers WHERE video_id IN (SELECT dup_id FROM xh_dup_map);
DELETE FROM video_streams   WHERE video_id IN (SELECT dup_id FROM xh_dup_map);
DELETE FROM videos          WHERE id       IN (SELECT dup_id FROM xh_dup_map);

-- 保留行的 ID 改成 URL 末尾那段：rtrim 把结尾的字母数字全削掉，
-- 剩下的长度就是最后一个 '-' 的位置（裸 hash 没有 '-'，算出来是 0，原样保留）。
UPDATE videos
   SET platform_video_id = substr(platform_video_id,
        length(rtrim(platform_video_id,
          'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789')) + 1)
 WHERE platform = 'xhamster'
   AND platform_video_id <> substr(platform_video_id,
        length(rtrim(platform_video_id,
          'abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789')) + 1);

DROP TABLE xh_dup_map;
