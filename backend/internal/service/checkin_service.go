package service

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"math/big"
	"time"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

var (
	// ErrDailyCheckinDisabled 签到功能未开启
	ErrDailyCheckinDisabled = infraerrors.BadRequest("DAILY_CHECKIN_DISABLED", "daily check-in feature is disabled")
	// ErrDailyCheckinAlreadyDone 今日已签到
	ErrDailyCheckinAlreadyDone = infraerrors.Conflict("DAILY_CHECKIN_ALREADY_DONE", "you have already checked in today")
)

// DailyCheckinRepository 签到记录仓储接口
type DailyCheckinRepository interface {
	// GetByUserAndDate 根据用户 ID 和业务日期查询签到记录
	GetByUserAndDate(ctx context.Context, userID int64, checkinDate string) (*DailyCheckinRecord, error)
	// Create 创建签到记录（依赖 (user_id, checkin_date) 唯一约束保证并发安全）
	Create(ctx context.Context, record *DailyCheckinRecord) error
	// GetRecentByUser 查询用户最近的签到记录（用于连续签到统计）
	GetRecentByUser(ctx context.Context, userID int64, limit int) ([]*DailyCheckinRecord, error)
}

// DailyCheckinRecord 签到记录领域模型
type DailyCheckinRecord struct {
	ID            int64
	UserID        int64
	RewardAmount  float64
	CheckinDate   string
	CheckinAt     time.Time
	CreatedAt     time.Time
}

// CheckinStatusResult 签到状态查询结果
type CheckinStatusResult struct {
	CheckedInToday bool     `json:"checked_in_today"`
	TodayDate      string   `json:"today_date"`
	RewardAmount   *float64 `json:"reward_amount,omitempty"`
	CheckinAt      *time.Time `json:"checkin_at,omitempty"`
	MinReward      float64  `json:"min_reward"`
	MaxReward      float64  `json:"max_reward"`
}

// CheckinResult 签到执行结果
type CheckinResult struct {
	RewardAmount float64   `json:"reward_amount"`
	CheckinDate  string    `json:"checkin_date"`
	CheckinAt    time.Time `json:"checkin_at"`
	NewBalance   float64   `json:"new_balance"`
}

// checkinBalanceCache 余额缓存失效接口（与 BillingCacheService.InvalidateUserBalance 签名一致）
type checkinBalanceCache interface {
	InvalidateUserBalance(ctx context.Context, userID int64) error
}

// CheckinService 每日签到服务
type CheckinService struct {
	checkinRepo           DailyCheckinRepository
	userRepo              UserRepository
	redeemRepo            RedeemCodeRepository
	settingService        *SettingService
	entClient             *dbent.Client
	authCacheInvalidator  APIKeyAuthCacheInvalidator
	billingCacheService   checkinBalanceCache
}

// NewCheckinService 创建签到服务实例
func NewCheckinService(
	checkinRepo DailyCheckinRepository,
	userRepo UserRepository,
	redeemRepo RedeemCodeRepository,
	settingService *SettingService,
	entClient *dbent.Client,
	authCacheInvalidator APIKeyAuthCacheInvalidator,
	billingCacheService *BillingCacheService,
) *CheckinService {
	svc := &CheckinService{
		checkinRepo:          checkinRepo,
		userRepo:             userRepo,
		redeemRepo:           redeemRepo,
		settingService:       settingService,
		entClient:            entClient,
		authCacheInvalidator: authCacheInvalidator,
	}
	if billingCacheService != nil {
		svc.billingCacheService = billingCacheService
	}
	return svc
}

// GetCheckinStatus 查询用户今日签到状态
func (s *CheckinService) GetCheckinStatus(ctx context.Context, userID int64) (*CheckinStatusResult, error) {
	settings, err := s.settingService.GetDailyCheckinSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("get daily checkin settings: %w", err)
	}

	todayDate := s.getBusinessDate(settings.Timezone)

	result := &CheckinStatusResult{
		CheckedInToday: false,
		TodayDate:      todayDate,
		MinReward:      settings.MinReward,
		MaxReward:      settings.MaxReward,
	}

	record, err := s.checkinRepo.GetByUserAndDate(ctx, userID, todayDate)
	if err != nil {
		// 记录不存在是正常情况，不算错误
		return result, nil
	}
	if record != nil {
		result.CheckedInToday = true
		reward := record.RewardAmount
		result.RewardAmount = &reward
		result.CheckinAt = &record.CheckinAt
	}

	return result, nil
}

