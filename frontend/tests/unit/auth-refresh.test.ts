import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import {
  createApiClient,
  logoutCurrentSession,
  recoverUnauthorizedResponse,
  refreshStrategy,
  refreshTokenOnce,
  setLocalMode,
  setUnauthorizedHandler,
  type ApiSchemas,
} from '@/api/client-core'
import { refreshAuthSession } from '@/api/auth'
import {
  authTokenStorageKeys,
  clearAuthTokens,
  clearRefreshAttempt,
  getAccessToken,
  getAuthEpoch,
  getRefreshToken,
  readRefreshAttempt,
  readStoredAuthTokens,
  setAuthSession,
  setAuthTokens,
  writeRefreshAttempt,
} from '@/api/token-storage'
import { captureSession, changeSessionContext, StaleSessionError } from '@/api/session-context'

vi.mock('@/i18n', () => ({ t: (key: string) => key }))

const BASE_URL = 'https://service.example/api/v1'
const nextSession: ApiSchemas['AuthSession'] = {
  access_token: 'access-new',
  refresh_token: 'refresh-new',
  token_type: 'Bearer',
  expires_at: '2026-09-30T01:00:00Z',
  refresh_expires_at: '2026-10-30T01:00:00Z',
  user: { id: 1, username: 'test', email: 'test@example.com', active: true, role: 'admin' },
}
const json = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } })
const deferred = <T>() => {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((res) => {
    resolve = res
  })
  return { promise, resolve }
}
const browserStorage = (seed: Record<string, string> = {}) => {
  const store = new Map(Object.entries(seed))
  vi.stubGlobal('window', {
    localStorage: {
      getItem: (key: string) => store.get(key) ?? null,
      setItem: (key: string, value: string) => {
        store.set(key, value)
      },
      removeItem: (key: string) => {
        store.delete(key)
      },
    },
  })
  return store
}

beforeEach(() => {
  setLocalMode(false)
  setUnauthorizedHandler(null)
  changeSessionContext(BASE_URL, 1, true)
  setAuthTokens({ access_token: 'access-old', refresh_token: 'refresh-old' })
  clearRefreshAttempt()
})
afterEach(() => {
  vi.unstubAllGlobals()
  clearRefreshAttempt()
  clearAuthTokens()
  setUnauthorizedHandler(null)
})

describe('auth epoch', () => {
  it('keeps the login epoch stable across rotation and rotates it only when cleared', () => {
    const epoch = getAuthEpoch()
    setAuthSession(nextSession)
    expect(getAuthEpoch()).toBe(epoch)
    clearAuthTokens()
    expect(getAuthEpoch()).not.toBe(epoch)
  })

  it('observes an epoch another tab rotated in shared storage', () => {
    const store = browserStorage()
    const epoch = getAuthEpoch()
    store.set(authTokenStorageKeys.authEpoch, 'epoch-from-another-tab')
    expect(getAuthEpoch()).toBe('epoch-from-another-tab')
    expect(epoch).not.toBe('epoch-from-another-tab')
  })

  it('does not resurrect tokens another tab removed from shared storage', () => {
    expect(getAccessToken()).toBe('access-old')
    browserStorage()
    expect(getAccessToken()).toBeNull()
    expect(getRefreshToken()).toBeNull()
  })
})

