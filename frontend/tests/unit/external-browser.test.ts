import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { webcrypto } from 'node:crypto'
import { captureSession, changeSessionContext } from '../../src/api/session-context'
import {
  EXTERNAL_FLOW_KEY,
  createBrowserChallenge,
  readExternalFlow,
  exchangeExternalTicket,
  completeExternalConfirmation,
  cancelExternalFlow,
  startExternalLogin,
  startExternalTest,
  saveExternalResult,
  takeExternalProof,
  type ExternalFlow,
} from '../../src/auth/external-browser'
import { safeReturnPath } from '../../src/auth/return-path'

const mocks = vi.hoisted(() => ({ post: vi.fn(), epoch: 'epoch-a', assign: vi.fn() }))
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
afterEach(() => vi.unstubAllGlobals())
const flow = (patch: Partial<ExternalFlow> = {}): ExternalFlow => ({
  baseUrl: captureSession().baseUrl,
  epoch: mocks.epoch,
  userId: null,
  purpose: 'login',
  connectionId: 'oidc-one',
  verifier: 'v'.repeat(43),
  transactionId: 'transaction-one',
  expiresAt: new Date(Date.now() + 60_000).toISOString(),
  returnTo: '/projects',
  stage: 'started',
  ...patch,
})
const seed = (value = flow()) => storage.set(EXTERNAL_FLOW_KEY, JSON.stringify(value))
const confirmation = () => ({
  kind: 'signup_confirmation' as const,
  transaction_id: 'transaction-one',
  confirmation_token: 'confirmation-one',
  expires_at: new Date(Date.now() + 40_000).toISOString(),
  signup_requirements: ['local_password' as const],
  profile: {
    connection_id: 'oidc-one',
    connection_name: 'Company',
    external_display: 'External user',
  },
})
const reply = (data: unknown) => ({ data, response: new Response(null, { status: 200 }) })
beforeEach(() => {
  mocks.epoch = 'epoch-a'
  mocks.post.mockReset()
  mocks.assign.mockReset()
  storage = new Map()
  vi.stubGlobal('sessionStorage', {
    getItem: (key: string) => storage.get(key) ?? null,
    setItem: (key: string, value: string) => storage.set(key, value),
    removeItem: (key: string) => storage.delete(key),
  })
  vi.stubGlobal('crypto', webcrypto)
  vi.stubGlobal('location', { origin: 'https://frontend.example', assign: mocks.assign })
  changeSessionContext('https://service.example/api/v1', null, true)
})
describe('external browser transactions', () => {
  it('uses the aggregate revision to start a test and the snapshot revision to validate its result', async () => {
    changeSessionContext(captureSession().baseUrl, 1, true)
    mocks.post.mockResolvedValueOnce(
      reply({
        transaction_id: 'tx-test',
        authorization_url: 'https://idp.example/authorize',
        expires_at: new Date(Date.now() + 60_000).toISOString(),
      }),
    )
    await startExternalTest('oidc-one', 7, 4)
    expect(mocks.post.mock.calls[0]?.[1].body.expected_revision).toBe(7)
    mocks.post.mockResolvedValueOnce(
      reply({
        kind: 'test_result',
        connection_id: 'oidc-one',
        revision: 4,
        fresh_supported: false,
        attributes: {},
        rules: {},
      }),
    )
    expect((await exchangeExternalTicket('ticket')).kind).toBe('test_result')
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

  it('does not reenable a confirmation after the server marks its transaction invalid', async () => {
    seed(flow({ stage: 'confirming', confirmation: confirmation() }))
    mocks.post.mockResolvedValue({
      error: { type: 'urn:linguaflow:external-flow-invalid', title: 'external_flow_invalid' },
      response: new Response(null, { status: 400 }),
    })
    await expect(completeExternalConfirmation({ username: 'tester' })).rejects.toThrow()
    expect(readExternalFlow()?.stage).toBe('completing')
    await expect(completeExternalConfirmation({ username: 'tester' })).rejects.toThrow()
    expect(mocks.post).toHaveBeenCalledTimes(1)
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
    expect(readExternalFlow()).toMatchObject({
      purpose: 'login',
      connectionId: 'oidc-one',
      userId: null,
      returnTo: '/',
    })
    expect(mocks.assign).toHaveBeenCalledWith('https://idp.example/authorize')
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
  it('does not turn an administrator test into a login session', async () => {
    changeSessionContext(captureSession().baseUrl, 1, true)
    seed(flow({ userId: 1, purpose: 'test', revision: 3 }))
    mocks.post.mockResolvedValue(reply({ kind: 'session', session: {} }))
    await expect(exchangeExternalTicket('ticket')).rejects.toThrow()
    expect(readExternalFlow()).toBeNull()
  })
  it('requires explicit signup confirmation and resumes it without another ticket exchange', async () => {
    seed()
    mocks.post.mockResolvedValue(reply(confirmation()))
    expect((await exchangeExternalTicket('ticket')).kind).toBe('signup_confirmation')
    expect((await exchangeExternalTicket()).kind).toBe('signup_confirmation')
    expect(mocks.post).toHaveBeenCalledTimes(1)
    expect(mocks.post.mock.calls[0]?.[0]).toBe('/auth/external/exchange')
  })
  it('permits editing after a definite username conflict, but does not persist the password', async () => {
    seed(flow({ stage: 'confirming', confirmation: confirmation() }))
    mocks.post.mockResolvedValue({
      error: { title: 'Username unavailable' },
      response: new Response(null, { status: 409 }),
    })
    await expect(
      completeExternalConfirmation({ username: 'taken', initial_password: 'secret-password' }),
    ).rejects.toThrow()
    expect(readExternalFlow()?.stage).toBe('confirming')
    expect(storage.get(EXTERNAL_FLOW_KEY)).not.toContain('secret-password')
  })
  it('never automatically replays a completion whose response was lost', async () => {
    seed(flow({ stage: 'confirming', confirmation: confirmation() }))
    mocks.post.mockRejectedValue(new TypeError('network'))
    await expect(completeExternalConfirmation({ username: 'tester' })).rejects.toThrow()
    await expect(completeExternalConfirmation({ username: 'tester' })).rejects.toThrow()
    expect(mocks.post).toHaveBeenCalledTimes(1)
  })
  it('cancels with the original transaction and verifier without submitting a confirmation', async () => {
    seed(flow({ stage: 'confirming', confirmation: confirmation() }))
    mocks.post.mockResolvedValue({ response: new Response(null, { status: 204 }) })
    await cancelExternalFlow()
    expect(mocks.post).toHaveBeenCalledWith('/auth/external/cancel', {
      body: { transaction_id: 'transaction-one', verifier: 'v'.repeat(43) },
    })
    expect(readExternalFlow()).toBeNull()
  })
  it('returns a reauthentication proof once for the same account, action and target', () => {
    changeSessionContext(captureSession().baseUrl, 1, true)
    seed(flow({ userId: 1, purpose: 'reauthenticate', action: 'unlink', targetId: '9' }))
    saveExternalResult({
      kind: 'reauthentication_proof',
      reauthentication_proof: 'one-proof',
      expires_at: new Date(Date.now() + 20_000).toISOString(),
    })
    expect(takeExternalProof()).toMatchObject({
      proof: 'one-proof',
      action: 'unlink',
      targetId: '9',
    })
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
