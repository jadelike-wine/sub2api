package schema

import (
	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// ImageConversation 表示一个用户的图片生成会话。
// 多轮"对话"由 Sub2API 自身维护，Agnes 接口本身是无状态的。
type ImageConversation struct {
	ent.Schema
}

func (ImageConversation) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "image_conversations"},
	}
}

func (ImageConversation) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
		mixins.SoftDeleteMixin{},
	}
}

func (ImageConversation) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("user_id"),
		field.String("title").MaxLen(200).Default(""),
		field.Time("last_message_at").
			Optional().
			Nillable().
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}),
	}
}

func (ImageConversation) Indexes() []ent.Index {
	return []ent.Index{
		// 用户隔离 + 按最近活动排序的核心索引
		index.Fields("user_id", "last_message_at"),
		index.Fields("user_id", "created_at"),
	}
}
