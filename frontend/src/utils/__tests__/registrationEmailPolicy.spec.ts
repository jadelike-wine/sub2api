import { describe, expect, it } from 'vitest'
import {
  buildRegistrationEmail,
  formatRegistrationEmailSuffixWhitelistForMessage,
  getRegistrationEmailDomainOptions,
  isRegistrationEmailLocalPartValid,
  isRegistrationEmailSuffixAllowed,
  isRegistrationEmailSuffixDomainValid,
  normalizeRegistrationEmailSuffixDomain,
  normalizeRegistrationEmailSuffixDomains,
  normalizeRegistrationEmailSuffixWhitelist,
  parseRegistrationEmailSuffixWhitelistInput,
  shouldUseEmailDomainSelect
} from '@/utils/registrationEmailPolicy'

describe('registrationEmailPolicy utils', () => {
  it('normalizeRegistrationEmailSuffixDomain lowercases, strips @, and ignores invalid chars', () => {
    expect(normalizeRegistrationEmailSuffixDomain(' @Exa!mple.COM ')).toBe('example.com')
    expect(normalizeRegistrationEmailSuffixDomain(' *.EDU!.CN ')).toBe('*.edu.cn')
  })

  it('normalizeRegistrationEmailSuffixDomains deduplicates normalized domains', () => {
    expect(
      normalizeRegistrationEmailSuffixDomains([
        '@example.com',
        'Example.com',
        '',
        '-invalid.com',
        'foo..bar.com',
        ' @foo.bar ',
        '@foo.bar',
        '*.EDU.CN',
        '*.edu.cn'
      ])
    ).toEqual(['example.com', 'foo.bar', '*.edu.cn'])
  })

  it('parseRegistrationEmailSuffixWhitelistInput supports separators and deduplicates', () => {
    const input = '\n  @example.com,example.com，@foo.bar\t@FOO.bar *.EDU.CN  '
    expect(parseRegistrationEmailSuffixWhitelistInput(input)).toEqual([
      'example.com',
      'foo.bar',
      '*.edu.cn'
    ])
  })

  it('parseRegistrationEmailSuffixWhitelistInput drops tokens containing invalid chars', () => {
    const input = '@exa!mple.com, @foo.bar, @bad#token.com, @ok-domain.com'
    expect(parseRegistrationEmailSuffixWhitelistInput(input)).toEqual(['foo.bar', 'ok-domain.com'])
  })

  it('parseRegistrationEmailSuffixWhitelistInput drops structurally invalid domains', () => {
    const input = '@-bad.com, @foo..bar.com, @foo.bar, @xn--ok.com, *., *, *.@, *.foo'
    expect(parseRegistrationEmailSuffixWhitelistInput(input)).toEqual(['foo.bar', 'xn--ok.com'])
  })

  it('parseRegistrationEmailSuffixWhitelistInput returns empty list for blank input', () => {
    expect(parseRegistrationEmailSuffixWhitelistInput('   \n \n')).toEqual([])
  })

  it('normalizeRegistrationEmailSuffixWhitelist returns canonical @domain list', () => {
    expect(
      normalizeRegistrationEmailSuffixWhitelist([
        '@Example.com',
        'foo.bar',
        '',
        '-invalid.com',
        ' @foo.bar ',
        '*.EDU.CN'
      ])
    ).toEqual(['@example.com', '@foo.bar', '*.edu.cn'])
  })

  it('isRegistrationEmailSuffixDomainValid matches backend-compatible domain rules', () => {
    expect(isRegistrationEmailSuffixDomainValid('example.com')).toBe(true)
    expect(isRegistrationEmailSuffixDomainValid('foo-bar.example.com')).toBe(true)
    expect(isRegistrationEmailSuffixDomainValid('*.edu.cn')).toBe(true)
    expect(isRegistrationEmailSuffixDomainValid('-bad.com')).toBe(false)
    expect(isRegistrationEmailSuffixDomainValid('foo..bar.com')).toBe(false)
    expect(isRegistrationEmailSuffixDomainValid('localhost')).toBe(false)
    expect(isRegistrationEmailSuffixDomainValid('*.foo')).toBe(false)
    expect(isRegistrationEmailSuffixDomainValid('*')).toBe(false)
    expect(isRegistrationEmailSuffixDomainValid('*.@')).toBe(false)
  })

  it('isRegistrationEmailSuffixAllowed allows any email when whitelist is empty', () => {
    expect(isRegistrationEmailSuffixAllowed('user@example.com', [])).toBe(true)
  })

  it('isRegistrationEmailSuffixAllowed applies exact suffix matching', () => {
    expect(isRegistrationEmailSuffixAllowed('user@example.com', ['@example.com'])).toBe(true)
    expect(isRegistrationEmailSuffixAllowed('user@sub.example.com', ['@example.com'])).toBe(false)
    expect(isRegistrationEmailSuffixAllowed('user@qq.com', ['@qq.com'])).toBe(true)
    expect(isRegistrationEmailSuffixAllowed('user@sub.qq.com', ['@qq.com'])).toBe(false)
  })

  it('isRegistrationEmailSuffixAllowed applies wildcard suffix matching', () => {
    expect(isRegistrationEmailSuffixAllowed('student@cs.edu.cn', ['*.edu.cn'])).toBe(true)
    expect(isRegistrationEmailSuffixAllowed('student@edu.cn', ['*.edu.cn'])).toBe(true)
    expect(isRegistrationEmailSuffixAllowed('student@foo.cn', ['*.edu.cn'])).toBe(false)
  })

  it('isRegistrationEmailSuffixAllowed supports mixed exact and wildcard entries', () => {
    const whitelist = ['@a.com', '*.b.cn']
    expect(isRegistrationEmailSuffixAllowed('user@a.com', whitelist)).toBe(true)
    expect(isRegistrationEmailSuffixAllowed('user@school.b.cn', whitelist)).toBe(true)
    expect(isRegistrationEmailSuffixAllowed('user@b.cn', whitelist)).toBe(true)
    expect(isRegistrationEmailSuffixAllowed('user@c.cn', whitelist)).toBe(false)
  })

  it('formatRegistrationEmailSuffixWhitelistForMessage lists up to five entries', () => {
    expect(
      formatRegistrationEmailSuffixWhitelistForMessage(
        ['@a.com', '@b.com', '@c.com', '@d.com', '@e.com'],
        { separator: ', ', more: (count) => `and ${count} more` }
      )
    ).toBe('@a.com, @b.com, @c.com, @d.com, @e.com')
    expect(
      formatRegistrationEmailSuffixWhitelistForMessage(
        ['@a.com', '@b.com', '@c.com', '@d.com', '@e.com', '*.edu.cn', '@f.com'],
        { separator: ', ', more: (count) => `and ${count} more` }
      )
    ).toBe('@a.com, @b.com, @c.com, @d.com, @e.com, and 2 more')
  })
})

