package repository

import (
	"context"
	"fmt"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/dailycheckin"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// dailyCheckinRepository 实现 service.DailyCheckinRepository 接口。
type dailyCheckinRepository struct {
	client *dbent.Client
}

// NewDailyCheckinRepository 创建签到记录仓储实例。
func NewDailyCheckinRepository(client *dbent.Client) service.DailyCheckinRepository {
	return &dailyCheckinRepository{client: client}
}

// GetByUserAndDate 根据用户 ID 和业务日期查询签到记录。
// 未找到记录时返回 (nil, nil)，不返回错误。
func (r *dailyCheckinRepository) GetByUserAndDate(ctx context.Context, userID int64, checkinDate string) (*service.DailyCheckinRecord, error) {
	record, err := r.client.DailyCheckin.Query().
		Where(
			dailycheckin.UserIDEQ(userID),
			dailycheckin.CheckinDateEQ(checkinDate),
		).
		Only(ctx)
	if err != nil {
		if dbent.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("query daily checkin: %w", err)
	}
	return entToModel(record), nil
}

// Create 创建签到记录。
// 依赖 (user_id, checkin_date) 数据库唯一约束保证并发安全：
// 并发插入时第二个请求会收到唯一约束冲突错误。
func (r *dailyCheckinRepository) Create(ctx context.Context, record *service.DailyCheckinRecord) error {
	builder := r.client.DailyCheckin.Create().
		SetUserID(record.UserID).
		SetRewardAmount(record.RewardAmount).
		SetCheckinDate(record.CheckinDate).
		SetCheckinAt(record.CheckinAt)

	saved, err := builder.Save(ctx)
	if err != nil {
		return fmt.Errorf("create daily checkin: %w", err)
	}
	record.ID = saved.ID
	record.CreatedAt = saved.CreatedAt
	return nil
}

// GetRecentByUser 查询用户最近的签到记录（按签到时间倒序）。
func (r *dailyCheckinRepository) GetRecentByUser(ctx context.Context, userID int64, limit int) ([]*service.DailyCheckinRecord, error) {
	if limit <= 0 {
		limit = 30
	}
	records, err := r.client.DailyCheckin.Query().
		Where(dailycheckin.UserIDEQ(userID)).
		Order(dbent.Desc(dailycheckin.FieldCheckinAt)).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query recent daily checkins: %w", err)
	}

	result := make([]*service.DailyCheckinRecord, len(records))
	for i, r := range records {
		result[i] = entToModel(r)
	}
	return result, nil
}

// entToModel 将 ent DailyCheckin 实体转换为领域模型。
func entToModel(e *dbent.DailyCheckin) *service.DailyCheckinRecord {
	if e == nil {
		return nil
	}
	return &service.DailyCheckinRecord{
		ID:           e.ID,
		UserID:       e.UserID,
		RewardAmount: e.RewardAmount,
		CheckinDate:  e.CheckinDate,
		CheckinAt:    e.CheckinAt,
		CreatedAt:    e.CreatedAt,
	}
}
