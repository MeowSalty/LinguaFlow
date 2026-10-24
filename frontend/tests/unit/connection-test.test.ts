import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { webcrypto } from 'node:crypto'
import type { ApiSchemas } from '../../src/api/client-core'
import { type AuthConnection } from '../../src/api/auth-connections'
import { changeSessionContext } from '../../src/api/session-context'
import {
  CONNECTION_TEST_RELAY_KEY,
  applyConnectionTestEvidence,
  matchConnectionTest,
  relayConnectionTestTicket,
  startConnectionTest,
  testEvidenceFields,
  type ConnectionTestHandle,
} from '../../src/auth/connection-test'
import { newConnectionSnapshot } from '../../src/components/admin/authentication/auth-connection-model'

const mocks = vi.hoisted(() => ({ post: vi.fn(), epoch: 'epoch-a' }))
vi.mock('@/i18n', () => ({ t: (key: string) => key }))
vi.mock('@/api/client-core', () => ({ apiClient: { POST: mocks.post } }))
vi.mock('@/api/token-storage', () => ({ getAuthEpoch: () => mocks.epoch }))

type Popup = {
  closed: boolean
  opener: Window | null
  location: { href: string }
  sessionStorage: Storage
  close: ReturnType<typeof vi.fn>
}

const channels: FakeChannel[] = []
const activeHandles: ConnectionTestHandle[] = []
class FakeChannel {
  readonly name: string
  onmessage: ((event: MessageEvent) => void) | null = null
  readonly close = vi.fn()
  readonly postMessage = vi.fn()
  constructor(name: string) {
    this.name = name
    channels.push(this)
  }
}

let popup: Popup
let popupStorage: Map<string, string>
let opened: Array<{ url: string; name: string; features?: string }> = []
const now = () => new Date(Date.now() + 60_000).toISOString()
const result = (data: unknown) => ({ data, response: new Response(null, { status: 200 }) })
const ticketResult = (patch: Partial<ApiSchemas['ExternalTestResponse']> = {}) => ({
  kind: 'test_result' as const,
  connection_id: 'company',
  revision: 7,
  test_id: 'tx-test',
  fresh_supported: true,
  attributes: {
    email: {
      value: 'person@example.com',
      status: 'pass' as const,
      fingerprint: 'fingerprint-email',
    },
  },
  rules: { login: {}, signup: {}, link: {} },
  expires_at: now(),
  ...patch,
})

const record = (patch: Partial<AuthConnection> = {}): AuthConnection => ({
  id: 'company',
  kind: 'oidc',
  aggregate_revision: 7,
  state: 'enabled',
  archived: false,
  configuration: {
    ...newConnectionSnapshot(),
    display_name: 'Company SSO',
    oidc: {
      issuer: 'https://identity.example.com',
      client_id: 'client',
      client_auth_method: 'client_secret_basic',
      scopes: ['openid', 'profile'],
      userinfo_enabled: true,
    },
    has_client_secret: true,
  },
  configuration_complete: true,
  capabilities: { can_login: true, can_signup: false, can_link: false, can_test: true },
  validation: { fresh_supported: true, tested_at: null },
  setup: {
    callback_url: 'https://app.example/auth/callback/company',
    completion_urls: ['https://app.example/auth/complete'],
    configuration_complete: true,
    issues: [],
    protocol: { required_fields: [], client_auth_methods: [], scope_guidance: '' },
  },
  ...patch,
})

const dispatch = (data: unknown, origin = 'https://frontend.example') => {
  const channel = channels[0]
  expect(channel).toBeDefined()
  void channel!.onmessage?.({ data, origin } as MessageEvent)
}
const flush = async () => {
  await Promise.resolve()
  await Promise.resolve()
  await new Promise((resolve) => setTimeout(resolve, 0))
  await Promise.resolve()
}
const waitForStart = async () => {
  await vi.waitFor(() => expect(mocks.post).toHaveBeenCalled(), { timeout: 1000 })
  await flush()
}
const begin = (...args: Parameters<typeof startConnectionTest>): ConnectionTestHandle => {
  const handle = startConnectionTest(...args)
  void handle.result.catch(() => undefined)
  activeHandles.push(handle)
  return handle
}
const startResponse = () =>
  result({
    transaction_id: 'tx-test',
    authorization_url: 'https://idp.example/authorize',
    expires_at: now(),
  })

const uncaught: unknown[] = []
process.on('unhandledRejection', (reason) => uncaught.push(reason))

