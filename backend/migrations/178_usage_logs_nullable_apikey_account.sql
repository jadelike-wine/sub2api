-- 178_usage_logs_nullable_apikey_account.sql
-- 允许 usage_logs.api_key_id / account_id 为 NULL
--
-- 背景：AI 生图等 JWT 用户直接调用的功能不经过 API Key 网关，
-- 没有 apiKey/account 关联。之前这两个列是 NOT NULL + FK 约束，
-- 导致这类功能的 usage_log INSERT 因外键违规失败（静默丢失）。
--
-- 改为允许 NULL 后：
--   - JWT 用户直接调用的功能可写入 api_key_id=NULL, account_id=NULL
--   - ON CONFLICT (request_id, api_key_id) 对 NULL 不生效（PostgreSQL NULL != NULL），
--     这正好符合这类场景（每次 requestID 唯一，无需按 api_key_id 去重）
--   - 向后兼容：已有的非 NULL 行不受影响

ALTER TABLE usage_logs ALTER COLUMN api_key_id DROP NOT NULL;
ALTER TABLE usage_logs ALTER COLUMN account_id DROP NOT NULL;
