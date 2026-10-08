import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, disposePinia, setActivePinia, type Pinia } from 'pinia'
import { useAuthStore } from '../auth'
import { changeSessionContext } from '@/api/session-context'

const mocks = vi.hoisted(() => ({ current: vi.fn(), login: vi.fn(), service: { isLocal: true } }))
vi.mock('@/stores/service', () => ({ useServiceStore: () => mocks.service }))
vi.mock('@/api/client', () => ({
  fetchCurrentUser: mocks.current,
  loginWithPassword: mocks.login,
  registerAndLogin: vi.fn(),
  clearAuthTokens: vi.fn(),
  changeCurrentUserPassword: vi.fn(),
  updateCurrentUser: vi.fn(),
  getAccessToken: vi.fn(),
  getRefreshToken: vi.fn(),
  logout: vi.fn(),
  setLocalMode: vi.fn(),
  setUnauthorizedHandler: vi.fn(),
}))

describe('bootstrap authentication races', () => {
  let pinia: Pinia
  beforeEach(() => {
    changeSessionContext('/api/v1', null, true)
    mocks.current.mockReset()
    mocks.login.mockReset()
    mocks.service.isLocal = true
    pinia = createPinia()
    setActivePinia(pinia)
  })
  afterEach(() => disposePinia(pinia))

  it.each(['success', 'failure'])(
    'late local bootstrap %s cannot clear a newer server login',
    async (outcome) => {
      let resolve!: (value: unknown) => void
      let reject!: (error: Error) => void
      mocks.current.mockReturnValue(
        new Promise((res, rej) => {
          resolve = res
          reject = rej
        }),
      )
      const auth = useAuthStore()
      const local = auth.bootstrapForMode('local')
      changeSessionContext('https://server.example/api/v1', null, true)
      mocks.service.isLocal = false
      const user = {
        id: 2,
        username: 'second',
        email: 'second@example.com',
        role: 'user',
        active: true,
      }
      mocks.login.mockResolvedValue({
        user,
        access_token: 'second-access',
        refresh_token: 'second-refresh',
      })
      await auth.login({ username: 'second', password: 'password' })
      if (outcome === 'success')
        resolve({
          id: 1,
          username: 'old-local',
          email: 'old@example.com',
          role: 'admin',
          active: true,
        })
      else reject(new Error('old-local network failure'))
      await local
      expect(auth.user).toEqual(user)
      expect(auth.accessToken).toBe('second-access')
      expect(auth.initializationError).toBeNull()
    },
  )
})
