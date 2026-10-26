-- 身材数据：身高 + 三围，抓取时已从资料行里认出来并换算成厘米（见 internal/model/body.go）。
-- 存 JSON 对象而不是四列：不是每个站点都四项齐全，将来加臂围/罩杯也不用再迁移。
-- 空串表示「这次没解析到」，写入时据此保留库里的旧值。
ALTER TABLE performers ADD COLUMN body_json TEXT NOT NULL DEFAULT '';
