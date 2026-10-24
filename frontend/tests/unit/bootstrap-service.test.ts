import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'

const mocks = vi.hoisted(() => ({
  get: vi.fn(),
  create: vi.fn(),
  capture: vi.fn(),
  fetchMode: vi.fn(),
  setBaseUrl: vi.fn(),
  setLocalMode: vi.fn(),
  ping: vi.fn(),
  stored: null as string | null,
  context: { signal: new AbortController().signal },
  current: true,
}))

vi.mock('@/api/client-core', () => ({
  createApiClient: mocks.create,
  setApiBaseUrl: mocks.setBaseUrl,
  setLocalMode: mocks.setLocalMode,
  pingService: mocks.ping,
}))
vi.mock('@/api/token-storage', () => ({
  readStoredApiBaseUrl: () => mocks.stored,
  clearStoredApiBaseUrl: () => {
    mocks.stored = null
  },
  getDefaultTokenStorage: () => ({
    getItem: (key: string) => (key === 'linguaflow.api_base_url' ? mocks.stored : null),
    setItem: vi.fn(),
    removeItem: vi.fn(),
  }),
}))
vi.mock('@/api/session-core', () => ({
  captureSession: mocks.capture,
  isSessionCurrent: (context: typeof mocks.context) =>
    mocks.current && context === mocks.context && !context.signal.aborted,
  normalizeApiBaseUrl: (value = '/api/v1') =>
    new URL(value, 'https://app.example').href.replace(/\/+$/, ''),
}))
vi.mock('@/api/system', () => ({ fetchMode: mocks.fetchMode }))

import { probeService, resolveBootstrapService } from '@/api/bootstrap-service'
import { useServiceStore } from '@/stores/service'

const response = (data: unknown, ok = true) => ({ data, response: { ok } })
const health = { status: 'ok', service: 'LinguaFlow' }
const deferred = <T>() => {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((complete) => {
    resolve = complete
  })
  return { promise, resolve }
}
const switchContext = (change: string, controller: AbortController) => {
  if (change === 'session') mocks.context = { signal: new AbortController().signal }
  if (change === 'selection') mocks.stored = 'https://new.example/api/v1'
  if (change === 'caller') controller.abort()
}

beforeEach(() => {
  mocks.get.mockReset()
  mocks.create.mockReset().mockImplementation(() => ({ GET: mocks.get }))
  mocks.capture.mockReset().mockImplementation(() => mocks.context)
  mocks.fetchMode.mockReset()
  mocks.setBaseUrl.mockReset().mockImplementation((url: string) => {
    mocks.stored = url
  })
  mocks.setLocalMode.mockReset()
  mocks.ping.mockReset()
  mocks.stored = null
  mocks.context = { signal: new AbortController().signal }
  mocks.current = true
  setActivePinia(createPinia())
})

