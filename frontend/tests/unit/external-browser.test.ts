import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { webcrypto } from 'node:crypto'
import { captureSession, changeSessionContext } from '../../src/api/session-context'
import {
  EXTERNAL_FLOW_KEY,
  activateExternalCallback,
  clearExternalFlow,
  clearExternalProof,
  createBrowserChallenge,
  completeExternalConfirmation,
  cancelExternalFlow,
  exchangeExternalTicket,
  installExternalAuthLifecycle,
  prepareExternalCallback,
  readExternalFlow,
  startExternalLogin,
  saveExternalResult,
  takeExternalProof,
  type ExternalFlow,
} from '../../src/auth/external-browser'
import { safeReturnPath } from '../../src/auth/return-path'

const mocks = vi.hoisted(() => ({
  post: vi.fn(),
  epoch: 'epoch-a',
  assign: vi.fn(),
  reload: vi.fn(),
}))
vi.mock('@/i18n', () => ({ t: (key: string) => key }))
vi.mock('@/api/client-core', () => ({
  apiClient: { POST: mocks.post },
  createApiClient: () => ({ POST: mocks.post }),
}))
vi.mock('@/api/token-storage', () => ({
  getAuthEpoch: () => mocks.epoch,
  clearAuthTokens: () => {
    mocks.epoch += '-new'
  },
}))
let storage: Map<string, string>
let storageFailure: 'get' | 'set' | 'remove' | null
let browserWindow: EventTarget
afterEach(() => vi.unstubAllGlobals())
const flow = (patch: Partial<ExternalFlow> = {}): ExternalFlow => ({
  baseUrl: captureSession().baseUrl,
  epoch: mocks.epoch,
  userId: null,
  purpose: 'login',
  connectionId: 'oidc-one',
  transactionId: 'transaction-one',
  expiresAt: new Date(Date.now() + 60_000).toISOString(),
  returnTo: '/projects',
  stage: 'started',
  ...patch,
})
const seedRaw = (value = flow(), extra: Record<string, unknown> = {}) =>
  storage.set(EXTERNAL_FLOW_KEY, JSON.stringify({ ...value, verifier: 'v'.repeat(43), ...extra }))
