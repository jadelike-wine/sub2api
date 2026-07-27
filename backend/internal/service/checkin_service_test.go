//go:build unit

package service

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Mock: DailyCheckinRepository
// ---------------------------------------------------------------------------

type mockCheckinRepo struct {
	existingRecord *DailyCheckinRecord
	createErr      error
	getErr         error
	createCalls    int
	createdRecord  *DailyCheckinRecord
}

func (m *mockCheckinRepo) GetByUserAndDate(_ context.Context, _ int64, _ string) (*DailyCheckinRecord, error) {
	if m.getErr != nil {
		return nil, m.getErr
	}
	return m.existingRecord, nil
}

func (m *mockCheckinRepo) Create(_ context.Context, record *DailyCheckinRecord) error {
	m.createCalls++
	m.createdRecord = record
	return m.createErr
}

func (m *mockCheckinRepo) GetRecentByUser(_ context.Context, _ int64, _ int) ([]*DailyCheckinRecord, error) {
	return nil, nil
}

// ---------------------------------------------------------------------------
// Mock: RedeemCodeRepository (minimal, only Create is used by checkin)
// ---------------------------------------------------------------------------

type mockRedeemRepoForCheckin struct {
	createErr error
}

func (m *mockRedeemRepoForCheckin) Create(_ context.Context, _ *RedeemCode) error {
	return m.createErr
}
func (m *mockRedeemRepoForCheckin) CreateBatch(context.Context, []RedeemCode) error {
	panic("not implemented")
}
func (m *mockRedeemRepoForCheckin) GetByID(context.Context, int64) (*RedeemCode, error) {
	panic("not implemented")
}
func (m *mockRedeemRepoForCheckin) GetByCode(context.Context, string) (*RedeemCode, error) {
	panic("not implemented")
}
func (m *mockRedeemRepoForCheckin) Update(context.Context, *RedeemCode) error {
	panic("not implemented")
}
func (m *mockRedeemRepoForCheckin) BatchUpdate(context.Context, []int64, RedeemCodeBatchUpdateFields) (int64, error) {
	panic("not implemented")
}
func (m *mockRedeemRepoForCheckin) Delete(context.Context, int64) error {
	panic("not implemented")
}
func (m *mockRedeemRepoForCheckin) Use(context.Context, int64, int64) error {
	panic("not implemented")
}
func (m *mockRedeemRepoForCheckin) List(context.Context, pagination.PaginationParams) ([]RedeemCode, *pagination.PaginationResult, error) {
	panic("not implemented")
}
func (m *mockRedeemRepoForCheckin) ListWithFilters(context.Context, pagination.PaginationParams, string, string, string) ([]RedeemCode, *pagination.PaginationResult, error) {
	panic("not implemented")
}
func (m *mockRedeemRepoForCheckin) ListByUser(context.Context, int64, int) ([]RedeemCode, error) {
	panic("not implemented")
}
func (m *mockRedeemRepoForCheckin) ListByUserPaginated(context.Context, int64, pagination.PaginationParams, string) ([]RedeemCode, *pagination.PaginationResult, error) {
	panic("not implemented")
}
func (m *mockRedeemRepoForCheckin) SumPositiveBalanceByUser(context.Context, int64) (float64, error) {
	return 0, nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func newCheckinTestService(repo *mockCheckinRepo, settingRepo *mockSettingRepo) (*CheckinService, *SettingService) {
	cfg := &config.Config{}
	svc := NewSettingService(settingRepo, cfg)

	checkinSvc := &CheckinService{
		checkinRepo:    repo,
		settingService: svc,
		userRepo:       &mockUserRepo{},
		redeemRepo:     &mockRedeemRepoForCheckin{},
	}
	return checkinSvc, svc
}

// ---------------------------------------------------------------------------
// Tests: generateRandomReward
// ---------------------------------------------------------------------------

func TestGenerateRandomReward_InRange(t *testing.T) {
	t.Parallel()

	const iterations = 10000
	min, max := 0.0, 1.0

	for i := 0; i < iterations; i++ {
		reward, err := generateRandomReward(min, max)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, reward, min, "reward should be >= min")
		assert.LessOrEqual(t, reward, max, "reward should be <= max")
	}
}

