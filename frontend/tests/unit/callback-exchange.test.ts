import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { captureSession, changeSessionContext } from '../../src/api/session-core'
import {
  callbackFailureMessage,
  completeAuthCallback,
  discardAuthCallback,
} from '../../src/auth/callback-exchange'
import {
  EXTERNAL_FLOW_KEY,
  completeExternalConfirmation,
  exchangeExternalTicket,
  installExternalAuthLifecycle,
  readExternalFlow,
  takeExternalProof,
  type ExternalFlow,
} from '../../src/auth/external-browser'
import { assertCallbackPrepared, finishCallbackStartup } from '../../src/auth/callback-startup'

const mocks = vi.hoisted(() => ({
  get: vi.fn(),
  post: vi.fn(),
  resolve: vi.fn(),
  publish: vi.fn(),
  baseUrl: 'https://app.example/api/v1',
  epoch: 'epoch-one',
  access: null as string | null,
  refresh: null as string | null,
}))
vi.mock('@/api/client-core', () => ({
  apiClient: { GET: mocks.get, POST: mocks.post },
  createApiClient: () => ({ POST: mocks.post }),
  setApiBaseUrl: (url: string) => {
    mocks.baseUrl = new URL(url, 'https://app.example').href
  },
  setLocalMode: vi.fn(),
}))
vi.mock('@/api/bootstrap-service', () => ({ resolveBootstrapService: mocks.resolve }))
vi.mock('@/api/token-storage', () => ({
  getAuthEpoch: () => mocks.epoch,
  getAccessToken: () => mocks.access,
  getRefreshToken: () => mocks.refresh,
  readStoredApiBaseUrl: () => mocks.baseUrl,
  setAuthSession: mocks.publish,
  clearAuthTokens: vi.fn(),
}))

let storage: Map<string, string>
let browser: EventTarget
const replace = vi.fn()
const user = { id: 42, username: 'member', email: null, active: true, role: 'user' }
const future = (ms = 120_000) => new Date(Date.now() + ms).toISOString()
const session = () => ({
  user,
  access_token: 'new-access',
  refresh_token: 'new-refresh',
  token_type: 'Bearer',
  expires_at: future(),
  refresh_expires_at: future(240_000),
})
const reply = (data: unknown) => ({ data, response: new Response(null, { status: 200 }) })
const seed = (patch: Partial<ExternalFlow> = {}, extra: Record<string, unknown> = {}) => {
  const flow: ExternalFlow = {
    baseUrl: 'https://app.example/api/v1',
    epoch: 'epoch-one',
    userId: null,
    purpose: 'login',
    connectionId: 'company',
    transactionId: 'tx-one',
    expiresAt: future(),
    returnTo: '/projects',
    stage: 'started',
    ...patch,
  }
  storage.set(EXTERNAL_FLOW_KEY, JSON.stringify({ ...flow, verifier: 'v'.repeat(43), ...extra }))
}
const confirmation = (
  kind: 'signup_confirmation' | 'link_confirmation' = 'signup_confirmation',
) => ({
  kind,
  transaction_id: 'tx-one',
  confirmation_token: 'private-confirmation',
  expires_at: future(),
  profile: { connection_id: 'company', connection_name: 'Company', external_display: 'Member' },
  signup_requirements: kind === 'signup_confirmation' ? ['local_password'] : [],
  signup:
    kind === 'signup_confirmation'
      ? { email_requirement: 'optional', password_requirement: 'required' }
      : undefined,
})
const run = (controller = new AbortController()) =>
  completeAuthCallback('ticket-one', controller.signal)

beforeEach(() => {
  vi.clearAllMocks()
  mocks.baseUrl = 'https://app.example/api/v1'
  mocks.epoch = 'epoch-one'
  mocks.access = null
  mocks.refresh = null
  storage = new Map()
  browser = new EventTarget()
  Object.assign(browser, { location: { reload: vi.fn() } })
  vi.stubGlobal('window', browser)
  vi.stubGlobal('location', { origin: 'https://app.example' })
  vi.stubGlobal('history', { replaceState: replace, state: null })
  vi.stubGlobal('sessionStorage', {
    getItem: (key: string) => storage.get(key) ?? null,
    setItem: (key: string, value: string) => storage.set(key, value),
    removeItem: (key: string) => storage.delete(key),
  })
  changeSessionContext(mocks.baseUrl, null, true)
  mocks.resolve.mockResolvedValue({ baseUrl: '/api/v1', mode: 'server', health: { status: 'ok' } })
  mocks.get.mockResolvedValue(reply(user))
  mocks.post.mockResolvedValue(reply({ kind: 'session', session: session() }))
})
afterEach(() => {
  installExternalAuthLifecycle()()
  discardAuthCallback()
  finishCallbackStartup()
  vi.unstubAllGlobals()
})