describe('bootstrap service resolution', () => {
  it('prefers the default service when it is local', async () => {
    mocks.stored = 'https://remote.example/api/v1'
    mocks.get
      .mockResolvedValueOnce(response(health))
      .mockResolvedValueOnce(response({ mode: 'local' }))
    await expect(resolveBootstrapService()).resolves.toMatchObject({
      baseUrl: '/api/v1',
      mode: 'local',
    })
    expect(mocks.get).toHaveBeenCalledTimes(2)
    expect(mocks.create).toHaveBeenCalledWith({
      baseUrl: '/api/v1',
      getAccessToken: expect.any(Function),
    })
    expect(mocks.capture).toHaveBeenCalledTimes(1)
  })

  it('does not select a default server without an explicit stored service', async () => {
    mocks.get
      .mockResolvedValueOnce(response(health))
      .mockResolvedValueOnce(response({ mode: 'server' }))
    await expect(resolveBootstrapService()).resolves.toBeNull()
  })

  it('reuses the default server probe when the stored URL normalizes to default', async () => {
    mocks.stored = ' /api/v1/ '
    mocks.get
      .mockResolvedValueOnce(response(health))
      .mockResolvedValueOnce(response({ mode: 'server' }))
    await expect(resolveBootstrapService()).resolves.toMatchObject({
      baseUrl: '/api/v1',
      mode: 'server',
    })
    expect(mocks.get).toHaveBeenCalledTimes(2)
  })

  it('selects a separately stored service after the default server probe', async () => {
    mocks.stored = 'https://remote.example/api/v1'
    mocks.get
      .mockResolvedValueOnce(response(health))
      .mockResolvedValueOnce(response({ mode: 'server' }))
      .mockResolvedValueOnce(response({ status: 'ok', service: 'Remote' }))
      .mockResolvedValueOnce(response({ mode: 'server' }))
    await expect(resolveBootstrapService()).resolves.toMatchObject({
      baseUrl: 'https://remote.example/api/v1',
      mode: 'server',
      health: { service: 'Remote' },
    })
    expect(mocks.get).toHaveBeenCalledTimes(4)
    expect(mocks.create.mock.calls.map(([options]) => options.baseUrl)).toEqual([
      '/api/v1',
      'https://remote.example/api/v1',
    ])
    expect(mocks.capture).toHaveBeenCalledTimes(1)
  })

  it('returns null for an aborted caller or a late session result', async () => {
    const controller = new AbortController()
    controller.abort()
    await expect(probeService('/api/v1', controller.signal)).resolves.toBeNull()
    expect(mocks.create).not.toHaveBeenCalled()
    expect(mocks.get).not.toHaveBeenCalled()

    mocks.get.mockResolvedValueOnce(response(health))
    mocks.current = false
    await expect(probeService('/api/v1')).resolves.toBeNull()
    expect(mocks.create).not.toHaveBeenCalled()
  })

  it.each(['session', 'selection', 'caller'])(
    'stops before mode and stored probes when %s changes during default health await',
    async (change) => {
      mocks.stored = 'https://remote.example/api/v1'
      const controller = new AbortController()
      const waiting = deferred<ReturnType<typeof response>>()
      mocks.get.mockReturnValueOnce(waiting.promise)
      const pending = resolveBootstrapService(controller.signal)

      switchContext(change, controller)
      waiting.resolve(response(health))

      await expect(pending).resolves.toBeNull()
      expect(mocks.get).toHaveBeenCalledTimes(1)
      expect(mocks.create).toHaveBeenCalledTimes(1)
      expect(mocks.capture).toHaveBeenCalledTimes(1)
    },
  )

  it.each(['session', 'selection', 'caller'])(
    'does not start a stored probe when %s changes during default mode await',
    async (change) => {
      mocks.stored = 'https://remote.example/api/v1'
      const controller = new AbortController()
      const waiting = deferred<ReturnType<typeof response>>()
      mocks.get.mockResolvedValueOnce(response(health)).mockReturnValueOnce(waiting.promise)
      const pending = resolveBootstrapService(controller.signal)
      await vi.waitFor(() => expect(mocks.get).toHaveBeenCalledTimes(2))

      switchContext(change, controller)
      waiting.resolve(response({ mode: 'local' }))

      await expect(pending).resolves.toBeNull()
      expect(mocks.get).toHaveBeenCalledTimes(2)
      expect(mocks.create).toHaveBeenCalledTimes(1)
      expect(mocks.capture).toHaveBeenCalledTimes(1)
    },
  )

  it.each(['session', 'selection', 'caller'])(
    'stops before stored mode when %s changes during stored health await',
    async (change) => {
      mocks.stored = 'https://remote.example/api/v1'
      const controller = new AbortController()
      const waiting = deferred<ReturnType<typeof response>>()
      mocks.get
        .mockResolvedValueOnce(response(health))
        .mockResolvedValueOnce(response({ mode: 'server' }))
        .mockReturnValueOnce(waiting.promise)
      const pending = resolveBootstrapService(controller.signal)
      await vi.waitFor(() => expect(mocks.get).toHaveBeenCalledTimes(3))

      switchContext(change, controller)
      waiting.resolve(response(health))

      await expect(pending).resolves.toBeNull()
      expect(mocks.get).toHaveBeenCalledTimes(3)
      expect(mocks.create).toHaveBeenCalledTimes(2)
      expect(mocks.capture).toHaveBeenCalledTimes(1)
    },
  )

  it.each(['session', 'selection', 'caller'])(
    'rejects the stored result when %s changes during stored mode await',
    async (change) => {
      mocks.stored = 'https://remote.example/api/v1'
      const controller = new AbortController()
      const waiting = deferred<ReturnType<typeof response>>()
      mocks.get
        .mockResolvedValueOnce(response(health))
        .mockResolvedValueOnce(response({ mode: 'server' }))
        .mockResolvedValueOnce(response(health))
        .mockReturnValueOnce(waiting.promise)
      const pending = resolveBootstrapService(controller.signal)
      await vi.waitFor(() => expect(mocks.get).toHaveBeenCalledTimes(4))

      switchContext(change, controller)
      waiting.resolve(response({ mode: 'server' }))

      await expect(pending).resolves.toBeNull()
      expect(mocks.create).toHaveBeenCalledTimes(2)
      expect(mocks.capture).toHaveBeenCalledTimes(1)
    },
  )

  it('continues to the stored service after an ordinary default network failure', async () => {
    mocks.stored = 'https://remote.example/api/v1'
    mocks.get
      .mockRejectedValueOnce(new Error('Unavailable'))
      .mockResolvedValueOnce(response(health))
      .mockResolvedValueOnce(response({ mode: 'server' }))
    await expect(resolveBootstrapService()).resolves.toMatchObject({
      baseUrl: mocks.stored,
      mode: 'server',
    })
    expect(mocks.capture).toHaveBeenCalledTimes(1)
  })

  it('returns null when client creation rejects an invalid service URL', async () => {
    mocks.create.mockImplementationOnce(() => {
      throw new TypeError('Invalid URL')
    })
    await expect(probeService('https://[')).resolves.toBeNull()
    expect(mocks.get).not.toHaveBeenCalled()
  })

  it('does not create a client when the original session signal is already aborted', async () => {
    const suspended = new AbortController()
    suspended.abort()
    mocks.context = { signal: suspended.signal }
    await expect(resolveBootstrapService()).resolves.toBeNull()
    await expect(probeService('/api/v1')).resolves.toBeNull()
    expect(mocks.create).not.toHaveBeenCalled()
    expect(mocks.get).not.toHaveBeenCalled()
  })

  it('returns null when the combined ten-second probe timeout aborts a pending request', async () => {
    const timeout = new AbortController()
    const timeoutSignal = vi.spyOn(AbortSignal, 'timeout').mockReturnValue(timeout.signal)
    try {
      mocks.get.mockImplementationOnce(
        (_path: string, options: { signal: AbortSignal }) =>
          new Promise((_, reject) => {
            options.signal.addEventListener('abort', () =>
              reject(new DOMException('Aborted', 'AbortError')),
            )
          }),
      )
      const pending = probeService('/api/v1')
      expect(timeoutSignal).toHaveBeenCalledWith(10_000)
      timeout.abort()
      await expect(pending).resolves.toBeNull()
    } finally {
      timeoutSignal.mockRestore()
    }
  })
})

