-- B 站（以及任何 DASH 站）的音画是分两条流给的：video_streams 里那条 url 只是画面，
-- 声音在 audio_url 上。空串 = 这条流自带声音（HLS / 单文件 mp4 都是这样）。
ALTER TABLE video_streams ADD COLUMN audio_url TEXT NOT NULL DEFAULT '';
