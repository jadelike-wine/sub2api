package schema

import (
	"time"

	"github.com/Wei-Shaw/sub2api/ent/schema/mixins"

	"entgo.io/ent"
	"entgo.io/ent/dialect"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// DailyCheckin holds the schema definition for the DailyCheckin entity.
//
// 删除策略：硬删除
// DailyCheckin 使用硬删除而非软删除，原因如下：
//   - 签到记录具有一次性特性，每天最多一条，无需软删除历史
//   - 通过 (user_id, checkin_date) 唯一约束保证数据完整性
//   - 保持表结构简洁
type DailyCheckin struct {
	ent.Schema
}

func (DailyCheckin) Annotations() []schema.Annotation {
	return []schema.Annotation{
		entsql.Annotation{Table: "daily_checkins"},
	}
}

func (DailyCheckin) Mixin() []ent.Mixin {
	return []ent.Mixin{
		mixins.TimeMixin{},
	}
}

func (DailyCheckin) Fields() []ent.Field {
	return []ent.Field{
		field.Int64("user_id").
			Comment("签到用户 ID"),
		field.Float("reward_amount").
			SchemaType(map[string]string{dialect.Postgres: "decimal(20,8)"}).
			Default(0).
			Comment("签到奖励金额"),
		field.String("checkin_date").
			MaxLen(10).
			Comment("业务日期，格式 YYYY-MM-DD，按配置时区计算"),
		field.Time("checkin_at").
			Default(time.Now).
			SchemaType(map[string]string{dialect.Postgres: "timestamptz"}).
			Comment("签到时间戳"),
	}
}

func (DailyCheckin) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("user", User.Type).
			Ref("daily_checkins").
			Field("user_id").
			Unique().
			Required(),
	}
}

func (DailyCheckin) Indexes() []ent.Index {
	return []ent.Index{
		// 唯一约束：同一用户同一业务日期最多一条签到记录
		index.Fields("user_id", "checkin_date").Unique(),
		index.Fields("checkin_date"),
	}
}