beforeEach(() => {
  channels.length = 0
  mocks.post.mockReset()
  mocks.epoch = 'epoch-a'
  popupStorage = new Map()
  popup = {
    closed: false,
    opener: null,
    location: { href: '' },
    sessionStorage: {
      getItem: (key: string) => popupStorage.get(key) ?? null,
      setItem: (key: string, value: string) => popupStorage.set(key, value),
      removeItem: (key: string) => popupStorage.delete(key),
      clear: () => popupStorage.clear(),
      key: (index: number) => [...popupStorage.keys()][index] ?? null,
      get length() {
        return popupStorage.size
      },
    } as Storage,
    close: vi.fn(() => {
      popup.closed = true
    }),
  }
  opened = []
  vi.stubGlobal('crypto', webcrypto)
  vi.stubGlobal('BroadcastChannel', FakeChannel)
  vi.stubGlobal('location', { origin: 'https://frontend.example' })
  vi.stubGlobal(
    'window',
    Object.assign(new EventTarget(), {
      open: (url: string, name: string, features?: string) => {
        opened.push({ url, name, features })
        return popup
      },
      close: vi.fn(),
    }),
  )
  changeSessionContext('https://service.example/api/v1', 42, true)
})

afterEach(async () => {
  for (const handle of activeHandles.splice(0)) {
    void handle.result.catch(() => undefined)
    await handle.cancel()
  }
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('administrator connection test relay', () => {
  it('opens synchronously and does not POST when the popup is blocked', () => {
    vi.stubGlobal('window', { open: vi.fn(() => null) })
    expect(() => startConnectionTest(record())).toThrow('authAdmin.popupBlocked')
    expect(mocks.post).not.toHaveBeenCalled()
  })

  it('opens a unique blank popup, removes opener, and starts with challenge plus memory changes', async () => {
    mocks.post.mockResolvedValue(startResponse())
    const inheritedKeys = [
      'linguaflow.external-flow.v1',
      'linguaflow.external-flow.v2',
      'linguaflow.external-flow.v2.probe',
      'linguaflow.external-result.v1',
    ]
    for (const key of inheritedKeys) popupStorage.set(key, 'previous-external-auth')
    const changes = { oidc: { issuer: 'https://candidate.example' }, client_secret: 'secret' }
    const handle = begin(record(), changes)
    expect(opened[0]).toMatchObject({
      url: 'about:blank',
      name: expect.stringMatching(/^linguaflow-auth-test-/),
    })
    expect(popup.opener).toBeNull()
    for (const key of inheritedKeys) expect(popupStorage.has(key)).toBe(false)
    await waitForStart()
    const [path, options] = mocks.post.mock.calls[0]!
    expect(path).toBe('/admin/auth-connections/{connectionId}/test')
    expect(options.body).toMatchObject({
      expected_revision: 7,
      completion_url: 'https://frontend.example/auth/complete',
      return_to: '/admin/authentication',
      changes,
    })
    expect(options.body.browser_challenge).toMatch(/^[\w-]{43}$/)
    expect(options.body.verifier).toBeUndefined()
    expect(JSON.parse(popupStorage.get(CONNECTION_TEST_RELAY_KEY)!)).toMatchObject({
      transactionId: 'tx-test',
      channel: expect.stringMatching(/^linguaflow-auth-test-/),
    })
    expect(popup.location.href).toBe('https://idp.example/authorize')
    await handle.cancel()
  })

  it('keeps proof and candidate secret only in memory, and cancels the transaction with verifier', async () => {
    mocks.post.mockResolvedValueOnce(startResponse()).mockResolvedValueOnce(result(ticketResult()))
    const handle = begin(record(), { client_secret: 'candidate-secret' })
    await waitForStart()
    const startBody = mocks.post.mock.calls[0]![1].body
    expect(startBody).not.toHaveProperty('verifier')
    expect(popupStorage.has(CONNECTION_TEST_RELAY_KEY)).toBe(true)
    dispatch({ type: 'ticket', transactionId: 'tx-test', ticket: 'ticket-one' })
    const evidence = await handle.result
    expect(evidence.verifier).toMatch(/^[\w-]{43}$/)
    expect(evidence.candidate.secret).toBe('candidate-secret')
    expect(testEvidenceFields(evidence)).toEqual({
      test_id: 'tx-test',
      verifier: evidence.verifier,
    })
    expect(mocks.post.mock.calls[1]![1]).toEqual({
      body: { ticket: 'ticket-one', verifier: evidence.verifier },
    })
    await handle.cancel()
    expect(evidence.cancelled).toBe(true)
    expect(evidence.verifier).toBe('')
    expect(evidence.candidate.secret).toBeNull()
    expect(mocks.post.mock.calls[2]).toEqual([
      '/auth/external/cancel',
      { body: { transaction_id: 'tx-test', verifier: expect.any(String) } },
    ])
  })

  it.each([
    [
      'origin',
      { type: 'ticket', transactionId: 'tx-test', ticket: 'wrong-origin' },
      'https://evil.example',
    ],
    [
      'transaction',
      { type: 'ticket', transactionId: 'other', ticket: 'wrong-transaction' },
      'https://frontend.example',
    ],
  ])('ignores a wrong %s ticket without exchanging it', async (_name, message, origin) => {
    mocks.post.mockResolvedValue(startResponse())
    const handle = begin(record())
    await waitForStart()
    dispatch(message, origin)
    await flush()
    expect(mocks.post).toHaveBeenCalledTimes(1)
    await handle.cancel()
  })

  it('cancels and rejects when the authenticated user changes', async () => {
    mocks.post.mockResolvedValue(startResponse())
    const handle = begin(record())
    await waitForStart()
    changeSessionContext('https://service.example/api/v1', 99, true)
    await expect(handle.result).rejects.toThrow('authAdmin.testInvalid')
    expect(mocks.post).toHaveBeenCalledTimes(1)
  })

  it.each([
    ['connection', { connection_id: 'other' }],
    ['expiry', { expires_at: new Date(0).toISOString() }],
    ['transaction', { test_id: 'other-transaction' }],
    ['revision', { revision: 8 }],
  ])('does not accept a server result with a wrong %s', async (_name, patch) => {
    mocks.post
      .mockResolvedValueOnce(startResponse())
      .mockResolvedValueOnce(result(ticketResult(patch)))
    const handle = begin(record())
    await waitForStart()
    dispatch({ type: 'ticket', transactionId: 'tx-test', ticket: 'ticket-one' })
    await expect(handle.result).rejects.toThrow()
    expect(mocks.post.mock.calls[1]![1].body.ticket).toBe('ticket-one')
  })

  it('cancels in flight start requests and cancels a late transaction without navigating', async () => {
    let resolveStart!: (value: unknown) => void
    mocks.post.mockReturnValueOnce(
      new Promise((resolve) => {
        resolveStart = resolve
      }),
    )
    const handle = begin(record())
    await waitForStart()
    await handle.cancel()
    expect(popup.location.href).toBe('')
    resolveStart(startResponse())
    await flush()
    expect(mocks.post).toHaveBeenCalledTimes(2)
    expect(mocks.post.mock.calls[1]).toEqual([
      '/auth/external/cancel',
      { body: { transaction_id: 'tx-test', verifier: expect.any(String) } },
    ])
    expect(popup.location.href).toBe('')
  })

  it('rejects an in-flight exchange on cancellation and ignores its late result', async () => {
    let finishExchange!: (value: unknown) => void
    mocks.post
      .mockResolvedValueOnce(startResponse())
      .mockReturnValueOnce(
        new Promise((resolve) => {
          finishExchange = resolve
        }),
      )
      .mockResolvedValueOnce(result(null))
    const handle = begin(record(), { client_secret: 'candidate-secret' })
    await waitForStart()
    dispatch({ type: 'ticket', transactionId: 'tx-test', ticket: 'ticket-one' })
    await flush()
    expect(mocks.post).toHaveBeenCalledTimes(2)
    await handle.cancel()
    finishExchange(result(ticketResult()))
    await expect(handle.result).rejects.toThrow('authAdmin.testCancelled')
    await flush()
    expect(channels[0]?.postMessage).not.toHaveBeenCalled()
    expect(mocks.post).toHaveBeenCalledTimes(3)
    expect(channels[0]?.close).toHaveBeenCalled()
  })

  it('waits for real webcrypto digest before start and awaits in-flight cancellation', async () => {
    const originalDigest = webcrypto.subtle.digest.bind(webcrypto.subtle)
    const digest = vi.fn(async (algorithm: AlgorithmIdentifier, data: AllowSharedBufferSource) => {
      await new Promise((resolve) => setTimeout(resolve, 20))
      return originalDigest(algorithm, data)
    })
    Object.defineProperty(webcrypto.subtle, 'digest', {
      value: digest,
      configurable: true,
    })
    let startResolve!: (value: ReturnType<typeof startResponse>) => void
    let cancelled = 0
    mocks.post.mockImplementation(async (path: string) => {
      if (path === '/admin/auth-connections/{connectionId}/test')
        return new Promise((resolve) => {
          startResolve = resolve
        })
      cancelled += 1
      return { data: undefined, error: undefined, response: new Response(null, { status: 204 }) }
    })
    const handle = startConnectionTest(record())
    const rejection = handle.result.catch((error: unknown) => error)
    await vi.waitFor(() => {
      expect(startResolve).toBeTypeOf('function')
      expect(digest).toHaveBeenCalledTimes(1)
    })
    expect(mocks.post.mock.calls[0]?.[0]).toBe('/admin/auth-connections/{connectionId}/test')
    await handle.cancel()
    expect(await rejection).toBeInstanceOf(Error)
    startResolve(startResponse())
    await flush()
    expect(mocks.post.mock.calls.map(([path]) => path)).toEqual([
      '/admin/auth-connections/{connectionId}/test',
      '/auth/external/cancel',
    ])
    expect(cancelled).toBe(1)
    expect(popup.location.href).toBe('')
    expect(uncaught).toEqual([])
  })

  it('deep clones the starting record, applies a full tested candidate once, and guards reuse', async () => {
    const base = record()
    const savedProtocol = { ...base.configuration.oidc! }
    const testedProtocol = { ...base.configuration.oidc!, issuer: 'https://candidate.example' }
    const source = record({
      configuration: { ...base.configuration, oidc: { ...testedProtocol } },
    })
    mocks.post.mockResolvedValueOnce(startResponse()).mockResolvedValueOnce(result(ticketResult()))
    const handle = begin(source, { oidc: { ...testedProtocol } })
    source.aggregate_revision = 99
    source.id = 'mutated'
    source.configuration.oidc!.issuer = 'https://mutated.example'
    await waitForStart()
    expect(mocks.post.mock.calls[0]![1].body.expected_revision).toBe(7)
    dispatch({ type: 'ticket', transactionId: 'tx-test', ticket: 'ticket-one' })
    const evidence = await handle.result
    expect(evidence.result.connection_id).toBe('company')
    expect(mocks.post.mock.calls[1]![0]).toBe('/auth/external/exchange')
    expect(evidence.applied).toBe(false)
    const current = record({
      configuration: { ...base.configuration, oidc: testedProtocol },
      validation: {
        fresh_supported: true,
        tested_at: '2026-10-16T02:00:00Z',
        applied_test_id: 'tx-test',
      },
    })
    current.aggregate_revision = 8
    const applied = applyConnectionTestEvidence(evidence, current)
    expect(applied).toBe(evidence)
    expect(evidence.revision).toBe(8)
    expect(evidence.applied).toBe(true)
    expect(evidence.candidate.protocol).toEqual(testedProtocol)
    expect(applyConnectionTestEvidence(evidence, current)).toBeNull()
    expect(matchConnectionTest(evidence, current)).toBe(false)
    expect(matchConnectionTest(evidence, current, {}, 'enable')).toBe(true)
    expect(matchConnectionTest(evidence, current, { client_secret: 'different' }, 'enable')).toBe(
      false,
    )
    expect(
      matchConnectionTest(
        evidence,
        current,
        {
          policy: {
            ...current.configuration.policy,
            login_enabled: !current.configuration.policy.login_enabled,
          },
        },
        'enable',
      ),
    ).toBe(false)

    const wrongRevision = record({
      configuration: { ...base.configuration, oidc: savedProtocol },
      validation: {
        fresh_supported: true,
        tested_at: '2026-10-16T02:00:00Z',
        applied_test_id: 'tx-test',
      },
    })
    wrongRevision.aggregate_revision = 9
    const replay = { ...evidence, applied: false, revision: 7 }
    expect(applyConnectionTestEvidence(replay, wrongRevision)).toBeNull()

    const differentRule = record({
      configuration: { ...base.configuration, oidc: savedProtocol },
      validation: {
        fresh_supported: true,
        tested_at: '2026-10-16T02:00:00Z',
        applied_test_id: 'tx-rule',
      },
    })
    differentRule.aggregate_revision = 8
    const otherEvidence = {
      ...evidence,
      applied: false,
      candidate: {
        ...evidence.candidate,
        policy: { ...evidence.candidate.policy, login_enabled: true },
      },
    }
    expect(applyConnectionTestEvidence(otherEvidence, differentRule)).toBeNull()

    await handle.cancel()
  })

  it('matches candidates with equivalent object key order', async () => {
    const base = record()
    const protocol = { ...base.configuration.oidc!, scopes: ['openid', 'profile'] }
    mocks.post.mockResolvedValueOnce(startResponse()).mockResolvedValueOnce(result(ticketResult()))
    const handle = begin(record(), { oidc: { ...protocol } })
    await waitForStart()
    dispatch({ type: 'ticket', transactionId: 'tx-test', ticket: 'ticket-one' })
    const evidence = await handle.result
    expect(
      matchConnectionTest(evidence, record(), {
        oidc: {
          client_id: 'client',
          issuer: 'https://identity.example.com',
          userinfo_enabled: true,
          scopes: ['openid', 'profile'],
          client_auth_method: 'client_secret_basic',
        },
      }),
    ).toBe(true)
    await handle.cancel()
  })

  it('relay consumes only a valid marker and sends a ticket over the named channel', () => {
    const expiresAt = now()
    const storage = new Map([
      [
        CONNECTION_TEST_RELAY_KEY,
        JSON.stringify({
          channel: 'linguaflow-auth-test-channel',
          transactionId: 'tx-test',
          expiresAt,
        }),
      ],
    ])
    vi.stubGlobal('sessionStorage', {
      getItem: (key: string) => storage.get(key) ?? null,
      removeItem: (key: string) => storage.delete(key),
    })
    expect(relayConnectionTestTicket('ticket-one')).toBe(true)
    expect(storage.has(CONNECTION_TEST_RELAY_KEY)).toBe(false)
    expect(channels[0]?.postMessage).toHaveBeenCalledWith({
      type: 'ticket',
      transactionId: 'tx-test',
      ticket: 'ticket-one',
    })
    const channel = channels[0]!
    channel.onmessage?.({
      origin: 'https://wrong.example',
      data: { type: 'complete', transactionId: 'tx-test' },
    } as MessageEvent)
    channel.onmessage?.({
      origin: location.origin,
      data: { type: 'complete', transactionId: 'wrong' },
    } as MessageEvent)
    expect(channel.close).not.toHaveBeenCalled()
    window.dispatchEvent(new Event('pagehide'))
    expect(channel.close).toHaveBeenCalledOnce()
    channel.onmessage?.({
      origin: location.origin,
      data: { type: 'complete', transactionId: 'tx-test' },
    } as MessageEvent)
    expect(window.close).not.toHaveBeenCalled()
    expect(relayConnectionTestTicket('ticket-one')).toBe(false)
  })

  it('bounds evidence fields to the live authenticated memory context', async () => {
    mocks.post.mockResolvedValueOnce(startResponse()).mockResolvedValueOnce(result(ticketResult()))
    const handle = begin(record())
    await waitForStart()
    dispatch({ type: 'ticket', transactionId: 'tx-test', ticket: 'ticket-one' })
    const evidence = await handle.result
    expect(matchConnectionTest(evidence, record())).toBe(true)
    changeSessionContext('https://service.example/api/v1', 99, true)
    expect(evidence.cancelled).toBe(true)
    expect(evidence.verifier).toBe('')
    expect(evidence.candidate.secret).toBeNull()
    expect(matchConnectionTest(evidence, record())).toBe(false)
    expect(() => testEvidenceFields(evidence)).toThrow('authAdmin.testExpired')
    await handle.cancel()
  })

  it('retains a completed proof until its original deadline, then clears sensitive memory', async () => {
    vi.useFakeTimers({ toFake: ['Date', 'setInterval', 'clearInterval'] })
    const expiresAt = new Date(Date.now() + 60_000).toISOString()
    mocks.post
      .mockResolvedValueOnce(startResponse())
      .mockResolvedValueOnce(
        result(ticketResult({ expires_at: new Date(Date.now() + 120_000).toISOString() })),
      )
    const handle = begin(record(), { client_secret: 'candidate-secret' })
    await waitForStart()
    dispatch({ type: 'ticket', transactionId: 'tx-test', ticket: 'ticket-one' })
    const evidence = await handle.result
    expect(evidence.expiresAt).toBe(expiresAt)
    expect(popup.closed).toBe(true)
    // Closing the relay window on success must not invalidate the retained proof.
    await vi.advanceTimersByTimeAsync(500)
    expect(evidence.cancelled).toBe(false)
    await vi.advanceTimersByTimeAsync(60_000)
    expect(evidence.cancelled).toBe(true)
    expect(evidence.verifier).toBe('')
    expect(evidence.candidate.secret).toBeNull()
  })
})
