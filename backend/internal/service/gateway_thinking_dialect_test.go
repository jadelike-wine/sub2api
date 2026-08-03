package service

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// ============================================================================
// isSenseNovaUpstream 测试
// ============================================================================

func TestIsSenseNovaUpstream(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    bool
	}{
		// 标准格式
		{"standard", "https://token.sensenova.cn", true},
		{"trailing slash", "https://token.sensenova.cn/", true},
		{"with path", "https://token.sensenova.cn/v1", true},
		{"with port", "https://token.sensenova.cn:443", true},
		// 大小写不敏感
		{"uppercase host", "https://TOKEN.SENSENOVA.CN", true},
		{"mixed case host", "https://Token.SenseNova.Cn", true},
		{"uppercase scheme", "HTTPS://token.sensenova.cn", true},
		// 相似域名不得匹配
		{"subdomain", "https://evil-token.sensenova.cn", false},
		{"parent domain", "https://sensenova.cn", false},
		{"suffix domain", "https://token.sensenova.cn.example.com", false},
		{"different domain", "https://api.deepseek.com", false},
		// 非法 / 空 URL
		{"empty", "", false},
		{"invalid url", "://not-a-url", false},
		{" whitespace only", "   ", false},
		// 带 whitespace 的合法 URL
		{"with whitespace", "  https://token.sensenova.cn  ", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isSenseNovaUpstream(tt.baseURL)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ============================================================================
// isNativeDeepSeekUpstream 测试
// ============================================================================

func TestIsNativeDeepSeekUpstream(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    bool
	}{
		{"standard", "https://api.deepseek.com", true},
		{"with path", "https://api.deepseek.com/v1", true},
		{"with port", "https://api.deepseek.com:443", true},
		{"uppercase", "https://API.DEEPSEEK.COM", true},
		{"sensenova", "https://token.sensenova.cn", false},
		{"other domain", "https://example.com", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isNativeDeepSeekUpstream(tt.baseURL)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ============================================================================
// ResolveThinkingDialect 测试
// ============================================================================

func TestResolveThinkingDialect(t *testing.T) {
	senseNovaAccount := &Account{
		Type:     AccountTypeAPIKey,
		Platform: PlatformAnthropic,
		Credentials: map[string]any{
			"base_url": "https://token.sensenova.cn",
		},
	}
	nativeDeepSeekAccount := &Account{
		Type:     AccountTypeAPIKey,
		Platform: PlatformAnthropic,
		Credentials: map[string]any{
			"base_url": "https://api.deepseek.com",
		},
	}
	otherUpstreamAccount := &Account{
		Type:     AccountTypeAPIKey,
		Platform: PlatformAnthropic,
		Credentials: map[string]any{
			"base_url": "https://custom-relay.example.com",
		},
	}
	defaultAnthropicAccount := &Account{
		Type:        AccountTypeAPIKey,
		Platform:    PlatformAnthropic,
		Credentials: map[string]any{},
	}

	tests := []struct {
		name        string
		account     *Account
		mappedModel string
		want        ThinkingDialect
	}{
		// SenseNova
		{"sensenova + deepseek-v4", senseNovaAccount, "deepseek-v4-flash", ThinkingDialectSenseNova},
		{"sensenova + deepseek-v4-pro", senseNovaAccount, "deepseek-v4-pro", ThinkingDialectSenseNova},
		{"sensenova + deepseek-v4 (uppercase)", senseNovaAccount, "DeepSeek-V4-Flash", ThinkingDialectSenseNova},

		// Native DeepSeek
		{"native deepseek + deepseek-v4", nativeDeepSeekAccount, "deepseek-v4-flash", ThinkingDialectNativeDeepSeek},

		// Unknown third-party upstream
		{"other upstream + deepseek-v4", otherUpstreamAccount, "deepseek-v4-flash", ThinkingDialectUnknown},
		{"default anthropic + deepseek-v4", defaultAnthropicAccount, "deepseek-v4-flash", ThinkingDialectUnknown},

		// Non-deepseek-v4 model → always Unknown
		{"sensenova + claude model", senseNovaAccount, "claude-opus-5", ThinkingDialectUnknown},
		{"native deepseek + claude model", nativeDeepSeekAccount, "claude-opus-5", ThinkingDialectUnknown},
		{"sensenova + deepseek-reasoner", senseNovaAccount, "deepseek-reasoner", ThinkingDialectUnknown},

		// Nil account
		{"nil account + deepseek-v4", nil, "deepseek-v4-flash", ThinkingDialectUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ResolveThinkingDialect(tt.account, tt.mappedModel)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ============================================================================
// NormalizeSenseNovaThinking 测试
// ============================================================================

func TestNormalizeSenseNovaThinking_AdaptiveToAuto(t *testing.T) {
	body := []byte(`{"thinking":{"type":"adaptive","budget_tokens":10000},"messages":[]}`)
	got, err := NormalizeSenseNovaThinking(body)
	require.NoError(t, err)
	assert.Equal(t, "auto", gjson.GetBytes(got, "thinking.type").String(),
		"adaptive 必须被转换为 auto")
	assert.False(t, gjson.GetBytes(got, "thinking.budget_tokens").Exists(),
		"adaptive→auto 后不应携带 budget_tokens")
}

func TestNormalizeSenseNovaThinking_AdaptiveNoBudgetTokens(t *testing.T) {
	body := []byte(`{"thinking":{"type":"adaptive"},"messages":[]}`)
	got, err := NormalizeSenseNovaThinking(body)
	require.NoError(t, err)
	assert.Equal(t, "auto", gjson.GetBytes(got, "thinking.type").String())
	assert.False(t, gjson.GetBytes(got, "thinking.budget_tokens").Exists())
}

func TestNormalizeSenseNovaThinking_AutoStripsBudgetTokens(t *testing.T) {
	body := []byte(`{"thinking":{"type":"auto","budget_tokens":10000},"messages":[]}`)
	got, err := NormalizeSenseNovaThinking(body)
	require.NoError(t, err)
	assert.Equal(t, "auto", gjson.GetBytes(got, "thinking.type").String(),
		"auto 应保持不变")
	assert.False(t, gjson.GetBytes(got, "thinking.budget_tokens").Exists(),
		"auto 模式不应携带 budget_tokens")
}

func TestNormalizeSenseNovaThinking_AutoNoBudgetTokens(t *testing.T) {
	body := []byte(`{"thinking":{"type":"auto"},"messages":[]}`)
	got, err := NormalizeSenseNovaThinking(body)
	require.NoError(t, err)
	assert.Equal(t, "auto", gjson.GetBytes(got, "thinking.type").String())
	assert.False(t, gjson.GetBytes(got, "thinking.budget_tokens").Exists())
}

func TestNormalizeSenseNovaThinking_EnabledKeepsBudgetTokens(t *testing.T) {
	body := []byte(`{"thinking":{"type":"enabled","budget_tokens":10000},"messages":[]}`)
	got, err := NormalizeSenseNovaThinking(body)
	require.NoError(t, err)
	assert.Equal(t, "enabled", gjson.GetBytes(got, "thinking.type").String(),
		"enabled 应保持不变")
	assert.Equal(t, int64(10000), gjson.GetBytes(got, "thinking.budget_tokens").Int(),
		"enabled 模式应保留 budget_tokens")
}

func TestNormalizeSenseNovaThinking_DisabledStripsBudgetTokens(t *testing.T) {
	body := []byte(`{"thinking":{"type":"disabled","budget_tokens":10000},"messages":[]}`)
	got, err := NormalizeSenseNovaThinking(body)
	require.NoError(t, err)
	assert.Equal(t, "disabled", gjson.GetBytes(got, "thinking.type").String(),
		"disabled 应保持不变")
	assert.False(t, gjson.GetBytes(got, "thinking.budget_tokens").Exists(),
		"disabled 模式不应携带 budget_tokens")
}

func TestNormalizeSenseNovaThinking_NoThinkingField(t *testing.T) {
	body := []byte(`{"messages":[]}`)
	got, err := NormalizeSenseNovaThinking(body)
	require.NoError(t, err)
	assert.False(t, gjson.GetBytes(got, "thinking").Exists(),
		"未传 thinking 时不应自动生成字段")
}

func TestNormalizeSenseNovaThinking_PreservesOtherFields(t *testing.T) {
	body := []byte(`{"thinking":{"type":"adaptive","budget_tokens":10000},"model":"deepseek-v4-flash","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`)
	got, err := NormalizeSenseNovaThinking(body)
	require.NoError(t, err)
	assert.Equal(t, "auto", gjson.GetBytes(got, "thinking.type").String())
	assert.False(t, gjson.GetBytes(got, "thinking.budget_tokens").Exists())
	assert.Equal(t, "deepseek-v4-flash", gjson.GetBytes(got, "model").String(),
		"其他字段不应丢失")
	assert.Equal(t, int64(1024), gjson.GetBytes(got, "max_tokens").Int())
}

func TestNormalizeSenseNovaThinking_NonObjectThinking(t *testing.T) {
	body := []byte(`{"thinking":"enabled","messages":[]}`)
	_, err := NormalizeSenseNovaThinking(body)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected an object")
}

func TestNormalizeSenseNovaThinking_NonStringType(t *testing.T) {
	body := []byte(`{"thinking":{"type":123},"messages":[]}`)
	_, err := NormalizeSenseNovaThinking(body)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected enabled, disabled, or auto")
}

func TestNormalizeSenseNovaThinking_NullType(t *testing.T) {
	body := []byte(`{"thinking":{"type":null},"messages":[]}`)
	_, err := NormalizeSenseNovaThinking(body)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "expected enabled, disabled, or auto")
}

func TestNormalizeSenseNovaThinking_EmptyBody(t *testing.T) {
	got, err := NormalizeSenseNovaThinking([]byte{})
	require.NoError(t, err)
	assert.Equal(t, []byte{}, got)
}

func TestNormalizeSenseNovaThinking_UnknownTypeRejected(t *testing.T) {
	tests := []string{
		`{"thinking":{"type":"high"},"messages":[]}`,
		`{"thinking":{"type":"on"},"messages":[]}`,
		`{"thinking":{"type":"true"},"messages":[]}`,
	}
	for _, body := range tests {
		_, err := NormalizeSenseNovaThinking([]byte(body))
		require.Error(t, err, "expected error for body: %s", body)
		assert.Contains(t, err.Error(), "unsupported thinking.type")
	}
}

// ============================================================================
// NormalizeDeepSeekV4ThinkingForAccount 测试（统一入口）
// ============================================================================

func TestNormalizeDeepSeekV4ThinkingForAccount_SenseNovaAdaptiveToAuto(t *testing.T) {
	account := &Account{
		Type:     AccountTypeAPIKey,
		Platform: PlatformAnthropic,
		Credentials: map[string]any{
			"base_url": "https://token.sensenova.cn",
		},
	}
	body := []byte(`{"thinking":{"type":"adaptive","budget_tokens":10000},"messages":[]}`)
	got, dialect, err := NormalizeDeepSeekV4ThinkingForAccount(account, "deepseek-v4-flash", body)
	require.NoError(t, err)
	assert.Equal(t, ThinkingDialectSenseNova, dialect)
	assert.Equal(t, "auto", gjson.GetBytes(got, "thinking.type").String())
	assert.False(t, gjson.GetBytes(got, "thinking.budget_tokens").Exists())
}

func TestNormalizeDeepSeekV4ThinkingForAccount_SenseNovaAutoStaysAuto(t *testing.T) {
	account := &Account{
		Type:     AccountTypeAPIKey,
		Platform: PlatformAnthropic,
		Credentials: map[string]any{
			"base_url": "https://token.sensenova.cn",
		},
	}
	body := []byte(`{"thinking":{"type":"auto","budget_tokens":10000},"messages":[]}`)
	got, dialect, err := NormalizeDeepSeekV4ThinkingForAccount(account, "deepseek-v4-flash", body)
	require.NoError(t, err)
	assert.Equal(t, ThinkingDialectSenseNova, dialect)
	assert.Equal(t, "auto", gjson.GetBytes(got, "thinking.type").String())
	assert.False(t, gjson.GetBytes(got, "thinking.budget_tokens").Exists())
}

func TestNormalizeDeepSeekV4ThinkingForAccount_SenseNovaEnabledUnchanged(t *testing.T) {
	account := &Account{
		Type:     AccountTypeAPIKey,
		Platform: PlatformAnthropic,
		Credentials: map[string]any{
			"base_url": "https://token.sensenova.cn",
		},
	}
	body := []byte(`{"thinking":{"type":"enabled","budget_tokens":10000},"messages":[]}`)
	got, dialect, err := NormalizeDeepSeekV4ThinkingForAccount(account, "deepseek-v4-flash", body)
	require.NoError(t, err)
	assert.Equal(t, ThinkingDialectSenseNova, dialect)
	assert.Equal(t, "enabled", gjson.GetBytes(got, "thinking.type").String())
	assert.Equal(t, int64(10000), gjson.GetBytes(got, "thinking.budget_tokens").Int(),
		"enabled 的 budget_tokens 不应被删除")
}

func TestNormalizeDeepSeekV4ThinkingForAccount_SenseNovaDisabledStripsBudget(t *testing.T) {
	account := &Account{
		Type:     AccountTypeAPIKey,
		Platform: PlatformAnthropic,
		Credentials: map[string]any{
			"base_url": "https://token.sensenova.cn",
		},
	}
	body := []byte(`{"thinking":{"type":"disabled","budget_tokens":10000},"messages":[]}`)
	got, dialect, err := NormalizeDeepSeekV4ThinkingForAccount(account, "deepseek-v4-flash", body)
	require.NoError(t, err)
	assert.Equal(t, ThinkingDialectSenseNova, dialect)
	assert.Equal(t, "disabled", gjson.GetBytes(got, "thinking.type").String())
	assert.False(t, gjson.GetBytes(got, "thinking.budget_tokens").Exists())
}

func TestNormalizeDeepSeekV4ThinkingForAccount_SenseNovaNoThinkingField(t *testing.T) {
	account := &Account{
		Type:     AccountTypeAPIKey,
		Platform: PlatformAnthropic,
		Credentials: map[string]any{
			"base_url": "https://token.sensenova.cn",
		},
	}
	body := []byte(`{"messages":[]}`)
	got, dialect, err := NormalizeDeepSeekV4ThinkingForAccount(account, "deepseek-v4-flash", body)
	require.NoError(t, err)
	assert.Equal(t, ThinkingDialectSenseNova, dialect)
	assert.False(t, gjson.GetBytes(got, "thinking").Exists())
}

// ============================================================================
// 原生 DeepSeek 回归测试（确保原有行为不变）
// ============================================================================

func TestNormalizeDeepSeekV4ThinkingForAccount_NativeDeepSeekAutoToAdaptive(t *testing.T) {
	account := &Account{
		Type:     AccountTypeAPIKey,
		Platform: PlatformAnthropic,
		Credentials: map[string]any{
			"base_url": "https://api.deepseek.com",
		},
	}
	body := []byte(`{"thinking":{"type":"auto","budget_tokens":10000},"messages":[]}`)
	got, dialect, err := NormalizeDeepSeekV4ThinkingForAccount(account, "deepseek-v4-flash", body)
	require.NoError(t, err)
	assert.Equal(t, ThinkingDialectNativeDeepSeek, dialect)
	assert.Equal(t, "adaptive", gjson.GetBytes(got, "thinking.type").String(),
		"原生 DeepSeek: auto 必须转换为 adaptive")
	assert.False(t, gjson.GetBytes(got, "thinking.budget_tokens").Exists())
}

func TestNormalizeDeepSeekV4ThinkingForAccount_NativeDeepSeekAdaptiveStaysAdaptive(t *testing.T) {
	account := &Account{
		Type:     AccountTypeAPIKey,
		Platform: PlatformAnthropic,
		Credentials: map[string]any{
			"base_url": "https://api.deepseek.com",
		},
	}
	body := []byte(`{"thinking":{"type":"adaptive","budget_tokens":10000},"messages":[]}`)
	got, dialect, err := NormalizeDeepSeekV4ThinkingForAccount(account, "deepseek-v4-flash", body)
	require.NoError(t, err)
	assert.Equal(t, ThinkingDialectNativeDeepSeek, dialect)
	assert.Equal(t, "adaptive", gjson.GetBytes(got, "thinking.type").String(),
		"原生 DeepSeek: adaptive 应保持不变")
	assert.False(t, gjson.GetBytes(got, "thinking.budget_tokens").Exists(),
		"原生 DeepSeek: adaptive 模式应删除 budget_tokens")
}

func TestNormalizeDeepSeekV4ThinkingForAccount_NativeDeepSeekEnabledUnchanged(t *testing.T) {
	account := &Account{
		Type:     AccountTypeAPIKey,
		Platform: PlatformAnthropic,
		Credentials: map[string]any{
			"base_url": "https://api.deepseek.com",
		},
	}
	body := []byte(`{"thinking":{"type":"enabled","budget_tokens":10000},"messages":[]}`)
	got, dialect, err := NormalizeDeepSeekV4ThinkingForAccount(account, "deepseek-v4-flash", body)
	require.NoError(t, err)
	assert.Equal(t, ThinkingDialectNativeDeepSeek, dialect)
	assert.Equal(t, "enabled", gjson.GetBytes(got, "thinking.type").String())
	assert.Equal(t, int64(10000), gjson.GetBytes(got, "thinking.budget_tokens").Int())
}

func TestNormalizeDeepSeekV4ThinkingForAccount_NativeDeepSeekOutputConfigEffort(t *testing.T) {
	account := &Account{
		Type:     AccountTypeAPIKey,
		Platform: PlatformAnthropic,
		Credentials: map[string]any{
			"base_url": "https://api.deepseek.com",
		},
	}
	body := []byte(`{"thinking":{"type":"adaptive"},"output_config":{"effort":"high"},"messages":[]}`)
	got, dialect, err := NormalizeDeepSeekV4ThinkingForAccount(account, "deepseek-v4-flash", body)
	require.NoError(t, err)
	assert.Equal(t, ThinkingDialectNativeDeepSeek, dialect)
	assert.Equal(t, "adaptive", gjson.GetBytes(got, "thinking.type").String())
	assert.Equal(t, "high", gjson.GetBytes(got, "reasoning_effort").String(),
		"原生 DeepSeek: output_config.effort 应转换为 reasoning_effort")
	assert.False(t, gjson.GetBytes(got, "output_config").Exists(),
		"原生 DeepSeek: output_config 应被删除")
}

// ============================================================================
// 未知第三方上游测试（保持修复前安全行为）
// ============================================================================

func TestNormalizeDeepSeekV4ThinkingForAccount_UnknownUpstreamAutoToAdaptive(t *testing.T) {
	account := &Account{
		Type:     AccountTypeAPIKey,
		Platform: PlatformAnthropic,
		Credentials: map[string]any{
			"base_url": "https://custom-relay.example.com",
		},
	}
	body := []byte(`{"thinking":{"type":"auto","budget_tokens":10000},"messages":[]}`)
	got, dialect, err := NormalizeDeepSeekV4ThinkingForAccount(account, "deepseek-v4-flash", body)
	require.NoError(t, err)
	assert.Equal(t, ThinkingDialectUnknown, dialect)
	// 未知上游保持修复前行为：auto → adaptive（原生 DeepSeek 规则）
	assert.Equal(t, "adaptive", gjson.GetBytes(got, "thinking.type").String(),
		"未知上游应保持修复前行为: auto → adaptive")
	assert.False(t, gjson.GetBytes(got, "thinking.budget_tokens").Exists())
}

func TestNormalizeDeepSeekV4ThinkingForAccount_UnknownUpstreamAdaptiveStaysAdaptive(t *testing.T) {
	account := &Account{
		Type:     AccountTypeAPIKey,
		Platform: PlatformAnthropic,
		Credentials: map[string]any{
			"base_url": "https://custom-relay.example.com",
		},
	}
	body := []byte(`{"thinking":{"type":"adaptive"},"messages":[]}`)
	got, dialect, err := NormalizeDeepSeekV4ThinkingForAccount(account, "deepseek-v4-flash", body)
	require.NoError(t, err)
	assert.Equal(t, ThinkingDialectUnknown, dialect)
	// 未知上游保持修复前行为：adaptive 不变（不会强制转 auto）
	assert.Equal(t, "adaptive", gjson.GetBytes(got, "thinking.type").String(),
		"未知上游不应强制执行 adaptive → auto")
}

func TestNormalizeDeepSeekV4ThinkingForAccount_NonDeepSeekV4ModelNoTransform(t *testing.T) {
	account := &Account{
		Type:     AccountTypeAPIKey,
		Platform: PlatformAnthropic,
		Credentials: map[string]any{
			"base_url": "https://token.sensenova.cn",
		},
	}
	body := []byte(`{"thinking":{"type":"adaptive"},"messages":[]}`)
	got, dialect, err := NormalizeDeepSeekV4ThinkingForAccount(account, "claude-opus-5", body)
	require.NoError(t, err)
	assert.Equal(t, ThinkingDialectUnknown, dialect)
	assert.Equal(t, "adaptive", gjson.GetBytes(got, "thinking.type").String(),
		"非 deepseek-v4 模型不应触发任何转换")
}
