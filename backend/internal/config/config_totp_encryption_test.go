package config

import (
	"strings"
	"testing"
)

// TestValidateTotpEncryptionKey 校验 TOTP 加密密钥的格式规则。
// 这些规则在 release 模式下强制启用；密钥缺失/非 hex/字节数不对都必须返回错误。
func TestValidateTotpEncryptionKey(t *testing.T) {
	cases := []struct {
		name    string
		keyHex  string
		wantErr string
	}{
		{
			name:    "缺失密钥",
			keyHex:  "",
			wantErr: "TOTP_ENCRYPTION_KEY is required",
		},
		{
			name:    "仅空白字符视为缺失",
			keyHex:  "   \t\n",
			wantErr: "TOTP_ENCRYPTION_KEY is required",
		},
		{
			name:    "非 hex 字符",
			keyHex:  "not-a-valid-hex-string-zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
			wantErr: "TOTP_ENCRYPTION_KEY must be valid hex",
		},
		{
			name:    "字节数不足 32（仅 16 字节）",
			keyHex:  "00112233445566778899aabbccddeeff",
			wantErr: "must decode to 32 bytes",
		},
		{
			name:    "字节数超过 32（48 字节）",
			keyHex:  "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff0011223344556677",
			wantErr: "must decode to 32 bytes",
		},
		{
			name:   "合法 32 字节 hex 密钥（64 hex chars）",
			keyHex: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		},
		{
			name:   "大写 hex 合法",
			keyHex: "0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF0123456789ABCDEF",
		},
		{
			name:   "带首尾空白合法",
			keyHex: "  0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef  ",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateTotpEncryptionKey(tc.keyHex)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateTotpEncryptionKey(%q) unexpected error: %v", tc.keyHex, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateTotpEncryptionKey(%q) expected error containing %q, got nil", tc.keyHex, tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("validateTotpEncryptionKey(%q) error = %q, want substring %q", tc.keyHex, err.Error(), tc.wantErr)
			}
		})
	}
}

// TestValidateTotpEncryptionKey_NoKeyLeak 确保错误信息不会回显密钥内容。
// 这是安全要求：日志和错误信息中绝不出现密钥明文。
func TestValidateTotpEncryptionKey_NoKeyLeak(t *testing.T) {
	secret := "supersecret-key-1234567890abcdef-supersecret"
	err := validateTotpEncryptionKey(secret)
	if err == nil {
		t.Fatalf("expected error for non-hex key")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error message leaks key content: %q", err.Error())
	}
}

// TestIsStrictEncryptionMode 验证严格模式的判定规则：
//   - testing.Testing() 为 true 时（即 go test 运行环境），无论 server_mode 是什么，都不进入严格模式
//   - 因为 testing.Testing() 在测试运行时始终为 true，所以这里只能验证非严格模式的分支
//
// 完整的严格模式行为通过 TestLoad_ReleaseModeRejectsMissingTotpKey 等 integration 测试覆盖
// （需要在不带 -tags=unit 的环境下运行）。
func TestIsStrictEncryptionMode(t *testing.T) {
	// 测试环境下 testing.Testing() 始终为 true，因此严格模式永远不会触发
	// 这里验证测试模式下确实不会启用严格校验，避免本地开发误伤
	if isStrictEncryptionMode("release") {
		t.Fatalf("in test mode, isStrictEncryptionMode should return false even for release server mode")
	}
	if isStrictEncryptionMode("debug") {
		t.Fatalf("isStrictEncryptionMode should return false for debug mode")
	}
	if isStrictEncryptionMode("") {
		t.Fatalf("isStrictEncryptionMode should return false for empty mode")
	}
}

// TestIsStrictEncryptionMode_ModeMatching 验证字符串比较是大小写不敏感的。
// 测试环境下 testing.Testing() 为 true，所以这里只验证函数不会 panic 且返回 false。
func TestIsStrictEncryptionMode_ModeMatching(t *testing.T) {
	modes := []string{"release", "RELEASE", "Release", " debug ", "test", ""}
	for _, mode := range modes {
		// 在 test 环境下应始终返回 false（testing.Testing() == true）
		got := isStrictEncryptionMode(mode)
		if got {
			t.Fatalf("isStrictEncryptionMode(%q) = true in test environment, want false", mode)
		}
	}
}

// TestLoad_DevModeAutoGeneratesTotpKey 验证 development/test 模式下缺失密钥时会自动生成。
// 测试环境下 testing.Testing() == true，因此 isStrictEncryptionMode 始终返回 false，
// 走自动生成分支。验证生成的密钥是合法 hex 且 32 字节，可被 NewAESEncryptor 接受。
func TestLoad_DevModeAutoGeneratesTotpKey(t *testing.T) {
	resetViperWithJWTSecret(t)
	// 显式清空环境变量，确保走自动生成路径
	t.Setenv("TOTP_ENCRYPTION_KEY", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error in dev mode: %v", err)
	}
	if cfg.Totp.EncryptionKey == "" {
		t.Fatalf("Totp.EncryptionKey should be auto-generated in dev mode")
	}
	// 自动生成的密钥应标记为"未手动配置"，禁止启用 TOTP 功能
	if cfg.Totp.EncryptionKeyConfigured {
		t.Fatalf("EncryptionKeyConfigured should be false for auto-generated key")
	}
	// 自动生成的密钥必须能通过格式校验
	if err := validateTotpEncryptionKey(cfg.Totp.EncryptionKey); err != nil {
		t.Fatalf("auto-generated key failed validation: %v", err)
	}
}

// TestLoad_DevModeExplicitKeyValidated 验证 dev 模式下显式配置的密钥也会被校验格式。
// 避免用户在本地配置了错误格式的密钥，启动时才被 NewAESEncryptor 报错。
func TestLoad_DevModeExplicitKeyValidated(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv("TOTP_ENCRYPTION_KEY", "invalid-non-hex-key")

	_, err := Load()
	if err == nil {
		t.Fatalf("Load() should reject non-hex key even in dev mode")
	}
	if !strings.Contains(err.Error(), "TOTP_ENCRYPTION_KEY must be valid hex") {
		t.Fatalf("Load() error = %v, want substring 'TOTP_ENCRYPTION_KEY must be valid hex'", err)
	}
}

// TestLoad_DevModeExplicitValidKeyAccepted 验证 dev 模式下显式配置的合法密钥会被接受，
// 并且 EncryptionKeyConfigured 被标记为 true。
func TestLoad_DevModeExplicitValidKeyAccepted(t *testing.T) {
	resetViperWithJWTSecret(t)
	t.Setenv("TOTP_ENCRYPTION_KEY", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error in dev mode with valid key: %v", err)
	}
	if cfg.Totp.EncryptionKey != "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef" {
		t.Fatalf("Totp.EncryptionKey = %q, want configured value", cfg.Totp.EncryptionKey)
	}
	if !cfg.Totp.EncryptionKeyConfigured {
		t.Fatalf("EncryptionKeyConfigured should be true for explicitly configured key")
	}
}
