-- 「补元信息」补了几次还补不到，记一笔就不再每轮同步都去撞。
--
-- 为什么需要（2026-11-04 实测）：墙上一直挂着「有 3 条没有时长」，而 yt_meta 任务每轮同步
-- 都跑一遍、每轮都是「补完 0 条，3 条这次没拿到」——那 3 条是短视频/被登录墙挡住的行，
-- 观看页只剩 `playabilityStatus: LOGIN_REQUIRED` 的空壳（1.3MB、没有 lengthSeconds），
-- 补一百次也是这个结果。每轮白抓 3 个 1.3MB 页面，界面上还一直有个解释不了的缺口。
--
-- 到了 ytMetaMaxTries 次就当作「拿不到」，不计入墙上的缺口统计；用户点「补齐」
-- （force）或 Cookie 换了一份（新登录态可能就补得上）时清零重来。
ALTER TABLE yt_videos ADD COLUMN meta_tries INTEGER NOT NULL DEFAULT 0;
