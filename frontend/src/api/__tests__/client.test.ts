import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  createApiClient,
  logoutCurrentSession,
  refreshTokenOnce,
  setLocalMode,
  setUnauthorizedHandler,
  type ApiSchemas,
} from '../client'
import { changeSessionContext, invalidateSessionViews, StaleSessionError } from '../session-context'
import {
  clearAuthTokens,
  getAccessToken,
  getRefreshToken,
  setAuthTokens,
  writeStoredApiBaseUrl,
} from '../token-storage'
import { resolveStreamUrl } from '@/composables/sseShared'

vi.mock('@/i18n', () => ({ t: (key: string) => key }))

const nextSession: ApiSchemas['AuthSession'] = {
  access_token: 'access-new',
  refresh_token: 'refresh-new',
  token_type: 'Bearer',
  expires_at: '2026-09-30T01:00:00Z',
  refresh_expires_at: '2026-10-30T01:00:00Z',
  user: { id: 1, username: 'test', email: 'test@example.com', active: true, role: 'admin' },
}
const response = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
const deferred = <T>() => {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((res) => {
    resolve = res
  })
  return { promise, resolve }
}

describe('authentication request coordination', () => {
  beforeEach(() => {
    setLocalMode(false)
    setUnauthorizedHandler(null)
    changeSessionContext('https://service.example/api/v1', 1, true)
    setAuthTokens({ access_token: 'access-old', refresh_token: 'refresh-old' })
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    clearAuthTokens()
    setUnauthorizedHandler(null)
  })

  it('waits for in-flight rotation and revokes its latest credentials without restoring login', async () => {
    const refresh = deferred<Response>()
    const fetchMock = vi
      .fn()
      .mockReturnValueOnce(refresh.promise)
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)
    const rotation = refreshTokenOnce()
    const logout = logoutCurrentSession()
    expect(getAccessToken()).toBeNull()
    expect(getRefreshToken()).toBeNull()
    const client = createApiClient({ baseUrl: 'https://service.example/api/v1' })
    await expect(client.GET('/users/me')).rejects.toBeInstanceOf(StaleSessionError)
    refresh.resolve(response(nextSession))
    await Promise.all([rotation, logout])
    expect(fetchMock).toHaveBeenCalledTimes(2)
    const [url, init] = fetchMock.mock.calls[1] as [string, RequestInit]
    expect(url).toBe('https://service.example/api/v1/auth/logout')
    expect(new Headers(init.headers).get('Authorization')).toBe('Bearer access-new')
    expect(JSON.parse(init.body as string)).toEqual({ refresh_token: 'refresh-new' })
    expect(getAccessToken()).toBeNull()
    expect(getRefreshToken()).toBeNull()
  })

  it('rebuilds logout once with rotated credentials after an expired access token', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response({ title: 'expired' }, 401))
      .mockResolvedValueOnce(response(nextSession))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)
    await logoutCurrentSession()
    expect(fetchMock).toHaveBeenCalledTimes(3)
    const [, init] = fetchMock.mock.calls[2] as [string, RequestInit]
    expect(new Headers(init.headers).get('Authorization')).toBe('Bearer access-new')
    expect(JSON.parse(init.body as string)).toEqual({ refresh_token: 'refresh-new' })
    expect(getAccessToken()).toBeNull()
  })

  it('does not refresh or replay logout indefinitely on rejected credentials', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response({ title: 'expired' }, 401))
      .mockResolvedValueOnce(response(nextSession))
      .mockResolvedValueOnce(response({ title: 'revocation failed' }, 401))
    vi.stubGlobal('fetch', fetchMock)
    await expect(logoutCurrentSession()).rejects.toThrow('revocation failed')
    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect(getAccessToken()).toBeNull()
  })

  it('keeps recoverable credentials when refresh fails temporarily', async () => {
    const unauthorized = vi.fn()
    setUnauthorizedHandler(unauthorized)
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(response({ title: 'access expired' }, 401))
      .mockResolvedValueOnce(response({ title: 'temporary outage' }, 503))
    vi.stubGlobal('fetch', fetchMock)
    const client = createApiClient({ baseUrl: 'https://service.example/api/v1' })
    await expect(client.GET('/users/me')).rejects.toThrow('temporary outage')
    expect(unauthorized).not.toHaveBeenCalled()
    expect(getAccessToken()).toBe('access-old')
    expect(getRefreshToken()).toBe('refresh-old')
  })

  it('rejects an old account response even when its transport ignores cancellation', async () => {
    const pending = deferred<Response>()
    vi.stubGlobal('fetch', vi.fn().mockReturnValue(pending.promise))
    const client = createApiClient({ baseUrl: 'https://service.example/api/v1' })
    const request = client.GET('/users/me')
    await Promise.resolve()
    changeSessionContext('https://service.example/api/v1', 2)
    pending.resolve(response(nextSession.user))
    await expect(request).rejects.toBeInstanceOf(StaleSessionError)
  })

  it.each([401, 503])('retains HTTP status for an empty refresh failure (%s)', async (status) => {
    const unauthorized = vi.fn()
    setUnauthorizedHandler(unauthorized)
    vi.stubGlobal(
      'fetch',
      vi
        .fn()
        .mockResolvedValueOnce(response({ title: 'expired' }, 401))
        .mockResolvedValueOnce(new Response(null, { status })),
    )
    const client = createApiClient({ baseUrl: 'https://service.example/api/v1' })
    await expect(client.GET('/users/me')).rejects.toMatchObject({ status })
    expect(unauthorized).toHaveBeenCalledTimes(status === 401 ? 1 : 0)
  })

  it('opens local SSE without a bearer token while server mode requires one', () => {
    writeStoredApiBaseUrl('https://service.example/api/v1/')
    clearAuthTokens()
    setLocalMode(true)
    expect(resolveStreamUrl(42)).toBe('https://service.example/api/v1/jobs/42/stream')
    setLocalMode(false)
    expect(resolveStreamUrl(42)).toBeNull()
    setAuthTokens({ access_token: 'a+b/c', refresh_token: 'refresh' })
    expect(resolveStreamUrl(42)).toBe(
      'https://service.example/api/v1/jobs/42/stream?access_token=a%2Bb%2Fc',
    )
  })

  it('preserves a shared refresh rotation while invalidating the same account views', async () => {
    const pending = deferred<Response>()
    const fetchMock = vi.fn().mockReturnValue(pending.promise)
    vi.stubGlobal('fetch', fetchMock)
    const first = refreshTokenOnce()
    invalidateSessionViews()
    const second = refreshTokenOnce()
    expect(fetchMock).toHaveBeenCalledTimes(1)
    pending.resolve(response(nextSession))
    await Promise.all([first, second])
    expect(getAccessToken()).toBe('access-new')
    expect(getRefreshToken()).toBe('refresh-new')
  })
})