describe('refresh coordination', () => {
  it('routes the public refreshAuthSession through the single in-tab flight', async () => {
    const pending = deferred<Response>()
    const fetchMock = vi.fn().mockReturnValue(pending.promise)
    vi.stubGlobal('fetch', fetchMock)
    const viaFacade = refreshAuthSession()
    const viaCoordinator = refreshTokenOnce()
    expect(fetchMock).toHaveBeenCalledTimes(1)
    pending.resolve(json(nextSession))
    await expect(viaFacade).resolves.toEqual({
      access_token: 'access-new',
      refresh_token: 'refresh-new',
    })
    await expect(viaCoordinator).resolves.toEqual({
      access_token: 'access-new',
      refresh_token: 'refresh-new',
    })
    expect(fetchMock).toHaveBeenCalledTimes(1)
  })

  it('reuses an access token rotated after the request that produced a late 401', async () => {
    setAuthTokens({ access_token: 'access-new', refresh_token: 'refresh-new' })
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    await expect(refreshTokenOnce('access-old')).resolves.toEqual({
      access_token: 'access-new',
      refresh_token: 'refresh-new',
    })
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('refuses to rotate credentials that belong to a previous login epoch', async () => {
    const previousEpoch = getAuthEpoch()
    clearAuthTokens()
    setAuthTokens({ access_token: 'other-access', refresh_token: 'other-refresh' })
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    await expect(refreshTokenOnce('access-old', previousEpoch)).rejects.toBeInstanceOf(
      StaleSessionError,
    )
    expect(fetchMock).not.toHaveBeenCalled()
    expect(getAccessToken()).toBe('other-access')
  })

  it('re-reads the shared token inside the cross-tab lock instead of spending it twice', async () => {
    const gate = deferred<void>()
    vi.stubGlobal('navigator', {
      locks: {
        request: async (_name: string, callback: (lock: unknown) => Promise<unknown>) => {
          await gate.promise
          return callback(null)
        },
      },
    })
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    const pending = refreshTokenOnce()
    await Promise.resolve()
    // Another tab completes its rotation while this tab waits for the lock.
    setAuthTokens({ access_token: 'access-new', refresh_token: 'refresh-new' })
    gate.resolve()
    await expect(pending).resolves.toEqual({
      access_token: 'access-new',
      refresh_token: 'refresh-new',
    })
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('does not consume a refresh token whose outcome is unknown', async () => {
    const fetchMock = vi.fn().mockRejectedValueOnce(new TypeError('network'))
    vi.stubGlobal('fetch', fetchMock)
    await expect(refreshTokenOnce()).rejects.toThrow('network')
    expect(readRefreshAttempt()).toEqual({
      token: 'refresh-old',
      epoch: getAuthEpoch(),
      baseUrl: BASE_URL,
    })
    await expect(refreshTokenOnce()).rejects.toMatchObject({ status: 401 })
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(getAccessToken()).toBe('access-old')
  })

  it('never replays a persisted attempt left by another tab or a reload', async () => {
    writeRefreshAttempt({ token: 'refresh-old', epoch: getAuthEpoch(), baseUrl: BASE_URL })
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    await expect(refreshTokenOnce()).rejects.toMatchObject({ status: 401 })
    expect(fetchMock).not.toHaveBeenCalled()
  })

  it('clears the uncertain marker after a definite rejection', async () => {
    const fetchMock = vi.fn().mockResolvedValueOnce(json({ title: 'invalid' }, 401))
    vi.stubGlobal('fetch', fetchMock)
    await expect(refreshTokenOnce()).rejects.toMatchObject({ status: 401 })
    expect(readRefreshAttempt()).toBeNull()
  })

  it('refuses automatic rotation in a browser without Web Locks', async () => {
    browserStorage({
      [authTokenStorageKeys.accessToken]: 'access-old',
      [authTokenStorageKeys.refreshToken]: 'refresh-old',
    })
    vi.stubGlobal('navigator', {})
    expect(refreshStrategy()).toBe('unavailable')
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    await expect(refreshTokenOnce()).rejects.toMatchObject({ status: 401 })
    expect(fetchMock).not.toHaveBeenCalled()
    expect(getAccessToken()).toBe('access-old')
  })
})

describe('middleware rotation and recovery boundaries', () => {
  const client = () => createApiClient({ baseUrl: BASE_URL })

  it.each(['/auth/capabilities', '/auth/external/{connectionId}/start'] as const)(
    'sends no bearer token and does not rotate on %s',
    async (path) => {
      const fetchMock = vi.fn().mockResolvedValue(json({ title: 'not found' }, 401))
      vi.stubGlobal('fetch', fetchMock)
      const request =
        path === '/auth/capabilities'
          ? client().GET('/auth/capabilities')
          : client().POST('/auth/external/{connectionId}/start', {
              params: { path: { connectionId: 'oidc-one' } },
              body: {
                browser_challenge: 'challenge',
                completion_url: 'https://frontend.example/auth/complete',
                return_to: '/',
              },
            })
      const { response } = await request
      expect(response.status).toBe(401)
      expect(fetchMock).toHaveBeenCalledTimes(1)
      expect((fetchMock.mock.calls[0]![0] as Request).headers.has('Authorization')).toBe(false)
    },
  )

  it('rotates and replays a bearer 401 on the dual-purpose external endpoint', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(json({ title: 'expired' }, 401))
      .mockResolvedValueOnce(json(nextSession))
      .mockResolvedValueOnce(json({ kind: 'test_result' }))
    vi.stubGlobal('fetch', fetchMock)
    const { response } = await client().POST('/auth/external/exchange', {
      body: { ticket: 'ticket', verifier: 'verifier' },
    })
    expect(response.status).toBe(200)
    expect(fetchMock).toHaveBeenCalledTimes(3)
    expect((fetchMock.mock.calls[2]![0] as Request).headers.get('Authorization')).toBe(
      'Bearer access-new',
    )
    expect(getAccessToken()).toBe('access-new')
  })

  it('does not rotate or drop the session for an anonymous 401', async () => {
    const unauthorized = vi.fn()
    setUnauthorizedHandler(unauthorized)
    clearAuthTokens()
    const fetchMock = vi.fn().mockResolvedValue(json({ title: 'unauthenticated' }, 401))
    vi.stubGlobal('fetch', fetchMock)
    const { response } = await client().GET('/users/me')
    expect(response.status).toBe(401)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(unauthorized).not.toHaveBeenCalled()
  })

  it('never logs out on a 403', async () => {
    const unauthorized = vi.fn()
    setUnauthorizedHandler(unauthorized)
    const fetchMock = vi.fn().mockResolvedValue(json({ title: 'forbidden' }, 403))
    vi.stubGlobal('fetch', fetchMock)
    const { response } = await client().GET('/users/me')
    expect(response.status).toBe(403)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(unauthorized).not.toHaveBeenCalled()
    expect(getAccessToken()).toBe('access-old')
  })

  it('keeps the local-mode 401 logout semantics for token-less requests', async () => {
    setLocalMode(true)
    const unauthorized = vi.fn()
    setUnauthorizedHandler(unauthorized)
    const fetchMock = vi.fn().mockResolvedValue(json({ title: 'local' }, 401))
    vi.stubGlobal('fetch', fetchMock)
    const { response } = await client().GET('/users/me')
    expect(response.status).toBe(401)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(unauthorized).toHaveBeenCalledTimes(1)
  })

  it('sends the reused access token when replaying after an in-flight rotation', async () => {
    const rotation = deferred<Response>()
    const fetchMock = vi.fn().mockReturnValueOnce(rotation.promise)
    vi.stubGlobal('fetch', fetchMock)
    const recovery = recoverUnauthorizedResponse(
      { status: 401, text: '{}' },
      captureSession(),
      (accessToken) => Promise.resolve({ status: 200, text: accessToken }),
      'access-old',
    )
    rotation.resolve(json(nextSession))
    await expect(recovery).resolves.toEqual({ status: 200, text: 'access-new' })
  })

  it.each(['refresh-success', 'refresh-rejected', 'replay-rejected'] as const)(
    'rejects a changed shared epoch after awaiting %s without replay or invalidation',
    async (stage) => {
      const network = deferred<Response>()
      const fetchMock = vi.fn().mockReturnValue(network.promise)
      const replay = vi.fn().mockReturnValue(network.promise)
      const unauthorized = vi.fn()
      setUnauthorizedHandler(unauthorized)
      vi.stubGlobal('fetch', fetchMock)
      if (stage === 'replay-rejected') setAuthTokens(nextSession)
      const pending = recoverUnauthorizedResponse(
        { status: 401 },
        captureSession(),
        replay,
        'access-old',
        getAuthEpoch(),
      )
      const rejected = expect(pending).rejects.toBeInstanceOf(StaleSessionError)
      await vi.waitFor(() =>
        expect(stage === 'replay-rejected' ? replay : fetchMock).toHaveBeenCalledTimes(1),
      )
      // Storage events have not yet updated this realm's session generation.
      clearAuthTokens()
      setAuthTokens({ access_token: 'other-access', refresh_token: 'other-refresh' })
      network.resolve(stage === 'refresh-success' ? json(nextSession) : json({}, 401))
      await rejected
      expect(replay).toHaveBeenCalledTimes(stage === 'replay-rejected' ? 1 : 0)
      expect(unauthorized).not.toHaveBeenCalled()
      expect(getAccessToken()).toBe('other-access')
    },
  )

  it('does not rotate the new account after awaiting the original 401 body', async () => {
    const parsed = deferred<unknown>()
    const response = json({}, 401)
    vi.spyOn(response, 'clone').mockReturnValue({ json: () => parsed.promise } as Response)
    const fetchMock = vi.fn()
    const replay = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    const pending = recoverUnauthorizedResponse(response, captureSession(), replay, 'access-old')
    const rejected = expect(pending).rejects.toBeInstanceOf(StaleSessionError)
    clearAuthTokens()
    setAuthTokens({ access_token: 'other-access', refresh_token: 'other-refresh' })
    parsed.resolve({})
    await rejected
    expect(fetchMock).not.toHaveBeenCalled()
    expect(replay).not.toHaveBeenCalled()
    expect(getAccessToken()).toBe('other-access')
  })
})

describe('coordinated logout', () => {
  it('keeps shared credentials and epoch until another tab publishes its rotation', async () => {
    const gate = deferred<void>()
    vi.stubGlobal('navigator', {
      locks: {
        request: async (_name: string, run: () => Promise<unknown>) => {
          await gate.promise
          return run()
        },
      },
    })
    const epoch = getAuthEpoch()
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)
    const pending = logoutCurrentSession()
    expect(getAccessToken()).toBeNull()
    expect(getRefreshToken()).toBeNull()
    expect(getAuthEpoch()).toBe(epoch)
    expect(readStoredAuthTokens()?.refresh_token).toBe('refresh-old')
    // Simulate the other realm publishing while it still owns the lock.
    setAuthTokens(nextSession)
    gate.resolve()
    await pending
    expect(fetchMock).toHaveBeenCalledExactlyOnceWith(
      `${BASE_URL}/auth/logout`,
      expect.objectContaining({
        headers: expect.objectContaining({ Authorization: 'Bearer access-new' }),
        body: JSON.stringify({ refresh_token: 'refresh-new' }),
      }),
    )
    expect(readStoredAuthTokens()).toBeNull()
    expect(getAuthEpoch()).not.toBe(epoch)
  })

  it('does not deadlock behind its own queued refresh or spend its token twice', async () => {
    const gate = deferred<void>()
    let tail: Promise<unknown> = gate.promise
    const request = vi.fn((_name: string, run: () => Promise<unknown>) => {
      const next = tail.then(run)
      tail = next.catch(() => undefined)
      return next
    })
    vi.stubGlobal('navigator', { locks: { request } })
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)
    const rotation = refreshTokenOnce()
    const rejected = expect(rotation).rejects.toBeInstanceOf(StaleSessionError)
    const pending = logoutCurrentSession()
    gate.resolve()
    await Promise.all([rejected, pending])
    expect(request).toHaveBeenCalledTimes(2)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(readStoredAuthTokens()).toBeNull()
  })

  it('publishes a local in-flight rotation under the lock without exposing it to local getters', async () => {
    const rotating = deferred<Response>()
    const revoking = deferred<Response>()
    let tail = Promise.resolve<unknown>(undefined)
    vi.stubGlobal('navigator', {
      locks: {
        request: (_name: string, run: () => Promise<unknown>) => {
          const next = tail.then(run)
          tail = next.catch(() => undefined)
          return next
        },
      },
    })
    const fetchMock = vi
      .fn()
      .mockReturnValueOnce(rotating.promise)
      .mockReturnValueOnce(revoking.promise)
    vi.stubGlobal('fetch', fetchMock)
    const rotation = refreshTokenOnce()
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    const pending = logoutCurrentSession()
    rotating.resolve(json(nextSession))
    await rotation
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2))
    expect(readStoredAuthTokens()?.refresh_token).toBe('refresh-new')
    expect(getAccessToken()).toBeNull()
    expect(getRefreshToken()).toBeNull()
    expect(readRefreshAttempt()).toBeNull()
    const [, init] = fetchMock.mock.calls[1] as [string, RequestInit]
    expect(JSON.parse(init.body as string)).toEqual({ refresh_token: 'refresh-new' })
    revoking.resolve(new Response(null, { status: 204 }))
    await pending
    expect(readStoredAuthTokens()).toBeNull()
  })

  it('does not replay an unknown refresh outcome while logging out', async () => {
    writeRefreshAttempt({ token: 'refresh-old', epoch: getAuthEpoch(), baseUrl: BASE_URL })
    const fetchMock = vi.fn().mockResolvedValue(json({ title: 'expired' }, 401))
    vi.stubGlobal('fetch', fetchMock)
    await expect(logoutCurrentSession()).rejects.toMatchObject({ status: 401 })
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(fetchMock.mock.calls[0]![0]).toBe(`${BASE_URL}/auth/logout`)
    expect(readStoredAuthTokens()).toBeNull()
  })

  it('still attempts revocation without Web Locks but never automatically refreshes', async () => {
    browserStorage({
      [authTokenStorageKeys.accessToken]: 'access-old',
      [authTokenStorageKeys.refreshToken]: 'refresh-old',
    })
    vi.stubGlobal('navigator', {})
    const fetchMock = vi.fn().mockResolvedValue(json({ title: 'expired' }, 401))
    vi.stubGlobal('fetch', fetchMock)
    await expect(logoutCurrentSession()).rejects.toMatchObject({ status: 401 })
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(fetchMock.mock.calls[0]![0]).toBe(`${BASE_URL}/auth/logout`)
    expect(readStoredAuthTokens()).toBeNull()
  })

  it('keeps local-mode logout local', async () => {
    setLocalMode(true)
    const fetchMock = vi.fn()
    vi.stubGlobal('fetch', fetchMock)
    await logoutCurrentSession()
    expect(fetchMock).not.toHaveBeenCalled()
    expect(readStoredAuthTokens()).toBeNull()
  })

  it.each(
    (['lock', 'revoke', 'refresh'] as const).flatMap((stage) =>
      (['epoch', 'service'] as const).map((change) => ({ stage, change })),
    ),
  )(
    'preserves another login when $change changes while awaiting $stage',
    async ({ stage, change }) => {
      const store = browserStorage({
        [authTokenStorageKeys.accessToken]: 'access-old',
        [authTokenStorageKeys.refreshToken]: 'refresh-old',
      })
      const gate = deferred<void>()
      const network = deferred<Response>()
      vi.stubGlobal('navigator', {
        locks: {
          request: async (_name: string, run: () => Promise<unknown>) => {
            if (stage === 'lock') await gate.promise
            return run()
          },
        },
      })
      const fetchMock = vi.fn()
      if (stage === 'refresh') fetchMock.mockResolvedValueOnce(json({ title: 'expired' }, 401))
      fetchMock.mockReturnValue(network.promise)
      vi.stubGlobal('fetch', fetchMock)
      const pending = logoutCurrentSession()
      if (stage !== 'lock')
        await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(stage === 'refresh' ? 2 : 1))
      if (change === 'epoch') clearAuthTokens()
      else store.set(authTokenStorageKeys.apiBaseUrl, 'https://other-service.example/api/v1')
      setAuthTokens({ access_token: 'other-access', refresh_token: 'other-refresh' })
      const otherEpoch = getAuthEpoch()
      gate.resolve()
      network.resolve(stage === 'refresh' ? json(nextSession) : json({ title: 'expired' }, 401))
      await pending
      expect(getAccessToken()).toBe('other-access')
      expect(getRefreshToken()).toBe('other-refresh')
      expect(getAuthEpoch()).toBe(otherEpoch)
      expect(fetchMock).toHaveBeenCalledTimes(stage === 'lock' ? 0 : stage === 'refresh' ? 2 : 1)
    },
  )

  it('waits for the cross-tab lock and revokes the latest credentials', async () => {
    const gate = deferred<void>()
    const request = vi.fn(async (_name: string, callback: (lock: unknown) => Promise<unknown>) => {
      await gate.promise
      return callback(null)
    })
    vi.stubGlobal('navigator', { locks: { request } })
    const fetchMock = vi.fn().mockResolvedValue(new Response(null, { status: 204 }))
    vi.stubGlobal('fetch', fetchMock)
    const logout = logoutCurrentSession()
    await Promise.resolve()
    expect(fetchMock).not.toHaveBeenCalled()
    gate.resolve()
    await logout
    expect(request).toHaveBeenCalledTimes(1)
    const [url, init] = fetchMock.mock.calls[0] as [string, RequestInit]
    expect(url).toBe(`${BASE_URL}/auth/logout`)
    expect(new Headers(init.headers).get('Authorization')).toBe('Bearer access-old')
    expect(JSON.parse(init.body as string)).toEqual({ refresh_token: 'refresh-old' })
    expect(getAccessToken()).toBeNull()
    expect(getRefreshToken()).toBeNull()
  })
})