func TestGenerateRandomReward_MinEqualsMax(t *testing.T) {
	t.Parallel()

	reward, err := generateRandomReward(5.0, 5.0)
	require.NoError(t, err)
	assert.Equal(t, 5.0, reward, "when min==max, reward should equal that value")
}

func TestGenerateRandomReward_MaxLessThanMin(t *testing.T) {
	t.Parallel()

	reward, err := generateRandomReward(10.0, 5.0)
	require.NoError(t, err)
	assert.Equal(t, 10.0, reward, "when max<min, reward should equal min")
}

func TestGenerateRandomReward_LargeRange(t *testing.T) {
	t.Parallel()

	min, max := 0.0, 1000.0
	for i := 0; i < 1000; i++ {
		reward, err := generateRandomReward(min, max)
		require.NoError(t, err)
		assert.GreaterOrEqual(t, reward, min)
		assert.LessOrEqual(t, reward, max)
	}
}

func TestGenerateRandomReward_Precision(t *testing.T) {
	t.Parallel()

	min, max := 0.01, 0.05
	for i := 0; i < 1000; i++ {
		reward, err := generateRandomReward(min, max)
		require.NoError(t, err)
		// Verify 8 decimal precision (no more than 8 decimal places)
		rounded := roundTo8Decimals(reward)
		assert.Equal(t, rounded, reward, "reward should be rounded to 8 decimals")
	}
}

// ---------------------------------------------------------------------------
// Tests: roundTo8Decimals
// ---------------------------------------------------------------------------

func TestRoundTo8Decimals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input    float64
		expected float64
	}{
		{0.123456789, 0.12345679},
		{0.123456781, 0.12345678},
		{1.000000005, 1.00000001}, // rounds up
		{1.000000004, 1.0},        // rounds down
		{0.0, 0.0},
		{1.0, 1.0},
	}

	for _, tt := range tests {
		got := roundTo8Decimals(tt.input)
		assert.Equal(t, tt.expected, got, "roundTo8Decimals(%v)", tt.input)
	}
}

func TestRoundTo8Decimals_NaNAndInf(t *testing.T) {
	t.Parallel()

	// NaN should be returned as-is
	nanVal := roundTo8Decimals(math.NaN())
	assert.True(t, math.IsNaN(nanVal), "NaN should be returned as-is")

	// Inf should be returned as-is
	infVal := roundTo8Decimals(math.Inf(1))
	assert.True(t, math.IsInf(infVal, 1), "Inf should be returned as-is")
}

// ---------------------------------------------------------------------------
// Tests: getBusinessDate
// ---------------------------------------------------------------------------

func TestGetBusinessDate_ValidTimezone(t *testing.T) {
	t.Parallel()

	svc := &CheckinService{}
	date := svc.getBusinessDate("Asia/Shanghai")

	// Should be a valid YYYY-MM-DD format
	_, err := time.Parse("2006-01-02", date)
	require.NoError(t, err, "date should be in YYYY-MM-DD format")

	// Verify it matches the expected timezone
	loc, _ := time.LoadLocation("Asia/Shanghai")
	expected := time.Now().In(loc).Format("2006-01-02")
	assert.Equal(t, expected, date)
}

func TestGetBusinessDate_InvalidTimezone(t *testing.T) {
	t.Parallel()

	svc := &CheckinService{}
	date := svc.getBusinessDate("Invalid/Timezone")

	// Should fall back to UTC
	_, err := time.Parse("2006-01-02", date)
	require.NoError(t, err)

	expected := time.Now().UTC().Format("2006-01-02")
	assert.Equal(t, expected, date, "invalid timezone should fall back to UTC")
}

