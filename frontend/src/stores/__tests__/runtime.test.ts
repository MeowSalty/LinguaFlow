import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, disposePinia, setActivePinia, type Pinia } from 'pinia'
import { reactive } from 'vue'
import { useRuntimeStore, RUNTIME_POLL_INTERVAL } from '../runtime'
import { ApiError } from '@/api/utils'
import { changeSessionContext } from '@/api/session-context'
import type { RuntimeSummary } from '@/api/runtime'

const mocks = vi.hoisted(() => ({
  fetch: vi.fn(),
  auth: { isAuthenticated: true, user: { role: 'admin' } },
}))
vi.mock('@/api/runtime', () => ({ fetchRuntimeSummary: mocks.fetch }))
vi.mock('@/stores/auth', () => ({ useAuthStore: () => mocks.auth }))
vi.mock('@/i18n', () => ({ t: (key: string) => key }))

const sample = (id = 'instance-a', count = 25): RuntimeSummary => ({
  instance_id: id,
  started_at: '2026-09-30T00:00:00Z',
  as_of: '2026-09-30T00:01:00.123456789Z',
  uptime_seconds: 60,
  scope: 'instance',
  runners: [
    {
      task_type: 'translation',
      state: 'running',
      recovered_total: 2,
      recovery_errors_total: 0,
      queue_capacity: null,
      queue_waiting: 0,
      enqueue_waiters: 0,
      worker_capacity: 4,
      workers_alive: 4,
      workers_busy: 1,
    },
  ],
  limiters: null,
  external_requests: [
    {
      provider: 'openai',
      operation: 'generate',
      http_attempts_inflight: 1,
      http_attempts_total: count,
      outcomes: [],
    },
  ],
})

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}

describe('runtime sampling lifecycle', () => {
  let pinia: Pinia
  let fakeDocument: EventTarget & { visibilityState: string }
  beforeEach(() => {
    vi.useFakeTimers()
    fakeDocument = Object.assign(new EventTarget(), { visibilityState: 'visible' })
    vi.stubGlobal('document', fakeDocument)
    mocks.auth = reactive({ isAuthenticated: true, user: { role: 'admin' } })
    mocks.fetch.mockReset().mockResolvedValue(sample())
    changeSessionContext('/api/v1', 1, true)
    pinia = createPinia()
    setActivePinia(pinia)
  })
  afterEach(() => {
    disposePinia(pinia)
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('merges in-flight samples and schedules five seconds after completion', async () => {
    const pending = deferred<RuntimeSummary>()
    mocks.fetch.mockReturnValueOnce(pending.promise)
    const store = useRuntimeStore()
    const release = store.subscribe()
    const first = store.refresh()
    void store.refresh()
    await vi.advanceTimersByTimeAsync(20_000)
    expect(mocks.fetch).toHaveBeenCalledTimes(1)
    pending.resolve(sample())
    await first
    await vi.advanceTimersByTimeAsync(RUNTIME_POLL_INTERVAL - 1)
    expect(mocks.fetch).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(mocks.fetch).toHaveBeenCalledTimes(2)
    release()
    await vi.advanceTimersByTimeAsync(20_000)
    expect(mocks.fetch).toHaveBeenCalledTimes(2)
  })

  it('stops while hidden, aborts its sample, and refreshes immediately on return', async () => {
    const store = useRuntimeStore()
    store.subscribe()
    await store.refresh()
    const pending = deferred<RuntimeSummary>()
    mocks.fetch.mockReturnValueOnce(pending.promise)
    void store.refresh()
    const signal = mocks.fetch.mock.calls[1]?.[0] as AbortSignal
    fakeDocument.visibilityState = 'hidden'
    fakeDocument.dispatchEvent(new Event('visibilitychange'))
    expect(signal.aborted).toBe(true)
    pending.resolve(sample('late-hidden'))
    await vi.advanceTimersByTimeAsync(30_000)
    expect(mocks.fetch).toHaveBeenCalledTimes(2)
    expect(store.snapshot?.instance_id).toBe('instance-a')
    fakeDocument.visibilityState = 'visible'
    fakeDocument.dispatchEvent(new Event('visibilitychange'))
    await store.refresh()
    expect(mocks.fetch).toHaveBeenCalledTimes(3)
  })

  it('clears and stops on 403 until a new identity is established', async () => {
    const store = useRuntimeStore()
    store.subscribe()
    await store.refresh()
    mocks.fetch.mockRejectedValueOnce(new ApiError('revoked', 403))
    await store.refresh()
    expect(store.snapshot).toBeNull()
    expect(store.forbidden).toBe(true)
    await vi.advanceTimersByTimeAsync(30_000)
    await store.refresh()
    expect(mocks.fetch).toHaveBeenCalledTimes(2)
  })

  it('preserves the last sample and precise collection time on a temporary failure', async () => {
    const store = useRuntimeStore()
    store.subscribe()
    await store.refresh()
    mocks.fetch.mockRejectedValueOnce(new ApiError('temporarily unavailable', 503))
    await store.refresh()
    expect(store.stale).toBe(true)
    expect(store.snapshot?.as_of).toBe('2026-09-30T00:01:00.123456789Z')
    expect(store.snapshot?.limiters).toBeNull()
    expect(store.snapshot?.runners[0]?.queue_capacity).toBeNull()
    await vi.advanceTimersByTimeAsync(5_000)
    expect(store.error).toBeNull()
  })

  it('replaces instance totals instead of combining samples', async () => {
    const store = useRuntimeStore()
    store.subscribe()
    await store.refresh()
    mocks.fetch.mockResolvedValueOnce(sample('instance-b', 2))
    await store.refresh()
    expect(store.snapshot?.instance_id).toBe('instance-b')
    expect(store.snapshot?.external_requests[0]?.http_attempts_total).toBe(2)
  })

  it.each(['success', 'failure'])(
    'rejects a late %s after changing account or service',
    async (result) => {
      const pending = deferred<RuntimeSummary>()
      mocks.fetch.mockReturnValueOnce(pending.promise)
      const store = useRuntimeStore()
      store.subscribe()
      const request = store.refresh()
      changeSessionContext('https://new.example/api/v1', 2)
      if (result === 'success') pending.resolve(sample('previous-account'))
      else pending.reject(new Error('previous-account error'))
      await request
      expect(store.snapshot).toBeNull()
      expect(store.error).toBeNull()
      expect(store.loading).toBe(false)
    },
  )

  it('clears a sample immediately after role downgrade and refuses nonadmins', async () => {
    const store = useRuntimeStore()
    store.subscribe()
    await store.refresh()
    mocks.auth.user.role = 'user'
    expect(store.snapshot).toBeNull()
    await vi.advanceTimersByTimeAsync(30_000)
    await store.refresh()
    expect(mocks.fetch).toHaveBeenCalledTimes(1)
  })
})
