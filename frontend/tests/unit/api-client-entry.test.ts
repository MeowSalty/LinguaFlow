import { afterEach, describe, expect, it, vi } from 'vitest'
import {
  apiClient,
  clearAuthTokens,
  clearStoredApiBaseUrl,
  getAccessToken,
  getRefreshToken,
  loginWithPassword,
  logout,
  setApiBaseUrl,
  setAuthTokens,
  setLocalMode,
} from '../../src/api/client'
import { fetchCurrentUser } from '../../src/api/projects'

const user = { id: 1, username: 'tester', email: 'test@example.com', active: true, role: 'user' }
const json = (body: unknown) =>
  new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } })

afterEach(() => {
  vi.unstubAllGlobals()
  clearAuthTokens()
  setLocalMode(false)
  setApiBaseUrl('/api/v1')
  clearStoredApiBaseUrl()
})

describe('public API entry compatibility', () => {
  it('uses the current service and credentials through both business APIs and retained client references', async () => {
    const fetchMock = vi.fn().mockImplementation(async () => json(user))
    vi.stubGlobal('fetch', fetchMock)
    setLocalMode(false)
    setApiBaseUrl('https://first.example/api/v1')
    setAuthTokens({ access_token: 'first-access', refresh_token: 'first-refresh' })
    const retainedClient = apiClient

    expect(await fetchCurrentUser()).toEqual(user)
    const first = fetchMock.mock.calls[0]?.[0] as Request
    expect(first.url).toBe('https://first.example/api/v1/users/me')
    expect(first.headers.get('Authorization')).toBe('Bearer first-access')

    setApiBaseUrl('https://second.example/api/v1')
    expect(getAccessToken()).toBeNull()
    expect(getRefreshToken()).toBeNull()
    await retainedClient.GET('/users/me')
    const second = fetchMock.mock.calls[1]?.[0] as Request
    expect(second.url).toBe('https://second.example/api/v1/users/me')
    expect(second.headers.has('Authorization')).toBe(false)
    expect(await fetchCurrentUser()).toEqual(user)
    expect(fetchMock.mock.calls[2]?.[0]).toHaveProperty('url', second.url)
  })

  it('shares login and logout state through the public auth exports', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(json({ user, access_token: 'access', refresh_token: 'refresh' }))
      .mockResolvedValueOnce(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)
    setLocalMode(false)
    setApiBaseUrl('https://auth.example/api/v1')

    await loginWithPassword({ username: 'tester', password: 'password' })
    expect(getAccessToken()).toBe('access')
    expect(getRefreshToken()).toBe('refresh')
    await logout()

    const [url, init] = fetchMock.mock.calls[1] as [string, RequestInit]
    expect(url).toBe('https://auth.example/api/v1/auth/logout')
    expect(new Headers(init.headers).get('Authorization')).toBe('Bearer access')
    expect(JSON.parse(init.body as string)).toEqual({ refresh_token: 'refresh' })
    expect(getAccessToken()).toBeNull()
    expect(getRefreshToken()).toBeNull()
  })
})
