-- AI 图片生成功能：上游凭据池、用户会话、生成任务、图片资产
-- 见 ent/schema/image_provider_credential.go / image_conversation.go / image_generation.go / image_asset.go

CREATE TABLE IF NOT EXISTS image_provider_credentials (
    id BIGSERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    provider VARCHAR(32) NOT NULL,
    api_key_encrypted TEXT NOT NULL,
    key_fingerprint VARCHAR(32) NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    priority INTEGER NOT NULL DEFAULT 50,
    weight INTEGER NOT NULL DEFAULT 1,
    status VARCHAR(32) NOT NULL DEFAULT 'healthy',
    consecutive_failures INTEGER NOT NULL DEFAULT 0,
    last_used_at TIMESTAMPTZ,
    last_success_at TIMESTAMPTZ,
    last_failure_at TIMESTAMPTZ,
    cooldown_until TIMESTAMPTZ,
    last_error_code VARCHAR(128),
    last_error_message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS image_provider_credentials_provider_enabled_status_idx
    ON image_provider_credentials (provider, enabled, status);
CREATE INDEX IF NOT EXISTS image_provider_credentials_priority_idx
    ON image_provider_credentials (priority);
CREATE INDEX IF NOT EXISTS image_provider_credentials_cooldown_until_idx
    ON image_provider_credentials (cooldown_until);

CREATE TABLE IF NOT EXISTS image_conversations (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL,
    title VARCHAR(200) NOT NULL DEFAULT '',
    last_message_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS image_conversations_user_last_message_at_idx
    ON image_conversations (user_id, last_message_at);
CREATE INDEX IF NOT EXISTS image_conversations_user_created_at_idx
    ON image_conversations (user_id, created_at);

CREATE TABLE IF NOT EXISTS image_generations (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL,
    conversation_id BIGINT NOT NULL,
    parent_generation_id BIGINT,
    provider VARCHAR(32) NOT NULL DEFAULT 'agnes',
    provider_credential_id BIGINT,
    model VARCHAR(128) NOT NULL DEFAULT 'agnes-image-2.1-flash',
    generation_type VARCHAR(32) NOT NULL,
    prompt TEXT NOT NULL,
    size VARCHAR(16) NOT NULL DEFAULT '2K',
    ratio VARCHAR(16) NOT NULL DEFAULT '1:1',
    status VARCHAR(32) NOT NULL DEFAULT 'pending',
    idempotency_key VARCHAR(255),
    provider_request_id VARCHAR(255),
    provider_original_url VARCHAR(2048),
    error_code VARCHAR(128),
    error_message TEXT,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    started_at TIMESTAMPTZ,
    completed_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS image_generations_user_created_at_idx
    ON image_generations (user_id, created_at);
CREATE INDEX IF NOT EXISTS image_generations_conversation_created_at_idx
    ON image_generations (conversation_id, created_at);
CREATE INDEX IF NOT EXISTS image_generations_status_idx
    ON image_generations (status);
CREATE INDEX IF NOT EXISTS image_generations_parent_generation_id_idx
    ON image_generations (parent_generation_id);
CREATE UNIQUE INDEX IF NOT EXISTS image_generations_user_idempotency_key_uq
    ON image_generations (user_id, idempotency_key)
    WHERE idempotency_key IS NOT NULL AND idempotency_key <> '';

CREATE TABLE IF NOT EXISTS image_assets (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL,
    generation_id BIGINT NOT NULL,
    asset_type VARCHAR(32) NOT NULL,
    s3_bucket VARCHAR(255) NOT NULL,
    s3_key VARCHAR(1024) NOT NULL,
    mime_type VARCHAR(128) NOT NULL,
    file_size BIGINT NOT NULL DEFAULT 0,
    width INTEGER,
    height INTEGER,
    sha256 VARCHAR(64),
    original_filename VARCHAR(255),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ
);

CREATE INDEX IF NOT EXISTS image_assets_user_created_at_idx
    ON image_assets (user_id, created_at);
CREATE INDEX IF NOT EXISTS image_assets_generation_asset_type_idx
    ON image_assets (generation_id, asset_type);
CREATE INDEX IF NOT EXISTS image_assets_s3_key_idx
    ON image_assets (s3_key);
CREATE INDEX IF NOT EXISTS image_assets_sha256_idx
    ON image_assets (sha256);
