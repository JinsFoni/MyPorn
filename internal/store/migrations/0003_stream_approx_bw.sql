-- 播放快路径解析出来的码率是从文件名猜的（偏大两三倍），标记一下，
-- 下载体积预估时才不会再拿它当真值用。
ALTER TABLE video_streams ADD COLUMN approx_bw INTEGER NOT NULL DEFAULT 0;
