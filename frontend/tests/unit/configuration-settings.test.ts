import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, disposePinia, setActivePinia, type Pinia } from 'pinia'
import { useAdminStore } from '@/stores/admin'
import { fetchAdminSettings, updateAdminSettings } from '@/api/admin'
import { changeSessionContext } from '@/api/session-context'
import { ApiError } from '@/api/utils'
import type { ApiClient } from '@/api/client-core'

vi.mock('@/i18n', () => ({ t: (key: string) => key }))
vi.mock('@/api/admin', () => ({
  fetchAdminStats: vi.fn(),
  fetchAdminUsers: vi.fn(),
  createAdminUser: vi.fn(),
  updateAdminUser: vi.fn(),
  disableAdminUser: vi.fn(),
  resetAdminUserPassword: vi.fn(),
  fetchAdminAuditLogs: vi.fn(),
  fetchAdminSettings: vi.fn(),
  updateAdminSettings: vi.fn(),
}))
const settings = (enabled: boolean) => ({ settings: { registration_enabled: enabled } })
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => {
    resolve = done
  })
  return { promise, resolve }
}
let pinia: Pinia
beforeEach(() => {
  pinia = createPinia()
  setActivePinia(pinia)
  vi.resetAllMocks()
  changeSessionContext('https://settings.test/api/v1', 1, true)
})
afterEach(() => disposePinia(pinia))

describe('confirmed registration policy', () => {
  it('cannot save before a successful initial read or without a changed boolean', async () => {
    const store = useAdminStore()
    vi.mocked(fetchAdminSettings).mockRejectedValueOnce(new Error('offline'))
    expect(await store.loadSettings()).toBe(false)
    expect(store.settings).toBeNull()
    expect(store.settingsError).toBe('configurationSettings.loadFailed')
    expect(await store.saveSettings({ registration_enabled: false })).toBe(false)
    vi.mocked(fetchAdminSettings).mockResolvedValueOnce(settings(false))
    expect(await store.loadSettings()).toBe(true)
    expect(await store.saveSettings({ registration_enabled: false })).toBe(false)
    expect(updateAdminSettings).not.toHaveBeenCalled()
  })
  it('sends only the boolean policy and trusts the successful response', async () => {
    const store = useAdminStore()
    vi.mocked(fetchAdminSettings).mockResolvedValue(settings(false))
    await store.loadSettings()
    vi.mocked(updateAdminSettings).mockResolvedValueOnce(settings(true))
    expect(await store.saveSettings({ registration_enabled: true })).toBe(true)
    expect(updateAdminSettings).toHaveBeenLastCalledWith(settings(true))
    expect(store.settings).toEqual(settings(true).settings)
    vi.mocked(updateAdminSettings).mockResolvedValueOnce(settings(false))
    expect(await store.saveSettings({ registration_enabled: false })).toBe(true)
    expect(updateAdminSettings).toHaveBeenLastCalledWith(settings(false))
  })
  it('preserves the confirmed policy on refresh/save failure and serializes operations', async () => {
    const store = useAdminStore()
    vi.mocked(fetchAdminSettings).mockResolvedValueOnce(settings(true))
    await store.loadSettings()
    vi.mocked(fetchAdminSettings).mockRejectedValueOnce(new Error('offline'))
    await store.loadSettings()
    expect(store.settings).toEqual(settings(true).settings)
    expect(store.settingsError).toBe('configurationSettings.refreshFailed')
    const pending = deferred<ReturnType<typeof settings>>()
    vi.mocked(updateAdminSettings).mockReturnValueOnce(pending.promise)
    const save = store.saveSettings({ registration_enabled: false })
    expect(await store.loadSettings()).toBe(false)
    expect(await store.saveSettings({ registration_enabled: false })).toBe(false)
    expect(updateAdminSettings).toHaveBeenCalledTimes(1)
    pending.resolve(settings(false))
    await save
    vi.mocked(updateAdminSettings).mockRejectedValueOnce(new Error('offline'))
    expect(await store.saveSettings({ registration_enabled: true })).toBe(false)
    expect(store.settings).toEqual(settings(false).settings)
    expect(store.settingsSaveError).toBe('configurationSettings.saveFailed')
  })
  it('discards late reads, saves and finally across A → B → A sessions', async () => {
    const store = useAdminStore()
    const first = deferred<ReturnType<typeof settings>>()
    const next = deferred<ReturnType<typeof settings>>()
    vi.mocked(fetchAdminSettings)
      .mockReturnValueOnce(first.promise)
      .mockReturnValueOnce(next.promise)
    const oldRead = store.loadSettings()
    changeSessionContext('https://settings-b.test/api/v1', 2)
    changeSessionContext('https://settings.test/api/v1', 1)
    const newRead = store.loadSettings()
    first.resolve(settings(true))
    expect(await oldRead).toBe(false)
    expect(store.settingsLoading).toBe(true)
    expect(store.settings).toBeNull()
    next.resolve(settings(false))
    await newRead
    const write = deferred<ReturnType<typeof settings>>()
    vi.mocked(updateAdminSettings).mockReturnValueOnce(write.promise)
    const oldSave = store.saveSettings({ registration_enabled: true })
    changeSessionContext('https://settings-b.test/api/v1', 2)
    write.resolve(settings(true))
    expect(await oldSave).toBe(false)
    expect(store.settings).toBeNull()
    expect(store.settingsSaving).toBe(false)
    expect(store.settingsSaveError).toBeNull()
  })
  it('clears inaccessible cached policy on 403', async () => {
    const store = useAdminStore()
    vi.mocked(fetchAdminSettings).mockResolvedValueOnce(settings(true))
    await store.loadSettings()
    vi.mocked(fetchAdminSettings).mockRejectedValueOnce(new ApiError('forbidden', 403))
    await store.loadSettings()
    expect(store.settings).toBeNull()
    expect(store.settingsError).toBe('configurationSettings.accessDenied')
  })
})

describe('registration policy API response validation', () => {
  it('accepts false but refuses missing, null, and string policies', async () => {
    const api = await vi.importActual<typeof import('@/api/admin')>('@/api/admin')
    const get = vi.fn()
    const client = { GET: get, PATCH: get } as unknown as ApiClient
    get.mockResolvedValueOnce({ data: settings(false) })
    expect(await api.fetchAdminSettings(client)).toEqual(settings(false))
    for (const value of [{}, { settings: null }, { settings: { registration_enabled: 'false' } }]) {
      get.mockResolvedValueOnce({ data: value })
      await expect(api.fetchAdminSettings(client)).rejects.toThrow()
      get.mockResolvedValueOnce({ data: value })
      await expect(api.updateAdminSettings(settings(true), client)).rejects.toThrow()
    }
  })
})