describe('service store recovery and current service', () => {
  it.each(['session', 'selection'])(
    'does not commit a connect result after the %s changes',
    async (change) => {
      mocks.stored = 'https://current.example/api/v1'
      const store = useServiceStore()
      const waiting = deferred<ReturnType<typeof response>>()
      mocks.get.mockResolvedValueOnce(response(health)).mockReturnValueOnce(waiting.promise)
      const pending = store.connect('https://target.example/api/v1')
      await vi.waitFor(() => expect(mocks.get).toHaveBeenCalledTimes(2))

      switchContext(change, new AbortController())
      if (change === 'selection') store.baseUrl = mocks.stored!
      store.mode = 'server'
      waiting.resolve(response({ mode: 'local' }))

      await expect(pending).rejects.toThrow()
      expect(store.baseUrl).toBe(mocks.stored)
      expect(store.mode).toBe('server')
      expect(mocks.setBaseUrl).not.toHaveBeenCalled()
      expect(mocks.setLocalMode).not.toHaveBeenCalled()
    },
  )

  it('keeps the selected service on connect failure and permits a successful retry', async () => {
    mocks.stored = 'https://current.example/api/v1'
    const store = useServiceStore()
    store.mode = 'server'
    mocks.get.mockRejectedValueOnce(new Error('Unavailable'))

    await expect(store.connect('https://new.example/api/v1')).rejects.toThrow()
    expect(store.baseUrl).toBe('https://current.example/api/v1')
    expect(store.mode).toBe('server')
    expect(mocks.setBaseUrl).not.toHaveBeenCalled()

    mocks.get
      .mockResolvedValueOnce(response(health))
      .mockResolvedValueOnce(response({ mode: 'local' }))
    await expect(store.connect(' https://new.example/api/v1 ')).resolves.toMatchObject({
      mode: 'local',
    })
    expect(store.baseUrl).toBe('https://new.example/api/v1')
    expect(store.serverName).toBe('LinguaFlow')
    expect(store.hasSelected).toBe(true)
    expect(mocks.setLocalMode).toHaveBeenLastCalledWith(true)
  })

  it('refreshes the current mode without requiring a health probe and recovers after failure', async () => {
    mocks.stored = 'https://current.example/api/v1'
    const store = useServiceStore()
    store.mode = 'server'
    mocks.fetchMode.mockRejectedValueOnce(new Error('Unavailable'))
    await expect(store.refreshMode()).resolves.toBeNull()
    expect(store.mode).toBeNull()
    expect(store.baseUrl).toBe('https://current.example/api/v1')

    mocks.fetchMode.mockResolvedValueOnce({ mode: 'local' })
    await expect(store.refreshMode()).resolves.toBe('local')
    expect(store.mode).toBe('local')
    expect(mocks.setLocalMode).toHaveBeenLastCalledWith(true)
    expect(mocks.create).not.toHaveBeenCalled()
    expect(mocks.ping).not.toHaveBeenCalled()
  })

  it.each(['result', 'failure'])(
    'does not overwrite the new service mode after a stale refresh %s',
    async (outcome) => {
      mocks.stored = 'https://current.example/api/v1'
      const store = useServiceStore()
      const waiting = deferred<{ mode: 'local' }>()
      mocks.fetchMode.mockReturnValueOnce(
        waiting.promise.then((value) => {
          if (outcome === 'failure') throw new Error('Old service unavailable')
          return value
        }),
      )
      const pending = store.refreshMode()
      mocks.stored = 'https://new.example/api/v1'
      store.baseUrl = mocks.stored
      store.mode = 'server'
      waiting.resolve({ mode: 'local' })

      await expect(pending).resolves.toBeNull()
      expect(store.mode).toBe('server')
      expect(mocks.setLocalMode).not.toHaveBeenCalled()
    },
  )
})
