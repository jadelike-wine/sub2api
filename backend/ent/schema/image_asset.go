package schema

import (
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// ImageAsset 保存输入和输出图片的元数据。
// 数据库只保存 S3 Object Key 和元数据，不保存 Base64，也不保存短时 Presigned URL。
//
// 安全约束：
//   - s3_key 必须校验属于当前用户目录（media/images/{user_id}/...）
//   - 同一次生成允许多个输入图片和多个输出图片
type ImageAsset struct {
	ent.Schema
}

func (ImageAsset) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "image_assets"},
	}
}

func (ImageAsset) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
		mixins.SoftDeleteMixin{},
	}
}

func (ImageAsset) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("user_id"),
		field.Int64("generation_id"),
		field.Enum("asset_type").
			Values("input", "output", "thumbnail"),
		field.String("s3_bucket").MaxLen(255),
		field.String("s3_key").MaxLen(1024),
		field.String("mime_type").MaxLen(128),
		field.Int64("file_size").Default(0),
		field.Int("width").Optional().Nillable(),
		field.Int("height").Optional().Nillable(),
		field.String("sha256").Optional().Nillable().MaxLen(64),
		field.String("original_filename").Optional().Nillable().MaxLen(255),
	}
}

func (ImageAsset) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("user_id", "created_at"),
		index.Fields("generation_id", "asset_type"),
		index.Fields("s3_key"),
		index.Fields("sha256"),
	}
}