// Checkin 执行每日签到
func (s *CheckinService) Checkin(ctx context.Context, userID int64) (*CheckinResult, error) {
	// 1. 检查签到功能是否开启
	settings, err := s.settingService.GetDailyCheckinSettings(ctx)
	if err != nil {
		return nil, fmt.Errorf("get daily checkin settings: %w", err)
	}
	if !settings.Enabled {
		return nil, ErrDailyCheckinDisabled
	}

	// 2. 计算业务日期（按配置时区）
	todayDate := s.getBusinessDate(settings.Timezone)

	// 3. 预检查：今日是否已签到（减少事务开销；最终安全由数据库唯一约束保证）
	existing, err := s.checkinRepo.GetByUserAndDate(ctx, userID, todayDate)
	if err != nil {
		return nil, fmt.Errorf("check existing checkin: %w", err)
	}
	if existing != nil {
		return nil, ErrDailyCheckinAlreadyDone
	}

	// 4. 生成随机奖励金额（闭区间 [min, max]）
	rewardAmount, err := generateRandomReward(settings.MinReward, settings.MaxReward)
	if err != nil {
		return nil, fmt.Errorf("generate random reward: %w", err)
	}

	// 5. 开启数据库事务：创建签到记录 + 增加余额 + 记录流水
	tx, err := s.entClient.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	txCtx := dbent.NewTxContext(ctx, tx)

	// 5a. 创建签到记录（数据库唯一约束作为并发安全的最终防线）
	now := time.Now().UTC()
	record := &DailyCheckinRecord{
		UserID:       userID,
		RewardAmount: rewardAmount,
		CheckinDate:  todayDate,
		CheckinAt:    now,
	}
	if err := s.checkinRepo.Create(txCtx, record); err != nil {
		// 唯一约束冲突 = 并发签到，返回友好错误
		return nil, ErrDailyCheckinAlreadyDone
	}

	// 5b. 增加用户余额
	if err := s.userRepo.UpdateBalance(txCtx, userID, rewardAmount); err != nil {
		return nil, fmt.Errorf("update user balance: %w", err)
	}

	// 5c. 创建余额流水记录（复用 RedeemCode 表，type = daily_checkin）
	if err := s.createCheckinRedeemRecord(txCtx, userID, rewardAmount, todayDate); err != nil {
		// 流水记录失败不影响签到结果（best-effort），仅记录日志
		logger.LegacyPrintf("service.checkin", "failed to create checkin redeem record: user_id=%d err=%v", userID, err)
	}

	// 6. 提交事务
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}

	// 7. 事务提交后异步失效缓存
	s.invalidateCaches(ctx, userID)

	// 8. 查询最新余额返回给前端
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		slog.Warn("failed to get user balance after checkin", "user_id", userID, "error", err)
		return &CheckinResult{
			RewardAmount: rewardAmount,
			CheckinDate:  todayDate,
			CheckinAt:    now,
		}, nil
	}

	return &CheckinResult{
		RewardAmount: rewardAmount,
		CheckinDate:  todayDate,
		CheckinAt:    now,
		NewBalance:   user.Balance,
	}, nil
}

// getBusinessDate 根据配置时区计算当前业务日期（YYYY-MM-DD）
func (s *CheckinService) getBusinessDate(timezoneStr string) string {
	loc, err := time.LoadLocation(timezoneStr)
	if err != nil {
		loc = time.UTC
	}
	return time.Now().In(loc).Format("2006-01-02")
}

// generateRandomReward 在闭区间 [min, max] 内生成均匀分布的随机奖励金额。
// 使用 crypto/rand 保证安全随机性，精度保留到小数点后 8 位。
func generateRandomReward(min, max float64) (float64, error) {
	// min == max 时直接返回（避免除零）
	if min == max {
		return min, nil
	}
	if max < min {
		return min, nil
	}

	// 使用 crypto/rand 生成 [0, 1) 的随机浮点数
	// 53 位精度足够表示 decimal(20,8) 的粒度
	maxInt := new(big.Int).Lsh(big.NewInt(1), 53) // 2^53
	randInt, err := rand.Int(rand.Reader, maxInt)
	if err != nil {
		return 0, fmt.Errorf("generate random int: %w", err)
	}

	// 转换为 [0, 1) 的浮点数
	randFloat := float64(randInt.Int64()) / float64(maxInt.Int64())

	// 映射到 [min, max] 闭区间
	// randFloat ∈ [0, 1)，乘以 (max - min) 后加上 min
	// 由于 randFloat 可能非常接近 1 但不等于 1，结果可能非常接近 max 但不等于 max
	// 为了实现闭区间，我们在 randFloat == 0 时返回 min（已覆盖）
	// 在 randFloat 接近 1 时四舍五入到 8 位小数即可覆盖 max
	reward := min + randFloat*(max-min)

	// 修正浮点精度误差：四舍五入到 8 位小数
	reward = roundTo8Decimals(reward)

	// 确保 reward 在 [min, max] 范围内（修正浮点误差）
	if reward < min {
		reward = min
	}
	if reward > max {
		reward = max
	}

	return reward, nil
}

// roundTo8Decimals 将浮点数四舍五入到 8 位小数
func roundTo8Decimals(v float64) float64 {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return v
	}
	pow := math.Pow(10, 8)
	return math.Round(v*pow) / pow
}

// createCheckinRedeemRecord 在 RedeemCode 表中创建签到奖励流水记录
func (s *CheckinService) createCheckinRedeemRecord(ctx context.Context, userID int64, amount float64, checkinDate string) error {
	code, err := generateCheckinCode()
	if err != nil {
		return fmt.Errorf("generate checkin code: %w", err)
	}

	record := &RedeemCode{
		Code:   code,
		Type:   RedeemTypeDailyCheckin,
		Value:  amount,
		Status: StatusUsed,
		UsedBy: &userID,
		Notes:  fmt.Sprintf("每日签到奖励 (%s)", checkinDate),
	}
	now := time.Now()
	record.UsedAt = &now

	return s.redeemRepo.Create(ctx, record)
}

// generateCheckinCode 生成唯一的签到流水码
func generateCheckinCode() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return "CHK-" + hex.EncodeToString(bytes), nil
}

// invalidateCaches 异步失效认证缓存和余额缓存
func (s *CheckinService) invalidateCaches(ctx context.Context, userID int64) {
	if s.authCacheInvalidator != nil {
		s.authCacheInvalidator.InvalidateAuthCacheByUserID(ctx, userID)
	}
	if s.billingCacheService != nil {
		go func() {
			cacheCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := s.billingCacheService.InvalidateUserBalance(cacheCtx, userID); err != nil {
				slog.Warn("invalidate user balance cache after checkin failed", "user_id", userID, "error", err)
			}
		}()
	}
}

// 确保 errors 包被使用（避免 import 未使用错误）
var _ = errors.New
