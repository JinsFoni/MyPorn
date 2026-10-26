-- 0004_performer_profile.sql —— 艺人资料区块与站外链接
-- 资料字段是「标签: 值」的有序列表（性别、身高、出身地、发色……），各艺人条目不一致，
-- 建固定列会一直加列，所以整组存成 JSON 数组；写入时按顺序渲染。
-- 两条性癖偏好（"会勾起我性欲的是" / "会浇灭我性欲的是"）与平台统计行在上游页面里
-- 和别的资料混在一起，解析阶段就丢掉，不会进库。
ALTER TABLE performers ADD COLUMN profile_json TEXT NOT NULL DEFAULT '[]';
-- 社交链接：[{"label":"Twitter","url":"https://twitter.com/xxx"}, ...]
ALTER TABLE performers ADD COLUMN social_json TEXT NOT NULL DEFAULT '[]';
