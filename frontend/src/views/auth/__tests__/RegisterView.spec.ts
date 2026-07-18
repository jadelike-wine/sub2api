import { flushPromises, mount } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import RegisterView from '@/views/auth/RegisterView.vue'

const {
  pushMock,
  showSuccessMock,
  showErrorMock,
  showWarningMock,
  registerMock,
  getPublicSettingsMock,
  validatePromoCodeMock,
  validateInvitationCodeMock,
} = vi.hoisted(() => ({
  pushMock: vi.fn(),
  showSuccessMock: vi.fn(),
  showErrorMock: vi.fn(),
  showWarningMock: vi.fn(),
  registerMock: vi.fn(),
  getPublicSettingsMock: vi.fn(),
  validatePromoCodeMock: vi.fn(),
  validateInvitationCodeMock: vi.fn(),
}))

vi.mock('vue-router', () => ({
  useRouter: () => ({
    push: pushMock,
  }),
  useRoute: () => ({
    query: {},
  }),
}))

vi.mock('vue-i18n', () => ({
  createI18n: () => ({
    global: {
      t: (key: string) => key,
    },
  }),
  useI18n: () => ({
    t: (key: string, params?: Record<string, string | number>) => {
      if (params) {
        return `${key}:${JSON.stringify(params)}`
      }
      return key
    },
    locale: { value: 'zh' },
  }),
}))

vi.mock('@/stores', () => ({
  useAuthStore: () => ({
    register: (...args: any[]) => registerMock(...args),
  }),
  useAppStore: () => ({
    showSuccess: (...args: any[]) => showSuccessMock(...args),
    showError: (...args: any[]) => showErrorMock(...args),
    showWarning: (...args: any[]) => showWarningMock(...args),
  }),
}))

vi.mock('@/api/client', () => ({
  apiClient: {
    get: vi.fn(),
    post: vi.fn(),
    put: vi.fn(),
    delete: vi.fn(),
  },
}))

vi.mock('@/api/auth', async () => {
  const actual = await vi.importActual<typeof import('@/api/auth')>('@/api/auth')
  return {
    ...actual,
    getPublicSettings: (...args: any[]) => getPublicSettingsMock(...args),
    validatePromoCode: (...args: any[]) => validatePromoCodeMock(...args),
    validateInvitationCode: (...args: any[]) => validateInvitationCodeMock(...args),
  }
})

vi.mock('@/utils/oauthAffiliate', () => ({
  clearAffiliateReferralCode: vi.fn(),
  loadAffiliateReferralCode: vi.fn(() => ''),
  resolveAffiliateReferralCode: vi.fn(() => ''),
}))

vi.mock('@/utils/authError', () => ({
  buildAuthErrorMessage: (_error: unknown, opts: { fallback: string }) => opts.fallback,
}))

const baseSettings = {
  registration_enabled: true,
  email_verify_enabled: false,
  promo_code_enabled: false,
  invitation_code_enabled: false,
  turnstile_enabled: false,
  turnstile_site_key: '',
  site_name: 'Sub2API',
  linuxdo_oauth_enabled: false,
  wechat_oauth_enabled: false,
  oidc_oauth_enabled: false,
  oidc_oauth_provider_name: 'OIDC',
  github_oauth_enabled: false,
  google_oauth_enabled: false,
  registration_email_suffix_whitelist: [] as string[],
  login_agreement_enabled: false,
}

const globalStubs = {
  AuthLayout: { template: '<div><slot /><slot name="footer" /></div>' },
  Icon: true,
  TurnstileWidget: true,
  EmailOAuthButtons: true,
  LinuxDoOAuthSection: true,
  WechatOAuthSection: true,
  OidcOAuthSection: true,
  LoginAgreementPrompt: true,
  RouterLink: true,
  transition: false,
}

function buildSettings(overrides: Partial<typeof baseSettings> = {}) {
  return { ...baseSettings, ...overrides }
}