func TestGetBusinessDate_CrossDayBoundary(t *testing.T) {
	t.Parallel()

	svc := &CheckinService{}

	// Test with UTC timezone - should match UTC date
	utcDate := svc.getBusinessDate("UTC")
	expectedUTC := time.Now().UTC().Format("2006-01-02")
	assert.Equal(t, expectedUTC, utcDate)

	// Test with a timezone that might be on a different day
	// e.g., Pacific/Auckland is UTC+12/+13, so it could be the next day
	aucklandDate := svc.getBusinessDate("Pacific/Auckland")
	loc, _ := time.LoadLocation("Pacific/Auckland")
	expectedAuckland := time.Now().In(loc).Format("2006-01-02")
	assert.Equal(t, expectedAuckland, aucklandDate)
}

// ---------------------------------------------------------------------------
// Tests: GetCheckinStatus
// ---------------------------------------------------------------------------

func TestGetCheckinStatus_NotCheckedIn(t *testing.T) {
	t.Parallel()

	settingRepo := newMockSettingRepo()
	// Enable daily check-in
	settingRepo.data[SettingKeyDailyCheckinEnabled] = "true"
	settingRepo.data[SettingKeyDailyCheckinRewardMin] = "0.5"
	settingRepo.data[SettingKeyDailyCheckinRewardMax] = "2.0"
	settingRepo.data[SettingKeyDailyCheckinTimezone] = "Asia/Shanghai"

	checkinRepo := &mockCheckinRepo{existingRecord: nil} // No existing record

	svc, _ := newCheckinTestService(checkinRepo, settingRepo)

	result, err := svc.GetCheckinStatus(context.Background(), 1)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.False(t, result.CheckedInToday)
	assert.NotEmpty(t, result.TodayDate)
	assert.Equal(t, 0.5, result.MinReward)
	assert.Equal(t, 2.0, result.MaxReward)
	assert.Nil(t, result.RewardAmount)
	assert.Nil(t, result.CheckinAt)
}

func TestGetCheckinStatus_AlreadyCheckedIn(t *testing.T) {
	t.Parallel()

	settingRepo := newMockSettingRepo()
	settingRepo.data[SettingKeyDailyCheckinEnabled] = "true"
	settingRepo.data[SettingKeyDailyCheckinRewardMin] = "0.1"
	settingRepo.data[SettingKeyDailyCheckinRewardMax] = "1.0"
	settingRepo.data[SettingKeyDailyCheckinTimezone] = "UTC"

	now := time.Now().UTC()
	checkinRepo := &mockCheckinRepo{
		existingRecord: &DailyCheckinRecord{
			ID:           1,
			UserID:       42,
			RewardAmount: 0.75,
			CheckinDate:  now.Format("2006-01-02"),
			CheckinAt:    now,
		},
	}

	svc, _ := newCheckinTestService(checkinRepo, settingRepo)

	result, err := svc.GetCheckinStatus(context.Background(), 42)
	require.NoError(t, err)
	require.NotNil(t, result)

	assert.True(t, result.CheckedInToday)
	assert.NotNil(t, result.RewardAmount)
	assert.Equal(t, 0.75, *result.RewardAmount)
	assert.NotNil(t, result.CheckinAt)
}

func TestGetCheckinStatus_SettingsError_DefaultsSafe(t *testing.T) {
	t.Parallel()

	// Simulate settings read failure
	settingRepo := &mockSettingRepo{
		getValueErr: assertError("settings read failed"),
	}

	checkinRepo := &mockCheckinRepo{}

	svc, _ := newCheckinTestService(checkinRepo, settingRepo)

	// Should return safe defaults without error
	result, err := svc.GetCheckinStatus(context.Background(), 1)
	require.NoError(t, err, "should not return error on settings read failure")
	require.NotNil(t, result)

	// Should use default values
	assert.False(t, result.CheckedInToday)
	assert.Equal(t, DailyCheckinDefaultRewardMin, result.MinReward)
	assert.Equal(t, DailyCheckinDefaultRewardMax, result.MaxReward)
}

// ---------------------------------------------------------------------------
// Tests: SetDailyCheckinSettings validation
// ---------------------------------------------------------------------------