const seed = (value = flow(), ticket = 'ticket') => {
  seedRaw(value)
  prepareExternalCallback(ticket)
  activateExternalCallback()
}
const confirmation = () => ({
  kind: 'signup_confirmation' as const,
  transaction_id: 'transaction-one',
  confirmation_token: 'confirmation-one',
  expires_at: new Date(Date.now() + 40_000).toISOString(),
  signup_requirements: ['local_password' as const],
  signup: { email_requirement: 'optional' as const, password_requirement: 'required' as const },
  profile: {
    connection_id: 'oidc-one',
    connection_name: 'Company',
    external_display: 'External user',
  },
})
const reply = (data: unknown) => ({ data, response: new Response(null, { status: 200 }) })
const exchangeAs = (data: unknown) => {
  mocks.post.mockResolvedValue(reply(data))
  return exchangeExternalTicket('ticket')
}
beforeEach(() => {
  mocks.epoch = 'epoch-a'
  mocks.post.mockReset()
  mocks.assign.mockReset()
  mocks.reload.mockReset()
  storage = new Map()
  vi.stubGlobal('sessionStorage', {
    getItem: (key: string) => {
      if (storageFailure === 'get') throw new Error('storage get failed')
      return storage.get(key) ?? null
    },
    setItem: (key: string, value: string) => {
      if (storageFailure === 'set') throw new Error('storage set failed')
      storage.set(key, value)
    },
    removeItem: (key: string) => {
      if (storageFailure === 'remove') throw new Error('storage remove failed')
      storage.delete(key)
    },
  })
  vi.stubGlobal('crypto', webcrypto)
  vi.stubGlobal('location', {
    origin: 'https://frontend.example',
    assign: mocks.assign,
    reload: mocks.reload,
  })
  storageFailure = null
  browserWindow = new EventTarget()
  Object.assign(browserWindow, { location: { reload: mocks.reload } })
  vi.stubGlobal('window', browserWindow)
  changeSessionContext('https://service.example/api/v1', null, true)
})
describe('external browser transactions', () => {
  it('does not expose the removed test purpose or persist test results in ordinary flows', async () => {
    seed()
    expect(() =>
      saveExternalResult({
        kind: 'test_result',
        connection_id: 'oidc-one',
        revision: 1,
        fresh_supported: false,
        attributes: {},
        rules: {},
        test_id: 'test-one',
        expires_at: new Date(Date.now() + 20_000).toISOString(),
      } as never),
    ).toThrow()
    expect(storage.has(EXTERNAL_FLOW_KEY)).toBe(false)
  })
  it('deletes the v2 callback entry before activation and exposes only a detached metadata snapshot', () => {
    seedRaw()
    prepareExternalCallback('ticket')
    expect(storage.has(EXTERNAL_FLOW_KEY)).toBe(false)
    expect(readExternalFlow()).toBeNull()

    activateExternalCallback()
    const snapshot = readExternalFlow()!
    expect(snapshot).toMatchObject({ stage: 'started', transactionId: 'transaction-one' })
    expect(snapshot).not.toHaveProperty('verifier')
    expect(snapshot).not.toHaveProperty('confirmation_token')
    expect(snapshot).not.toHaveProperty('confirmation')
    expect(snapshot).not.toHaveProperty('unrecognized')
    snapshot.stage = 'completing'
    expect(readExternalFlow()?.stage).toBe('started')
  })
  it.each([
    ['unrecognized', 'private-future-field'],
    ['confirmation_token', 'must-not-be-cached'],
    ['confirmation', { confirmation_token: 'must-not-be-cached' }],
  ])('rejects unknown v2 redirect field %s', (field, value) => {
    seedRaw(flow(), { [field]: value })
    prepareExternalCallback('ticket')
    activateExternalCallback()
    expect(storage.has(EXTERNAL_FLOW_KEY)).toBe(false)
    expect(readExternalFlow()).toBeNull()
  })
  it('clears callback state without claiming when there is no ticket and removes legacy v1 data', () => {
    seedRaw()
    storage.set('linguaflow.external-flow.v1', JSON.stringify({ verifier: 'legacy' }))
    prepareExternalCallback()
    activateExternalCallback()
    expect(storage.has(EXTERNAL_FLOW_KEY)).toBe(false)
    expect(storage.has('linguaflow.external-flow.v1')).toBe(false)
    expect(readExternalFlow()).toBeNull()
  })
  it.each([
    '{broken-json',
    JSON.stringify({ ...flow(), stage: 'exchanging' }),
    JSON.stringify({ ...flow(), expiresAt: 'not-a-date' }),
  ])('rejects malformed or non-started callback state: %s', (raw) => {
    storage.set(EXTERNAL_FLOW_KEY, raw)
    prepareExternalCallback('ticket')
    activateExternalCallback()
    expect(storage.has(EXTERNAL_FLOW_KEY)).toBe(false)
    expect(readExternalFlow()).toBeNull()
  })
  it('does not activate a callback until identity restoration matches its captured user', () => {
    seedRaw(flow())
    prepareExternalCallback('ticket')
    changeSessionContext(captureSession().baseUrl, 7, true)
    activateExternalCallback()
    expect(readExternalFlow()).toBeNull()
  })
  it.each(['get', 'remove'] as const)(
    'fails closed when sessionStorage.%sItem throws',
    (failure) => {
      seedRaw()
      storageFailure = failure
      expect(() => prepareExternalCallback('ticket')).not.toThrow()
      expect(() => activateExternalCallback()).not.toThrow()
      storageFailure = null
      expect(readExternalFlow()).toBeNull()
    },
  )
  it('expires an active flow when its deadline timer fires', () => {
    vi.useFakeTimers()
    try {
      seed(flow({ expiresAt: new Date(Date.now() + 100).toISOString() }))
      expect(readExternalFlow()).toMatchObject({ stage: 'started' })
      vi.advanceTimersByTime(101)
      expect(readExternalFlow()).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })
  it('rejects a start when sessionStorage cannot persist the v2 verifier', async () => {
    mocks.post.mockResolvedValue(
      reply({
        transaction_id: 'new',
        authorization_url: 'https://idp.example/authorize',
        expires_at: new Date(Date.now() + 60_000).toISOString(),
      }),
    )
    storageFailure = 'set'
    await expect(startExternalLogin('oidc-one')).rejects.toThrow()
    expect(mocks.assign).not.toHaveBeenCalled()
  })
  it('clears active state on identity changes and page lifecycle events', () => {
    const uninstall = installExternalAuthLifecycle()
    try {
      seed()
      changeSessionContext(captureSession().baseUrl, 4, true)
      expect(readExternalFlow()).toBeNull()

      const linkedFlow = flow({ userId: 4, purpose: 'link' })
      seed(linkedFlow)
      expect(readExternalFlow()).toMatchObject({ purpose: 'link', userId: 4 })
      browserWindow.dispatchEvent(new Event('pagehide'))
      expect(readExternalFlow()).toBeNull()

      seed(linkedFlow)
      expect(readExternalFlow()).toMatchObject({ purpose: 'link', userId: 4 })
      browserWindow.dispatchEvent(new Event('pageshow'))
      expect(readExternalFlow()).toMatchObject({ purpose: 'link', userId: 4 })
      expect(mocks.reload).not.toHaveBeenCalled()

      const persistedPageshow = new Event('pageshow') as PageTransitionEvent
      Object.defineProperty(persistedPageshow, 'persisted', { value: true })
      browserWindow.dispatchEvent(persistedPageshow)
      expect(readExternalFlow()).toBeNull()
      expect(mocks.reload).toHaveBeenCalledTimes(1)
    } finally {
      uninstall()
    }
  })
  it('rejects a late exchange after changing services without replacing the new transaction', async () => {
    seed()
    let resolve!: (value: unknown) => void
    mocks.post.mockReturnValue(
      new Promise((value) => {
        resolve = value
      }),
    )
    const pending = exchangeExternalTicket('ticket')
    changeSessionContext('https://other.example/api/v1', null, true)
    seed(flow({ transactionId: 'new-transaction' }))
    resolve(reply(confirmation()))
    await expect(pending).rejects.toThrow()
    expect(readExternalFlow()?.transactionId).toBe('new-transaction')
  })

  it('clears an invalidated confirmation so it cannot be reenabled or replayed', async () => {
    seed()
    await exchangeAs(confirmation())
    mocks.post.mockResolvedValue({
      error: { type: 'urn:linguaflow:external-flow-invalid', title: 'external_flow_invalid' },
      response: new Response(null, { status: 400 }),
    })
    await expect(completeExternalConfirmation({ username: 'tester' })).rejects.toThrow()
    expect(readExternalFlow()).toBeNull()
    await expect(completeExternalConfirmation({ username: 'tester' })).rejects.toThrow()
    expect(mocks.post).toHaveBeenCalledTimes(2)
  })

  it('generates independent cryptographic verifier/challenge pairs', async () => {
    const first = await createBrowserChallenge()
    const second = await createBrowserChallenge()
    expect(first.verifier).toMatch(/^[\w-]{43}$/)
    expect(first.verifier).not.toBe(second.verifier)
    expect(first.browser_challenge).toBe(
      Buffer.from(
        await webcrypto.subtle.digest('SHA-256', new TextEncoder().encode(first.verifier)),
      ).toString('base64url'),
    )
  })
  it('sends only the challenge to start and stores verifier with service and context', async () => {
    mocks.post.mockResolvedValue(
      reply({
        transaction_id: 'new',
        authorization_url: 'https://idp.example/authorize',
        expires_at: new Date(Date.now() + 60_000).toISOString(),
      }),
    )
    await startExternalLogin('oidc-one', '//evil.example')
    const [, { body }] = mocks.post.mock.calls[0]!
    expect(body).toMatchObject({
      return_to: '/',
      completion_url: 'https://frontend.example/auth/complete',
    })
    expect(body.verifier).toBeUndefined()
    expect(JSON.parse(storage.get(EXTERNAL_FLOW_KEY)!)).toHaveProperty('verifier')
    expect(JSON.parse(storage.get(EXTERNAL_FLOW_KEY)!)).toHaveProperty('stage', 'started')
    expect(readExternalFlow()).toMatchObject({
      purpose: 'login',
      connectionId: 'oidc-one',
      userId: null,
      returnTo: '/',
    })
    expect(mocks.assign).toHaveBeenCalledWith('https://idp.example/authorize')
  })
  it('keeps the newer concurrent start when an older start response arrives late', async () => {
    const resolvers: Array<(value: unknown) => void> = []
    mocks.post.mockImplementation(() => new Promise((resolve) => resolvers.push(resolve)))
    const older = startExternalLogin('oidc-one')
    const olderRejected = expect(older).rejects.toThrow()
    await vi.waitFor(() => expect(resolvers).toHaveLength(1))
    const newer = startExternalLogin('oidc-two')
    await vi.waitFor(() => expect(resolvers).toHaveLength(2))

    resolvers[1]!(
      reply({
        transaction_id: 'newer-transaction',
        authorization_url: 'https://idp.example/newer',
        expires_at: new Date(Date.now() + 60_000).toISOString(),
      }),
    )
    await newer
    resolvers[0]!(
      reply({
        transaction_id: 'older-transaction',
        authorization_url: 'https://idp.example/older',
        expires_at: new Date(Date.now() + 60_000).toISOString(),
      }),
    )
    await olderRejected
    expect(JSON.parse(storage.get(EXTERNAL_FLOW_KEY)!).transactionId).toBe('newer-transaction')
  })
  it.each(['base', 'epoch', 'user', 'expired'])(
    'rejects a %s mismatch without exchanging a ticket',
    async (reason) => {
      seed()
      if (reason === 'base') changeSessionContext('https://other.example/api/v1', null, true)
      if (reason === 'epoch') mocks.epoch = 'epoch-new'
      if (reason === 'user') changeSessionContext(captureSession().baseUrl, 2, true)
      if (reason === 'expired') seed(flow({ expiresAt: new Date(0).toISOString() }))
      await expect(exchangeExternalTicket('ticket')).rejects.toThrow()
      expect(mocks.post).not.toHaveBeenCalled()
    },
  )
  it('claims a ticket before the request, preventing duplicate exchange and replay after response loss', async () => {
    seed()
    mocks.post.mockRejectedValue(new TypeError('network'))
    await expect(exchangeExternalTicket('ticket')).rejects.toThrow('network')
    await expect(exchangeExternalTicket('ticket')).rejects.toThrow()
    expect(mocks.post).toHaveBeenCalledTimes(1)
  })
  it('does not replay an exchange after a 5xx response', async () => {
    seed()
    mocks.post.mockResolvedValue({
      error: { title: 'server_error' },
      response: new Response(null, { status: 503 }),
    })
    await expect(exchangeExternalTicket('ticket')).rejects.toThrow()
    await expect(exchangeExternalTicket('ticket')).rejects.toThrow()
    expect(mocks.post).toHaveBeenCalledTimes(1)
  })
  it('rejects a removed test result in an ordinary login flow and clears the flow', async () => {
    seed()
    mocks.post.mockResolvedValue(
      reply({
        kind: 'test_result',
        connection_id: 'oidc-one',
        revision: 1,
        fresh_supported: false,
        attributes: {},
        rules: {},
        test_id: 'test-one',
        expires_at: new Date(Date.now() + 20_000).toISOString(),
      }),
    )
    await expect(exchangeExternalTicket('ticket')).rejects.toThrow()
    expect(readExternalFlow()).toBeNull()
  })
  it('requires explicit signup confirmation and resumes it without another ticket exchange', async () => {
    seed()
    const returned = await exchangeAs(confirmation())
    expect(returned.kind).toBe('signup_confirmation')
    expect(returned).not.toHaveProperty('confirmation_token')
    const resumed = await exchangeExternalTicket()
    expect(resumed.kind).toBe('signup_confirmation')
    expect(resumed).not.toHaveProperty('confirmation_token')
    await expect(exchangeExternalTicket('ticket')).rejects.toThrow()
    expect(mocks.post).toHaveBeenCalledTimes(1)
    expect(mocks.post.mock.calls[0]?.[0]).toBe('/auth/external/exchange')
  })
  it('permits editing after a definite username conflict, but does not persist the password', async () => {
    seed()
    await exchangeAs(confirmation())
    mocks.post.mockResolvedValue({
      error: { type: 'urn:linguaflow:conflict', title: 'conflict' },
      response: new Response(null, { status: 409 }),
    })
    await expect(
      completeExternalConfirmation({ username: 'taken', initial_password: 'secret-password' }),
    ).rejects.toThrow()
    expect(readExternalFlow()?.stage).toBe('confirming')
    expect(storage.size).toBe(0)
    expect(Array.from(storage.values()).join('\n')).not.toContain('secret-password')
    mocks.post.mockResolvedValue(reply({ kind: 'session' }))
    await completeExternalConfirmation({ username: 'available' })
    await expect(completeExternalConfirmation({ username: 'another' })).rejects.toThrow()
    expect(mocks.post).toHaveBeenCalledTimes(3)
  })
  it('never automatically replays a completion whose response was lost', async () => {
    seed()
    await exchangeAs(confirmation())
    mocks.post.mockRejectedValue(new TypeError('network'))
    await expect(completeExternalConfirmation({ username: 'tester' })).rejects.toThrow()
    await expect(completeExternalConfirmation({ username: 'tester' })).rejects.toThrow()
    expect(mocks.post).toHaveBeenCalledTimes(2)
  })
  it('does not replay a completion after a 5xx response', async () => {
    seed()
    await exchangeAs(confirmation())
    mocks.post.mockResolvedValue({
      error: { title: 'server_error' },
      response: new Response(null, { status: 503 }),
    })
    await expect(completeExternalConfirmation({ username: 'tester' })).rejects.toThrow()
    await expect(completeExternalConfirmation({ username: 'tester' })).rejects.toThrow()
    expect(mocks.post).toHaveBeenCalledTimes(2)
  })
  it('does not let a late completion clear a newer transaction', async () => {
    seed()
    await exchangeAs(confirmation())
    let resolve!: (value: unknown) => void
    mocks.post.mockReturnValue(new Promise((value) => (resolve = value)))
    const pending = completeExternalConfirmation({ username: 'tester' })
    await vi.waitFor(() => expect(mocks.post).toHaveBeenCalledTimes(2))
    seed(flow({ transactionId: 'replacement-transaction' }))
    resolve(reply({ kind: 'session' }))
    await expect(pending).rejects.toThrow()
    expect(readExternalFlow()?.transactionId).toBe('replacement-transaction')
  })
  it('cancels with the original transaction and verifier without submitting a confirmation', async () => {
    seed()
    mocks.post.mockResolvedValue({ response: new Response(null, { status: 204 }) })
    await cancelExternalFlow()
    expect(mocks.post).toHaveBeenCalledWith('/auth/external/cancel', {
      body: { transaction_id: 'transaction-one', verifier: 'v'.repeat(43) },
    })
    expect(readExternalFlow()).toBeNull()
  })
  it('returns a reauthentication proof once for the same account, action and target', async () => {
    changeSessionContext(captureSession().baseUrl, 1, true)
    seed(
      flow({
        userId: 1,
        purpose: 'reauthenticate',
        connectionId: undefined,
        action: 'unlink',
        targetId: '9',
      }),
    )
    const proof = {
      kind: 'reauthentication_proof' as const,
      reauthentication_proof: 'one-proof',
      expires_at: new Date(Date.now() + 20_000).toISOString(),
    }
    expect(() => saveExternalResult(proof)).toThrow()
    const result = await exchangeAs({
      ...proof,
    })
    if (result.kind !== 'reauthentication_proof') throw new Error('Expected proof response')
    saveExternalResult(result)
    expect(storage.has(EXTERNAL_FLOW_KEY)).toBe(false)
    clearExternalFlow()
    expect(takeExternalProof()).toMatchObject({
      proof: 'one-proof',
      action: 'unlink',
      targetId: '9',
    })
    expect(takeExternalProof()).toBeNull()
  })
  it('removes a proof before rejecting a changed identity and never persists it', async () => {
    changeSessionContext(captureSession().baseUrl, 1, true)
    seed(
      flow({
        userId: 1,
        purpose: 'reauthenticate',
        connectionId: undefined,
        action: 'unlink',
        targetId: '9',
      }),
    )
    const result = await exchangeAs({
      kind: 'reauthentication_proof',
      reauthentication_proof: 'one-proof',
      expires_at: new Date(Date.now() + 20_000).toISOString(),
    })
    if (result.kind !== 'reauthentication_proof') throw new Error('Expected proof response')
    saveExternalResult(result)
    expect(storage.size).toBe(0)

    changeSessionContext(captureSession().baseUrl, 2, true)
    expect(takeExternalProof()).toBeNull()
    changeSessionContext(captureSession().baseUrl, 1, true)
    expect(takeExternalProof()).toBeNull()
  })
  it('clears a queued proof only through clearExternalProof', async () => {
    changeSessionContext(captureSession().baseUrl, 1, true)
    seed(
      flow({
        userId: 1,
        purpose: 'reauthenticate',
        connectionId: undefined,
        action: 'unlink',
        targetId: '9',
      }),
    )
    const result = await exchangeAs({
      kind: 'reauthentication_proof',
      reauthentication_proof: 'one-proof',
      expires_at: new Date(Date.now() + 20_000).toISOString(),
    })
    if (result.kind !== 'reauthentication_proof') throw new Error('Expected proof response')
    saveExternalResult(result)
    clearExternalProof()
    expect(takeExternalProof()).toBeNull()
  })
})
describe('application return paths', () => {
  it.each([
    '//evil.example',
    'https://evil.example',
    '/\\evil.example',
    '/%2fevil.example',
    '/%255cevil.example',
    '/%0aevil',
    '/login',
    '/auth/complete?ticket=secret',
  ])('rejects %s', (path) => {
    expect(safeReturnPath(path)).toBe('/')
  })
  it('preserves a valid local path and query', () =>
    expect(safeReturnPath('/projects/2?tab=history')).toBe('/projects/2?tab=history'))
})