describe('lightweight callback identity and exchange', () => {
  it('claims storage before probing, publishes a session once, and changes history in the same document', async () => {
    seed()
    const issued = session()
    mocks.post.mockResolvedValue(reply({ kind: 'session', session: issued }))
    mocks.resolve.mockImplementation(async () => {
      expect(storage.has(EXTERNAL_FLOW_KEY)).toBe(false)
      expect(mocks.post).not.toHaveBeenCalled()
      return { baseUrl: '/api/v1', mode: 'server', health: { status: 'ok' } }
    })
    await run()
    expect(mocks.get).not.toHaveBeenCalled()
    expect(mocks.post).toHaveBeenCalledOnce()
    expect(mocks.post.mock.calls[0]?.[1].body).toEqual({
      ticket: 'ticket-one',
      verifier: 'v'.repeat(43),
    })
    expect(mocks.publish).toHaveBeenCalledWith(issued)
    expect(replace).toHaveBeenCalledWith(null, '', '/projects')
    expect(captureSession().userId).toBe(42)
    expect(readExternalFlow()).toBeNull()
    expect(() => assertCallbackPrepared()).not.toThrow()
  })

  it('keeps signup confirmation private and resumes without spending the ticket again', async () => {
    seed()
    mocks.post.mockResolvedValueOnce(reply(confirmation()))
    expect(await run()).toBeTruthy()
    expect(readExternalFlow()?.stage).toBe('confirming')
    const snapshot = await exchangeExternalTicket()
    expect(snapshot).not.toHaveProperty('confirmation_token')
    expect(snapshot).not.toHaveProperty('verifier')
    expect(storage.size).toBe(0)
    expect(mocks.publish).not.toHaveBeenCalled()
    expect(mocks.post).toHaveBeenCalledOnce()
    mocks.post.mockResolvedValueOnce(reply({ kind: 'session', session: session() }))
    await completeExternalConfirmation({ username: 'member', initial_password: 'new-password' })
    expect(mocks.post.mock.calls[1]?.[1].body).toMatchObject({
      confirmation_token: 'private-confirmation',
      verifier: 'v'.repeat(43),
      username: 'member',
    })
    expect(storage.size).toBe(0)
  })

  it('restores exactly the original protected user before a link exchange', async () => {
    seed({ purpose: 'link', userId: 42, returnTo: '/settings/security' })
    mocks.access = 'access'
    mocks.refresh = 'refresh'
    mocks.post.mockImplementation(async () => {
      expect(mocks.get).toHaveBeenCalledOnce()
      expect(captureSession().userId).toBe(42)
      return reply(confirmation('link_confirmation'))
    })
    await run()
    expect(mocks.get.mock.calls[0]?.[0]).toBe('/users/me')
    expect(readExternalFlow()?.purpose).toBe('link')
    expect(await exchangeExternalTicket()).not.toHaveProperty('confirmation_token')
    expect(mocks.post).toHaveBeenCalledOnce()
  })

  it('hands off reauthentication proof once, bound to its action, target and original user', async () => {
    seed({
      purpose: 'reauthenticate',
      userId: 42,
      connectionId: undefined,
      action: 'unlink',
      targetId: 'binding-7',
    })
    mocks.access = 'access'
    mocks.refresh = 'refresh'
    mocks.post.mockResolvedValue(
      reply({
        kind: 'reauthentication_proof',
        reauthentication_proof: 'private-proof',
        expires_at: future(),
      }),
    )
    await run()
    expect(replace).toHaveBeenCalledWith(null, '', '/settings/security')
    expect(storage.size).toBe(0)
    expect(takeExternalProof()).toMatchObject({
      action: 'unlink',
      targetId: 'binding-7',
      proof: 'private-proof',
    })
    expect(takeExternalProof()).toBeNull()
  })

  it.each([
    { name: 'expired', patch: { expiresAt: new Date(0).toISOString() } },
    { name: 'other epoch', patch: { epoch: 'epoch-other' } },
    { name: 'other service', patch: { baseUrl: 'https://other.example/api/v1' } },
    { name: 'unsafe return path', patch: { returnTo: '//other.example/' } },
    { name: 'other purpose', patch: { purpose: 'test' } },
  ])('rejects $name without an exchange', async ({ patch }) => {
    seed(patch as Partial<ExternalFlow>)
    await expect(run()).rejects.toThrow()
    expect(mocks.post).not.toHaveBeenCalled()
    expect(mocks.publish).not.toHaveBeenCalled()
    expect(storage.size).toBe(0)
  })

  it('rejects an anonymous callback carrying unrelated credentials', async () => {
    seed()
    mocks.access = 'unrelated-access'
    await expect(run()).rejects.toThrow()
    expect(mocks.resolve).not.toHaveBeenCalled()
    expect(mocks.post).not.toHaveBeenCalled()
  })

  it.each([43, 0])('rejects protected user mismatch (%s)', async (id) => {
    seed({ purpose: 'link', userId: 42 })
    mocks.access = 'access'
    mocks.refresh = 'refresh'
    mocks.get.mockResolvedValue(reply({ ...user, id }))
    await expect(run()).rejects.toThrow()
    expect(mocks.post).not.toHaveBeenCalled()
  })

  it('preserves local-default priority and refuses to exchange against an overridden server', async () => {
    seed()
    mocks.resolve.mockResolvedValue({ baseUrl: '/api/v1', mode: 'local' })
    await expect(run()).rejects.toThrow()
    expect(mocks.post).not.toHaveBeenCalled()
  })

  it('cancels a pending identity check and refuses its late result', async () => {
    seed()
    let resolve!: (value: unknown) => void
    mocks.resolve.mockReturnValue(
      new Promise((done) => {
        resolve = done
      }),
    )
    const controller = new AbortController()
    const pending = run(controller)
    controller.abort()
    resolve({ baseUrl: '/api/v1', mode: 'server' })
    await expect(pending).rejects.toThrow()
    expect(mocks.post).not.toHaveBeenCalled()
    expect(storage.size).toBe(0)
  })

  it('rejects a session switch during the probe before spending a ticket', async () => {
    seed()
    mocks.resolve.mockImplementation(async () => {
      changeSessionContext('https://other.example/api/v1', 99, true)
      return { baseUrl: '/api/v1', mode: 'server' }
    })
    await expect(run()).rejects.toThrow()
    expect(mocks.post).not.toHaveBeenCalled()
  })

  it('rejects an aborted exchange response without publishing a session or replaying the ticket', async () => {
    seed()
    const controller = new AbortController()
    mocks.post.mockImplementation(async () => {
      controller.abort()
      return reply({ kind: 'session', session: session() })
    })
    await expect(run(controller)).rejects.toThrow()
    expect(mocks.publish).not.toHaveBeenCalled()
    expect(readExternalFlow()).toBeNull()
    await expect(run()).rejects.toThrow()
    expect(mocks.post).toHaveBeenCalledOnce()
  })

  it('rejects malformed sessions and a result belonging to another purpose', async () => {
    seed()
    mocks.post.mockResolvedValueOnce(
      reply({ kind: 'session', session: { ...session(), user: { ...user, active: false } } }),
    )
    await expect(run()).rejects.toThrow()
    expect(mocks.publish).not.toHaveBeenCalled()
    seed()
    mocks.post.mockResolvedValueOnce(reply({ kind: 'test_result', test_id: 'wrong-kind' }))
    await expect(run()).rejects.toThrow()
    expect(mocks.publish).not.toHaveBeenCalled()
  })

  it('keeps safe failure text and invalidates prepared ownership on a later account change', async () => {
    seed()
    await run()
    changeSessionContext(mocks.baseUrl, 99, true)
    expect(() => assertCallbackPrepared()).toThrow()
    expect(callbackFailureMessage(new Error('private-ticket-or-server-payload'))).toBe(
      'resultUnknown',
    )
  })
})
