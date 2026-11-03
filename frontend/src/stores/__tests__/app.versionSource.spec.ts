import { describe, it, expect, vi, beforeEach } from 'vitest'
import { setActivePinia, createPinia } from 'pinia'
import { useAppStore } from '@/stores/app'
import { checkUpdates } from '@/api/admin/system'
import type { VersionInfo } from '@/api/admin/system'

vi.mock('@/api/admin/system', () => ({
  checkUpdates: vi.fn()
}))

const mockedCheckUpdates = vi.mocked(checkUpdates)

const UPSTREAM = 'Wei-Shaw/sub2api'
const FORK = 'fork-owner/sub2api'

function versionInfo(overrides: Partial<VersionInfo> = {}): VersionInfo {
  return {
    current_version: '0.1.176',
    latest_version: '0.1.177',
    has_update: true,
    build_type: 'release',
    cached: false,
    repo: UPSTREAM,
    available_repos: [UPSTREAM, FORK],
    ...overrides
  }
}

describe('app store update source', () => {
  beforeEach(() => {
    setActivePinia(createPinia())
    mockedCheckUpdates.mockReset()
  })

  it('passes the requested source through and records what the server used', async () => {
    mockedCheckUpdates.mockResolvedValue(versionInfo({ repo: FORK }))
    const store = useAppStore()

    await store.fetchVersion(true, FORK)

    expect(mockedCheckUpdates).toHaveBeenCalledWith(true, FORK)
    expect(store.updateRepo).toBe(FORK)
    expect(store.availableRepos).toEqual([UPSTREAM, FORK])
  })

  // Versions differ per source, so a cached result must never answer for the
  // other one — otherwise switching forks keeps showing upstream numbers.
  it('serves the cache only for the source it was fetched from', async () => {
    mockedCheckUpdates
      .mockResolvedValueOnce(versionInfo())
      .mockResolvedValueOnce(versionInfo({ repo: FORK, latest_version: '0.2.0' }))
    const store = useAppStore()

    await store.fetchVersion(false)
    expect(store.updateRepo).toBe(UPSTREAM)
    expect(store.latestVersion).toBe('0.1.177')

    await store.fetchVersion(false, FORK)
    expect(mockedCheckUpdates).toHaveBeenCalledTimes(2)
    expect(store.updateRepo).toBe(FORK)
    expect(store.latestVersion).toBe('0.2.0')

    const cached = await store.fetchVersion(false)
    expect(mockedCheckUpdates).toHaveBeenCalledTimes(2)
    expect(cached?.repo).toBe(FORK)
    expect(cached?.available_repos).toEqual([UPSTREAM, FORK])
  })
})