describe('RegisterView - email domain select', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    sessionStorage.clear()
    localStorage.clear()
    registerMock.mockResolvedValue(undefined)
    validatePromoCodeMock.mockResolvedValue({ valid: false })
    validateInvitationCodeMock.mockResolvedValue({ valid: true })
  })

  it('renders username + domain select when whitelist has exact domains only', async () => {
    getPublicSettingsMock.mockResolvedValue(
      buildSettings({
        registration_email_suffix_whitelist: [
          '@qq.com',
          '@163.com',
          '@126.com',
          '@sina.com',
          '@foxmail.com',
        ],
      })
    )

    const wrapper = mount(RegisterView, { global: { stubs: globalStubs } })
    await flushPromises()

    // Select mode: a <select> element should be present
    const select = wrapper.find('select')
    expect(select.exists()).toBe(true)

    // The original full-email input should NOT be present in select mode
    const emailInput = wrapper.find('input[type="email"]')
    expect(emailInput.exists()).toBe(false)

    // Username input should be a text input with the username placeholder
    const usernameInput = wrapper.find('input[type="text"]')
    expect(usernameInput.exists()).toBe(true)
    expect(usernameInput.attributes('placeholder')).toBe('auth.emailUsernamePlaceholder')

    // All domain options should be rendered
    const options = select.findAll('option')
    expect(options).toHaveLength(5)
    expect(options[0].attributes('value')).toBe('qq.com')
    expect(options[1].attributes('value')).toBe('163.com')
    expect(options[2].attributes('value')).toBe('126.com')
    expect(options[3].attributes('value')).toBe('sina.com')
    expect(options[4].attributes('value')).toBe('foxmail.com')
  })

  it('defaults the selected domain to the first available option', async () => {
    getPublicSettingsMock.mockResolvedValue(
      buildSettings({
        registration_email_suffix_whitelist: ['@qq.com', '@163.com'],
      })
    )

    const wrapper = mount(RegisterView, { global: { stubs: globalStubs } })
    await flushPromises()

    const select = wrapper.find('select')
    expect((select.element as HTMLSelectElement).value).toBe('qq.com')
  })

  it('falls back to free email input when whitelist contains wildcards', async () => {
    getPublicSettingsMock.mockResolvedValue(
      buildSettings({
        registration_email_suffix_whitelist: ['@qq.com', '*.edu.cn'],
      })
    )

    const wrapper = mount(RegisterView, { global: { stubs: globalStubs } })
    await flushPromises()

    // Free-input mode: <select> should NOT be present
    expect(wrapper.find('select').exists()).toBe(false)

    // The original email input should be present
    const emailInput = wrapper.find('input[type="email"]')
    expect(emailInput.exists()).toBe(true)
  })

  it('falls back to free email input when whitelist is empty', async () => {
    getPublicSettingsMock.mockResolvedValue(
      buildSettings({ registration_email_suffix_whitelist: [] })
    )

    const wrapper = mount(RegisterView, { global: { stubs: globalStubs } })
    await flushPromises()

    expect(wrapper.find('select').exists()).toBe(false)
    expect(wrapper.find('input[type="email"]').exists()).toBe(true)
  })

  it('disables the submit button when username is empty in select mode', async () => {
    getPublicSettingsMock.mockResolvedValue(
      buildSettings({ registration_email_suffix_whitelist: ['@qq.com', '@163.com'] })
    )

    const wrapper = mount(RegisterView, { global: { stubs: globalStubs } })
    await flushPromises()

    const submitBtn = wrapper.find('button[type="submit"]')
    expect((submitBtn.element as HTMLButtonElement).disabled).toBe(true)
  })

  it('enables the submit button when a valid username is entered', async () => {
    getPublicSettingsMock.mockResolvedValue(
      buildSettings({ registration_email_suffix_whitelist: ['@qq.com', '@163.com'] })
    )

    const wrapper = mount(RegisterView, { global: { stubs: globalStubs } })
    await flushPromises()

    const usernameInput = wrapper.find('input[type="text"]')
    await usernameInput.setValue('zhangsan')

    const submitBtn = wrapper.find('button[type="submit"]')
    expect((submitBtn.element as HTMLButtonElement).disabled).toBe(false)
  })

  it('keeps submit disabled for invalid username characters', async () => {
    getPublicSettingsMock.mockResolvedValue(
      buildSettings({ registration_email_suffix_whitelist: ['@qq.com'] })
    )

    const wrapper = mount(RegisterView, { global: { stubs: globalStubs } })
    await flushPromises()

    const usernameInput = wrapper.find('input[type="text"]')
    // Space is not a valid local-part character
    await usernameInput.setValue('zhang san')

    const submitBtn = wrapper.find('button[type="submit"]')
    expect((submitBtn.element as HTMLButtonElement).disabled).toBe(true)
  })

  it('submits the combined email when registering', async () => {
    getPublicSettingsMock.mockResolvedValue(
      buildSettings({ registration_email_suffix_whitelist: ['@qq.com', '@163.com'] })
    )

    const wrapper = mount(RegisterView, { global: { stubs: globalStubs } })
    await flushPromises()

    const usernameInput = wrapper.find('input[type="text"]')
    await usernameInput.setValue('zhangsan')

    const select = wrapper.find('select')
    await select.setValue('163.com')

    // Fill password to pass password validation
    const passwordInput = wrapper.find('input[type="password"]')
    await passwordInput.setValue('secret123')

    await wrapper.find('form').trigger('submit.prevent')
    await flushPromises()

    expect(registerMock).toHaveBeenCalledWith(
      expect.objectContaining({
        email: 'zhangsan@163.com',
        password: 'secret123',
      })
    )
  })

  it('updates the combined email when switching domain selection', async () => {
    getPublicSettingsMock.mockResolvedValue(
      buildSettings({ registration_email_suffix_whitelist: ['@qq.com', '@163.com'] })
    )

    const wrapper = mount(RegisterView, { global: { stubs: globalStubs } })
    await flushPromises()

    const usernameInput = wrapper.find('input[type="text"]')
    await usernameInput.setValue('lisi')

    const select = wrapper.find('select')
    await select.setValue('163.com')

    const passwordInput = wrapper.find('input[type="password"]')
    await passwordInput.setValue('secret123')

    await wrapper.find('form').trigger('submit.prevent')
    await flushPromises()

    expect(registerMock).toHaveBeenCalledWith(
      expect.objectContaining({ email: 'lisi@163.com' })
    )
  })
})
