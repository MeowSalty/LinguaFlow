import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, disposePinia, setActivePinia, type Pinia } from 'pinia'
import { useAuthStore } from '@/stores/auth'
import { registerAndLogin } from '@/api/client'
import { captureSession, changeSessionContext, isSessionCurrent } from '@/api/session-context'

vi.mock('@/api/client', () => ({
  clearAuthTokens: vi.fn(),
  registerAndLogin: vi.fn(),
  setLocalMode: vi.fn(),
  setUnauthorizedHandler: vi.fn(),
  getAccessToken: vi.fn(),
  getRefreshToken: vi.fn(),
}))
vi.mock('@/stores/service', () => ({ useServiceStore: () => ({ isLocal: false }) }))
let pinia: Pinia
beforeEach(() => {
  pinia = createPinia()
  setActivePinia(pinia)
  changeSessionContext('https://register.test/api/v1', null, true)
})
afterEach(() => disposePinia(pinia))
const payload = { username: 'new-user', email: 'new@example.com', password: 'password' }
const session = {
  user: { id: 7, username: 'new-user', email: 'new@example.com', role: 'user', active: true },
  access_token: 'registered-access',
  refresh_token: 'registered-refresh',
  token_type: 'Bearer',
  expires_at: '2026-10-01T12:00:00Z',
  refresh_expires_at: '2026-10-30T12:00:00Z',
}
describe('registration success session', () => {
  it('returns the applied session snapshot for guarded page effects', async () => {
    vi.mocked(registerAndLogin).mockResolvedValueOnce(session)
    const auth = useAuthStore()
    const result = await auth.register(payload)
    expect(result).toEqual(captureSession())
    expect(isSessionCurrent(result)).toBe(true)
    expect(auth.user?.id).toBe(7)
    changeSessionContext('https://other.test/api/v1', null)
    expect(isSessionCurrent(result)).toBe(false)
  })
  it('refuses an old service response without authenticating a replacement session', async () => {
    let resolve!: (value: typeof session) => void
    vi.mocked(registerAndLogin).mockReturnValueOnce(
      new Promise((done) => {
        resolve = done
      }),
    )
    const auth = useAuthStore()
    const pending = auth.register(payload)
    changeSessionContext('https://other.test/api/v1', null)
    resolve(session)
    await expect(pending).rejects.toHaveProperty('name', 'StaleSessionError')
    expect(auth.user).toBeNull()
  })
})
