-- B 站独立页面（M7 补）。
--
-- 没有新表：UP 主是 performers 的一行、投稿是 videos 的一行（理由见 internal/store/bilibili.go 顶部）。
-- 只补两个索引，都是这一页每次打开都会打到的查询：
--   1) 投稿墙：WHERE platform='bilibili' ORDER BY COALESCE(published_at,'') DESC, id DESC
--      （keyset 分页，翻到第 20 页也不该全表排序）；
--   2) 未看角标：COUNT(*) WHERE platform='bilibili' AND is_new=1，管理页与侧边栏都取它。
CREATE INDEX IF NOT EXISTS idx_videos_platform_published
    ON videos(platform, published_at DESC, id DESC);

CREATE INDEX IF NOT EXISTS idx_videos_platform_new
    ON videos(platform, is_new);