describe('getRegistrationEmailDomainOptions', () => {
  it('returns exact domains (without @) from the whitelist', () => {
    expect(
      getRegistrationEmailDomainOptions(['@qq.com', '@163.com', '126.com', 'sina.com'])
    ).toEqual(['qq.com', '163.com', '126.com', 'sina.com'])
  })

  it('excludes wildcard entries from options', () => {
    expect(
      getRegistrationEmailDomainOptions(['@qq.com', '*.edu.cn', '@163.com'])
    ).toEqual(['qq.com', '163.com'])
  })

  it('returns empty array when whitelist is empty or undefined', () => {
    expect(getRegistrationEmailDomainOptions([])).toEqual([])
    expect(getRegistrationEmailDomainOptions(null)).toEqual([])
    expect(getRegistrationEmailDomainOptions(undefined)).toEqual([])
  })

  it('normalizes raw input before extracting options', () => {
    expect(
      getRegistrationEmailDomainOptions(['@QQ.COM', ' @Foxmail.com ', '163.com'])
    ).toEqual(['qq.com', 'foxmail.com', '163.com'])
  })
})

describe('shouldUseEmailDomainSelect', () => {
  it('returns true when whitelist contains only exact domains', () => {
    expect(shouldUseEmailDomainSelect(['@qq.com', '@163.com'])).toBe(true)
    expect(shouldUseEmailDomainSelect(['qq.com', '163.com'])).toBe(true)
  })

  it('returns false when whitelist contains wildcard entries', () => {
    expect(shouldUseEmailDomainSelect(['@qq.com', '*.edu.cn'])).toBe(false)
    expect(shouldUseEmailDomainSelect(['*.edu.cn'])).toBe(false)
  })

  it('returns false when whitelist is empty or undefined', () => {
    expect(shouldUseEmailDomainSelect([])).toBe(false)
    expect(shouldUseEmailDomainSelect(null)).toBe(false)
    expect(shouldUseEmailDomainSelect(undefined)).toBe(false)
  })
})

describe('isRegistrationEmailLocalPartValid', () => {
  it('accepts typical email usernames', () => {
    expect(isRegistrationEmailLocalPartValid('zhangsan')).toBe(true)
    expect(isRegistrationEmailLocalPartValid('zhang.san')).toBe(true)
    expect(isRegistrationEmailLocalPartValid('zhang-san')).toBe(true)
    expect(isRegistrationEmailLocalPartValid('zhang_san')).toBe(true)
    expect(isRegistrationEmailLocalPartValid('zhang+san')).toBe(true)
    expect(isRegistrationEmailLocalPartValid('zhang123')).toBe(true)
    expect(isRegistrationEmailLocalPartValid('user%tag')).toBe(true)
  })

  it('rejects empty or whitespace-only input', () => {
    expect(isRegistrationEmailLocalPartValid('')).toBe(false)
    expect(isRegistrationEmailLocalPartValid('   ')).toBe(false)
    expect(isRegistrationEmailLocalPartValid(null as unknown as string)).toBe(false)
  })

  it('rejects usernames with invalid characters', () => {
    expect(isRegistrationEmailLocalPartValid('zhang@san')).toBe(false)
    expect(isRegistrationEmailLocalPartValid('zhang san')).toBe(false)
    expect(isRegistrationEmailLocalPartValid('中文用户')).toBe(false)
    expect(isRegistrationEmailLocalPartValid('zhang/san')).toBe(false)
  })

  it('rejects usernames exceeding 64 characters', () => {
    expect(isRegistrationEmailLocalPartValid('a'.repeat(64))).toBe(true)
    expect(isRegistrationEmailLocalPartValid('a'.repeat(65))).toBe(false)
  })
})

describe('buildRegistrationEmail', () => {
  it('combines username and domain with @ separator', () => {
    expect(buildRegistrationEmail('zhangsan', 'qq.com')).toBe('zhangsan@qq.com')
    expect(buildRegistrationEmail('ZhangSan', 'QQ.COM')).toBe('ZhangSan@qq.com')
  })

  it('trims whitespace from both parts', () => {
    expect(buildRegistrationEmail('  zhangsan  ', '  qq.com  ')).toBe('zhangsan@qq.com')
  })

  it('returns empty string when either part is missing', () => {
    expect(buildRegistrationEmail('', 'qq.com')).toBe('')
    expect(buildRegistrationEmail('zhangsan', '')).toBe('')
    expect(buildRegistrationEmail('', '')).toBe('')
  })
})
