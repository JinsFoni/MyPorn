-- 有些频道上传的文件本身就是「躺着拍」的：像素是横的、容器里没有任何 rotation 元数据
-- （实测 4kaamg 的 iEZ_quzVcOE：ffprobe 256×144 无 rotate/side_data，yt-dlp 41 个 format 里
-- rotation 全是 null，连 YouTube 自己生成的 maxresdefault 封面也是躺着的）。
-- 这种片子任何播放器都只能照着横的放，唯一的办法是让用户点一次「旋转」，然后**记住**。
--
-- 记在视频这一层而不是频道层：同一个频道里横竖混着传（实测 10 张封面里 6 张躺着、4 张正常）。
-- 单位是度，只取 0 / 90 / 180 / 270（顺时针，等于 CSS rotate()）；0 = 不旋转。
ALTER TABLE yt_videos ADD COLUMN rotate_deg INTEGER NOT NULL DEFAULT 0;
