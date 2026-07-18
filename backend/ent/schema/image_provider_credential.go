package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// ImageProviderCredential 保存上游图片生成 provider 的凭据（如 Agnes API Key）。
//
// 安全约束：
//   - api_key 必须加密保存（api_key_encrypted），明文不得入库
//   - key_fingerprint 仅展示末尾四位，用于后台识别
//   - 任何日志不得打印完整 api_key
type ImageProviderCredential struct {
	ent.Schema
}

func (ImageProviderCredential) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "image_provider_credentials"},
	}
}

func (ImageProviderCredential) Fields() []ent.Field {
	return []ent.Field{
		field.String("name").NotEmpty().MaxLen(100),
		field.Enum("provider").
			Values("agnes"),
		field.String("api_key_encrypted").
			NotEmpty().
			Sensitive().
			Comment("AES-256-GCM encrypted upstream API key"),
		field.String("key_fingerprint").
			NotEmpty().
			MaxLen(32).
			Comment("Masked fingerprint, e.g. last 4 chars, for backend identification only"),
		field.Bool("enabled").Default(true),
		field.Int("priority").Default(50).Comment("Lower value = higher priority"),
		field.Int("weight").Default(1).Range(1, 100).Comment("Weight for weighted round robin"),
		field.Enum("status").
			Values("healthy", "unhealthy", "disabled").
			Default("healthy"),
		field.Int("consecutive_failures").Default(0),
		field.Time("last_used_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("last_success_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("last_failure_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("cooldown_until").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.String("last_error_code").Optional().Nillable().MaxLen(128),
		field.String("last_error_message").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.Time("created_at").
			Immutable().
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("updated_at").
			Default(time.Now).
			UpdateDefault(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (ImageProviderCredential) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("provider", "enabled", "status"),
		index.Fields("priority"),
		index.Fields("cooldown_until"),
	}
}
