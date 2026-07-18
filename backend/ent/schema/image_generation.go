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

// ImageGeneration 表示一次实际的图片生成请求。
//
// 安全约束：
//   - provider_original_url 仅用于短期排障，不可作为前端长期展示地址
//   - provider_credential_id 仅管理员可见，不得返回给普通用户
//   - idempotency_key 配合 (user_id, idempotency_key) 唯一索引防重复生成
type ImageGeneration struct {
	ent.Schema
}

func (ImageGeneration) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "image_generations"},
	}
}

func (ImageGeneration) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("user_id"),
		field.Int64("conversation_id"),
		field.Int64("parent_generation_id").Optional().Nillable(),
		field.String("provider").MaxLen(32).Default("agnes"),
		field.Int64("provider_credential_id").Optional().Nillable(),
		field.String("model").MaxLen(128).Default("agnes-image-2.1-flash"),
		field.Enum("generation_type").
			Values("text_to_image", "image_to_image"),
		field.Text("prompt"),
		field.String("size").MaxLen(16).Default("2K"),
		field.String("ratio").MaxLen(16).Default("1:1"),
		field.Enum("status").
			Values("pending", "queued", "processing", "succeeded", "failed", "canceled").
			Default("pending"),
		field.String("idempotency_key").Optional().Nillable().MaxLen(255),
		field.String("provider_request_id").Optional().Nillable().MaxLen(255),
		field.String("provider_original_url").
			Optional().
			Nillable().
			MaxLen(2048).
			Comment("Short-lived upstream URL for debugging only; do not expose to end users"),
		field.String("error_code").Optional().Nillable().MaxLen(128),
		field.String("error_message").Optional().Nillable().SchemaType(map[string]string{dialect.Postgres: "text"}),
		field.Int("duration_ms").Default(0),
		field.Time("started_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
		field.Time("completed_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
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

func (ImageGeneration) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "created_at"),
		index.Fields("conversation_id", "created_at"),
		index.Fields("status"),
		index.Fields("parent_generation_id"),
		// 幂等键唯一索引：防止用户重复提交同一请求
		index.Fields("user_id", "idempotency_key").
			Unique().
			Annotations(entsql.IndexWhere("idempotency_key IS NOT NULL AND idempotency_key <> ''")),
	}
}
