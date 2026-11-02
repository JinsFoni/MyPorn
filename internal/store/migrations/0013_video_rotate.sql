-- 「躺着拍」的片子在任何播放器里都只能照着横的放（像素本身是横的、容器里没有任何 rotation
-- 元数据，ffprobe 也读不出来，见 0012_yt_video_rotate.sql），只能让用户点一次「旋转」然后**记住**。
--
-- 角度存在 videos 这一层（而不是 library_files）：转不转是**画面内容**的属性，和它落在哪个库、
-- 落成哪个文件名无关；同一部片下到两个库里不该有两个角度。
-- 单位是度，只取 0 / 90 / 180 / 270（顺时针，等于 CSS rotate()）；0 = 不旋转。
ALTER TABLE videos ADD COLUMN rotate_deg INTEGER NOT NULL DEFAULT 0;
