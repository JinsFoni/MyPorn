-- 平台给的模特排名（越小越靠前）；0 表示站点没给。
-- 单独一列而不是塞进 profile_json：这是页头要单独渲染的一个数，且会随同步更新。
ALTER TABLE performers ADD COLUMN model_rank INTEGER NOT NULL DEFAULT 0;