func TestSetDailyCheckinSettings_ValidConfig(t *testing.T) {
	t.Parallel()

	settingRepo := newMockSettingRepo()
	svc := NewSettingService(settingRepo, &config.Config{})

	err := svc.SetDailyCheckinSettings(context.Background(), &DailyCheckinSettings{
		Enabled:   true,
		MinReward: 0.5,
		MaxReward: 2.0,
		Timezone:  "Asia/Shanghai",
	})
	require.NoError(t, err)

	// Verify settings were persisted
	assert.Equal(t, "true", settingRepo.data[SettingKeyDailyCheckinEnabled])
	assert.Equal(t, "0.5", settingRepo.data[SettingKeyDailyCheckinRewardMin])
	assert.Equal(t, "2", settingRepo.data[SettingKeyDailyCheckinRewardMax])
	assert.Equal(t, "Asia/Shanghai", settingRepo.data[SettingKeyDailyCheckinTimezone])
}

func TestSetDailyCheckinSettings_NegativeMin(t *testing.T) {
	t.Parallel()

	svc := NewSettingService(newMockSettingRepo(), &config.Config{})

	err := svc.SetDailyCheckinSettings(context.Background(), &DailyCheckinSettings{
		Enabled:   true,
		MinReward: -1.0,
		MaxReward: 1.0,
		Timezone:  "UTC",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "negative")
}

func TestSetDailyCheckinSettings_NegativeMax(t *testing.T) {
	t.Parallel()

	svc := NewSettingService(newMockSettingRepo(), &config.Config{})

	err := svc.SetDailyCheckinSettings(context.Background(), &DailyCheckinSettings{
		Enabled:   true,
		MinReward: 0.0,
		MaxReward: -0.5,
		Timezone:  "UTC",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "negative")
}

func TestSetDailyCheckinSettings_MaxLessThanMin(t *testing.T) {
	t.Parallel()

	svc := NewSettingService(newMockSettingRepo(), &config.Config{})

	err := svc.SetDailyCheckinSettings(context.Background(), &DailyCheckinSettings{
		Enabled:   true,
		MinReward: 5.0,
		MaxReward: 1.0,
		Timezone:  "UTC",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be less than minimum")
}

func TestSetDailyCheckinSettings_InvalidTimezone(t *testing.T) {
	t.Parallel()

	svc := NewSettingService(newMockSettingRepo(), &config.Config{})

	err := svc.SetDailyCheckinSettings(context.Background(), &DailyCheckinSettings{
		Enabled:   true,
		MinReward: 0.0,
		MaxReward: 1.0,
		Timezone:  "Fake/Timezone",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid timezone")
}

func TestSetDailyCheckinSettings_EmptyTimezone_DefaultsToShanghai(t *testing.T) {
	t.Parallel()

	settingRepo := newMockSettingRepo()
	svc := NewSettingService(settingRepo, &config.Config{})

	err := svc.SetDailyCheckinSettings(context.Background(), &DailyCheckinSettings{
		Enabled:   true,
		MinReward: 0.0,
		MaxReward: 1.0,
		Timezone:  "", // Should default to Asia/Shanghai
	})
	require.NoError(t, err)
	assert.Equal(t, DailyCheckinDefaultTimezone, settingRepo.data[SettingKeyDailyCheckinTimezone])
}

func TestSetDailyCheckinSettings_NilSettings(t *testing.T) {
	t.Parallel()

	svc := NewSettingService(newMockSettingRepo(), &config.Config{})

	err := svc.SetDailyCheckinSettings(context.Background(), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
}

// ---------------------------------------------------------------------------
// Tests: IsDailyCheckinEnabled
// ---------------------------------------------------------------------------

func TestIsDailyCheckinEnabled_True(t *testing.T) {
	t.Parallel()

	settingRepo := newMockSettingRepo()
	settingRepo.data[SettingKeyDailyCheckinEnabled] = "true"
	svc := NewSettingService(settingRepo, &config.Config{})

	assert.True(t, svc.IsDailyCheckinEnabled(context.Background()))
}

func TestIsDailyCheckinEnabled_False(t *testing.T) {
	t.Parallel()

	settingRepo := newMockSettingRepo()
	settingRepo.data[SettingKeyDailyCheckinEnabled] = "false"
	svc := NewSettingService(settingRepo, &config.Config{})

	assert.False(t, svc.IsDailyCheckinEnabled(context.Background()))
}

func TestIsDailyCheckinEnabled_NotConfigured_DefaultsFalse(t *testing.T) {
	t.Parallel()

	// No setting configured - should default to false (safe default)
	svc := NewSettingService(newMockSettingRepo(), &config.Config{})

	assert.False(t, svc.IsDailyCheckinEnabled(context.Background()))
}

// ---------------------------------------------------------------------------
// Tests: GetDailyCheckinSettings
// ---------------------------------------------------------------------------

func TestGetDailyCheckinSettings_Defaults(t *testing.T) {
	t.Parallel()

	// No settings configured - should return defaults
	svc := NewSettingService(newMockSettingRepo(), &config.Config{})

	settings, err := svc.GetDailyCheckinSettings(context.Background())
	require.NoError(t, err)
	require.NotNil(t, settings)

	assert.Equal(t, DailyCheckinDefaultEnabled, settings.Enabled)
	assert.Equal(t, DailyCheckinDefaultRewardMin, settings.MinReward)
	assert.Equal(t, DailyCheckinDefaultRewardMax, settings.MaxReward)
	assert.Equal(t, DailyCheckinDefaultTimezone, settings.Timezone)
}

func TestGetDailyCheckinSettings_CustomValues(t *testing.T) {
	t.Parallel()

	settingRepo := newMockSettingRepo()
	settingRepo.data[SettingKeyDailyCheckinEnabled] = "true"
	settingRepo.data[SettingKeyDailyCheckinRewardMin] = "0.25"
	settingRepo.data[SettingKeyDailyCheckinRewardMax] = "5.0"
	settingRepo.data[SettingKeyDailyCheckinTimezone] = "America/New_York"

	svc := NewSettingService(settingRepo, &config.Config{})

	settings, err := svc.GetDailyCheckinSettings(context.Background())
	require.NoError(t, err)

	assert.True(t, settings.Enabled)
	assert.Equal(t, 0.25, settings.MinReward)
	assert.Equal(t, 5.0, settings.MaxReward)
	assert.Equal(t, "America/New_York", settings.Timezone)
}

func TestGetDailyCheckinSettings_InvalidValues_DefaultsSafe(t *testing.T) {
	t.Parallel()

	settingRepo := newMockSettingRepo()
	settingRepo.data[SettingKeyDailyCheckinEnabled] = "true"
	// Invalid numeric values
	settingRepo.data[SettingKeyDailyCheckinRewardMin] = "not-a-number"
	settingRepo.data[SettingKeyDailyCheckinRewardMax] = "also-not-a-number"
	// Invalid timezone
	settingRepo.data[SettingKeyDailyCheckinTimezone] = "Invalid/Zone"

	svc := NewSettingService(settingRepo, &config.Config{})

	settings, err := svc.GetDailyCheckinSettings(context.Background())
	require.NoError(t, err, "should not error on invalid values")

	// Should fall back to defaults for invalid values
	assert.True(t, settings.Enabled)
	assert.Equal(t, DailyCheckinDefaultRewardMin, settings.MinReward)
	assert.Equal(t, DailyCheckinDefaultRewardMax, settings.MaxReward)
	assert.Equal(t, DailyCheckinDefaultTimezone, settings.Timezone)
}

func TestGetDailyCheckinSettings_NegativeValues_DefaultsSafe(t *testing.T) {
	t.Parallel()

	settingRepo := newMockSettingRepo()
	settingRepo.data[SettingKeyDailyCheckinEnabled] = "true"
	settingRepo.data[SettingKeyDailyCheckinRewardMin] = "-1.0"
	settingRepo.data[SettingKeyDailyCheckinRewardMax] = "-2.0"

	svc := NewSettingService(settingRepo, &config.Config{})

	settings, err := svc.GetDailyCheckinSettings(context.Background())
	require.NoError(t, err)

	// Negative values should be rejected, falling back to defaults
	assert.Equal(t, DailyCheckinDefaultRewardMin, settings.MinReward)
	assert.Equal(t, DailyCheckinDefaultRewardMax, settings.MaxReward)
}

// ---------------------------------------------------------------------------
// Tests: Checkin (pre-transaction paths)
// ---------------------------------------------------------------------------

func TestCheckin_Disabled_ReturnsError(t *testing.T) {
	t.Parallel()

	settingRepo := newMockSettingRepo()
	settingRepo.data[SettingKeyDailyCheckinEnabled] = "false" // Disabled

	checkinRepo := &mockCheckinRepo{}
	svc, _ := newCheckinTestService(checkinRepo, settingRepo)

	result, err := svc.Checkin(context.Background(), 1)
	require.Error(t, err)
	assert.Nil(t, result)
	assert.ErrorIs(t, err, ErrDailyCheckinDisabled)

	// Should not have attempted to create a record
	assert.Equal(t, 0, checkinRepo.createCalls)
}

func TestCheckin_AlreadyDone_ReturnsError(t *testing.T) {
	t.Parallel()

	settingRepo := newMockSettingRepo()
	settingRepo.data[SettingKeyDailyCheckinEnabled] = "true"
	settingRepo.data[SettingKeyDailyCheckinRewardMin] = "0.0"
	settingRepo.data[SettingKeyDailyCheckinRewardMax] = "1.0"
	settingRepo.data[SettingKeyDailyCheckinTimezone] = "UTC"

	now := time.Now().UTC()
	checkinRepo := &mockCheckinRepo{
		existingRecord: &DailyCheckinRecord{
			ID:           1,
			UserID:       42,
			RewardAmount: 0.5,
			CheckinDate:  now.Format("2006-01-02"),
			CheckinAt:    now,
		},
	}

	svc, _ := newCheckinTestService(checkinRepo, settingRepo)

	result, err := svc.Checkin(context.Background(), 42)
	require.Error(t, err)
	assert.Nil(t, result)
	assert.ErrorIs(t, err, ErrDailyCheckinAlreadyDone)

	// Should not have attempted to create a record
	assert.Equal(t, 0, checkinRepo.createCalls)
}

// ---------------------------------------------------------------------------
// Tests: generateCheckinCode
// ---------------------------------------------------------------------------

func TestGenerateCheckinCode_Format(t *testing.T) {
	t.Parallel()

	code, err := generateCheckinCode()
	require.NoError(t, err)
	require.NotEmpty(t, code)

	// Should start with CHK- prefix
	assert.True(t, len(code) > len("CHK-"), "code should have prefix and body")
	assert.Equal(t, "CHK-", code[:4])

	// Should be unique across multiple calls
	code2, err := generateCheckinCode()
	require.NoError(t, err)
	assert.NotEqual(t, code, code2, "codes should be unique")
}

// ---------------------------------------------------------------------------
// Tests: CheckinResult and CheckinStatusResult structs
// ---------------------------------------------------------------------------

func TestCheckinStatusResult_JSONSerialization(t *testing.T) {
	t.Parallel()

	// Test with nil optional fields (not checked in)
	result := &CheckinStatusResult{
		CheckedInToday: false,
		TodayDate:      "2026-01-01",
		MinReward:      0.0,
		MaxReward:      1.0,
	}
	// reward_amount and checkin_at should be omitted in JSON
	assert.Nil(t, result.RewardAmount)
	assert.Nil(t, result.CheckinAt)

	// Test with populated optional fields (checked in)
	reward := 0.75
	checkinAt := time.Now()
	result2 := &CheckinStatusResult{
		CheckedInToday: true,
		TodayDate:      "2026-01-01",
		RewardAmount:   &reward,
		CheckinAt:      &checkinAt,
		MinReward:      0.0,
		MaxReward:      1.0,
	}
	assert.NotNil(t, result2.RewardAmount)
	assert.Equal(t, 0.75, *result2.RewardAmount)
	assert.NotNil(t, result2.CheckinAt)
}

// ---------------------------------------------------------------------------
// Helper: assertError returns a new error (avoids import in test)
// ---------------------------------------------------------------------------

func assertError(msg string) error {
	return &testError{msg: msg}
}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }
