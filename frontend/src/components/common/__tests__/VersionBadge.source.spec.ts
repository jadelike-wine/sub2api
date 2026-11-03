import { enableAutoUnmount, flushPromises, mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import VersionBadge from '../VersionBadge.vue'

const UPSTREAM = 'Wei-Shaw/sub2api'
const FORK = 'fork-owner/sub2api'
const STORAGE_KEY = 'sub2api.updateSourceRepo'

const stores = vi.hoisted(() => ({
  auth: { isAdmin: true },
  app: {
    versionLoading: false,
    currentVersion: '0.1.176',
    latestVersion: '0.1.176',
    hasUpdate: false,
    buildType: 'release',
    releaseInfo: null as unknown,
    updateRepo: '',
    availableRepos: [] as string[],
    fetchVersion: vi.fn(),
    clearVersionCache: vi.fn()
  }
}))

vi.mock('@/stores', () => ({
  useAuthStore: () => stores.auth,
  useAppStore: () => stores.app
}))
vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))
vi.mock('@/composables/useClipboard', () => ({
  useClipboard: () => ({ copied: false, copyToClipboard: vi.fn() })
}))
vi.mock('@/api/admin/system', () => ({
  performUpdate: vi.fn(),
  restartService: vi.fn(),
  rollback: vi.fn(),
  getRollbackVersions: vi.fn().mockResolvedValue({ versions: [] })
}))

enableAutoUnmount(afterEach)

async function openPanel() {
  const wrapper = mount(VersionBadge, {
    props: { version: '0.1.176' },
    global: { stubs: { Icon: true } }
  })
  await flushPromises()
  await wrapper.get('button').trigger('click')
  return wrapper
}

// Mimics what the real store does: remember the source the numbers came from.
function trackFetchedSource() {
  stores.app.fetchVersion.mockImplementation(async (_force?: boolean, repo?: string) => {
    const next = repo || UPSTREAM
    stores.app.updateRepo = next
    return { repo: next }
  })
}

describe('VersionBadge update source', () => {
  beforeEach(() => {
    localStorage.clear()
    stores.app.updateRepo = ''
    stores.app.availableRepos = []
    stores.app.fetchVersion.mockReset()
    trackFetchedSource()
  })

  it('hides the picker while only upstream exists', async () => {
    stores.app.availableRepos = [UPSTREAM]
    stores.app.updateRepo = UPSTREAM

    const wrapper = await openPanel()

    expect(wrapper.find('#update-source-select').exists()).toBe(false)
  })

  it('lists every configured source and re-checks on switch', async () => {
    stores.app.availableRepos = [UPSTREAM, FORK]

    const wrapper = await openPanel()
    const select = wrapper.get('#update-source-select')

    expect(select.findAll('option').map((option) => option.element.value)).toEqual([UPSTREAM, FORK])

    await select.setValue(FORK)

    expect(stores.app.fetchVersion).toHaveBeenLastCalledWith(true, FORK)
    expect(localStorage.getItem(STORAGE_KEY)).toBe(FORK)

    // An explicit upstream pick sticks too, even when update.repo defaults to the fork.
    await select.setValue(UPSTREAM)

    expect(stores.app.fetchVersion).toHaveBeenLastCalledWith(true, UPSTREAM)
    expect(localStorage.getItem(STORAGE_KEY)).toBe(UPSTREAM)
  })

  it('restores the source remembered from the previous visit', async () => {
    localStorage.setItem(STORAGE_KEY, FORK)
    stores.app.availableRepos = [UPSTREAM, FORK]

    const wrapper = await openPanel()

    expect(stores.app.fetchVersion).toHaveBeenCalledWith(false, FORK)
    expect(wrapper.get('#update-source-select').element.value).toBe(FORK)
  })

  it('falls back to the default when the remembered source disappears', async () => {
    localStorage.setItem(STORAGE_KEY, FORK)
    stores.app.fetchVersion
      .mockResolvedValueOnce(null) // backend rejects FORK: update.repo was removed
      .mockResolvedValueOnce({ repo: UPSTREAM })

    const wrapper = await openPanel()

    expect(stores.app.fetchVersion).toHaveBeenNthCalledWith(2, false, '')
    expect(localStorage.getItem(STORAGE_KEY)).toBeNull()
    expect(wrapper.find('#update-source-select').exists()).toBe(false)
  })

  it('asks rollback versions from the source that is shown', async () => {
    const { getRollbackVersions } = await import('@/api/admin/system')
    localStorage.setItem(STORAGE_KEY, FORK)
    stores.app.availableRepos = [UPSTREAM, FORK]

    const wrapper = await openPanel()
    const rollbackToggle = wrapper
      .findAll('button')
      .find((button) => button.text().includes('version.rollback'))
    expect(rollbackToggle).toBeTruthy()

    await rollbackToggle!.trigger('click')
    await flushPromises()

    expect(getRollbackVersions).toHaveBeenCalledWith(FORK)
  })
})
