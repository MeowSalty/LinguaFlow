import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { changeSessionContext } from '@/api/session-context'
import { ApiError } from '@/api/utils'
import { useGlobalJobTrackerStore } from '@/stores/globalJobTracker'

const mocks = vi.hoisted(() => ({
  listJobEvents: vi.fn(),
  queryTranslation: vi.fn(),
  subscribeTask: vi.fn(),
  forget: vi.fn(),
  invalidate: vi.fn(async () => {}),
  start: vi.fn(),
  refresh: vi.fn(),
}))
vi.mock('@/i18n', () => ({ t: (key: string) => key }))
vi.mock('@/api/client', () => ({ listJobEvents: mocks.listJobEvents }))
vi.mock('@/stores/operations', () => ({
  useOperationsStore: () => ({ ...mocks, initialized: true, allDiscovered: [] }),
}))
vi.mock('@/composables/sseShared', () => ({
  KNOWN_EVENT_TYPES: ['batch'],
  resolveStreamUrl: () => '/stream',
}))

type Subscription = {
  receive: (value: unknown) => void
  fail: (error: unknown) => void
  release: ReturnType<typeof vi.fn>
}
const subscriptions: Subscription[] = []
class TestEventSource {
  static instances: TestEventSource[] = []
  close = vi.fn()
  addEventListener = vi.fn()
  onopen: (() => void) | null = null
  onerror: (() => void) | null = null
  constructor() {
    TestEventSource.instances.push(this)
  }
}
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((yes, no) => {
    resolve = yes
    reject = no
  })
  return { promise, resolve, reject }
}
const job = (id = 12, project = 7) => ({ id, project_id: project, status: 'running' })
const event = (id = 12, seq = 5) => ({
  job_id: id,
  seq,
  type: 'batch',
  level: 'info',
  message: 'history',
  created_at: '2026-09-30T00:00:00Z',
})
let tracker: ReturnType<typeof useGlobalJobTrackerStore>

beforeEach(() => {
  changeSessionContext('https://instance.test/api/v1', 1, true)
  setActivePinia(createPinia())
  subscriptions.length = 0
  TestEventSource.instances = []
  for (const mock of Object.values(mocks)) mock.mockReset()
  mocks.invalidate.mockResolvedValue(undefined)
  mocks.listJobEvents.mockResolvedValue({ items: [event()], next_before_seq: 5 })
  mocks.subscribeTask.mockImplementation((_locator, receive, fail) => {
    const release = vi.fn()
    subscriptions.push({ receive, fail, release })
    return release
  })
  vi.stubGlobal('document', {
    hidden: false,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
  })
  vi.stubGlobal('EventSource', TestEventSource)
  tracker = useGlobalJobTrackerStore()
})
afterEach(() => {
  tracker.$dispose()
  vi.unstubAllGlobals()
})

describe('translation detail authorization and response ordering', () => {
  it('checks the link project before accepting detail, starting SSE, or requesting events', async () => {
    const opening = tracker.openDetail(12, 7)
    expect(mocks.listJobEvents).not.toHaveBeenCalled()
    subscriptions[0]!.receive(job(12, 8))
    await opening
    expect(tracker.detailJob).toBeNull()
    expect(tracker.detailError).toBe('operations.inaccessible')
    expect(mocks.listJobEvents).not.toHaveBeenCalled()
    expect(TestEventSource.instances).toHaveLength(0)
    expect(subscriptions[0]!.release).toHaveBeenCalledOnce()
    expect(mocks.forget).not.toHaveBeenCalled()
  })

  it('awaits validated detail instead of resolving openDetail before the detail response', async () => {
    const opening = tracker.openDetail(12, 7)
    let finished = false
    void opening.then(() => {
      finished = true
    })
    await Promise.resolve()
    expect(finished).toBe(false)
    subscriptions[0]!.receive(job())
    await opening
    expect(tracker.detailJob?.project_id).toBe(7)
    expect(tracker.getJobEvents()).toEqual([event()])
  })

  it('history denial invalidates simultaneous successful detail reads and stops the stream', async () => {
    const history = deferred<{ items: ReturnType<typeof event>[] }>()
    const detail = deferred<ReturnType<typeof job>>()
    mocks.listJobEvents.mockReturnValueOnce(history.promise)
    mocks.queryTranslation.mockReturnValueOnce(detail.promise)
    const opening = tracker.openDetail(12, 7)
    subscriptions[0]!.receive(job())
    const refreshing = tracker.refreshDetail()
    history.reject(new ApiError('revoked', 403))
    await opening
    detail.resolve(job())
    await refreshing
    subscriptions[0]!.receive(job())
    expect(tracker.detailJob).toBeNull()
    expect(tracker.getJobEvents()).toEqual([])
    expect(tracker.loadingDetail).toBe(false)
    expect(tracker.loadingOlder).toBe(false)
    expect(tracker.detailError).toBe('operations.inaccessible')
    expect(subscriptions[0]!.release).toHaveBeenCalledOnce()
    expect(TestEventSource.instances[0]!.close).toHaveBeenCalledOnce()
    expect(mocks.forget).toHaveBeenCalledWith({ task_type: 'translation', task_id: '12' })
  })

  it('older-history 404 resolves cleanly and clears the entire drawer', async () => {
    const opening = tracker.openDetail(12)
    subscriptions[0]!.receive(job())
    await opening
    mocks.listJobEvents.mockRejectedValueOnce(new ApiError('deleted', 404))
    await expect(tracker.loadOlder()).resolves.toBeUndefined()
    expect(tracker.detailJob).toBeNull()
    expect(tracker.getJobEvents()).toEqual([])
    expect(tracker.hasOlder).toBe(false)
    expect(tracker.loadingOlder).toBe(false)
    expect(subscriptions[0]!.release).toHaveBeenCalledOnce()
  })

  it('retains data and displays a temporary history error until a successful history retry', async () => {
    const opening = tracker.openDetail(12)
    subscriptions[0]!.receive(job())
    await opening
    mocks.listJobEvents.mockRejectedValueOnce(new ApiError('temporary history failure', 503))
    await expect(tracker.loadOlder()).resolves.toBeUndefined()
    subscriptions[0]!.receive(job())
    expect(tracker.detailJob?.id).toBe(12)
    expect(tracker.getJobEvents()).toEqual([event()])
    expect(tracker.detailError).toBe('temporary history failure')
    expect(tracker.loadingOlder).toBe(false)
    mocks.listJobEvents.mockResolvedValueOnce({ items: [event(12, 4)] })
    await tracker.loadOlder()
    expect(tracker.detailError).toBeNull()
    expect(tracker.getJobEvents().map((item) => item.seq)).toEqual([4, 5])
  })

  it('closing a pending drawer settles its opening and rejects callbacks from the old subscription', async () => {
    const opening = tracker.openDetail(12)
    tracker.closeDetail()
    await opening
    subscriptions[0]!.receive(job())
    subscriptions[0]!.fail(new ApiError('old error', 403))
    expect(tracker.drawerJobId).toBeNull()
    expect(tracker.detailError).toBeNull()
    expect(tracker.loadingDetail).toBe(false)
    expect(mocks.listJobEvents).not.toHaveBeenCalled()
  })

  it('does not allow delayed history from the previous drawer to replace the current job events', async () => {
    const oldHistory = deferred<{ items: ReturnType<typeof event>[] }>()
    mocks.listJobEvents
      .mockReturnValueOnce(oldHistory.promise)
      .mockResolvedValueOnce({ items: [event(13)] })
    const first = tracker.openDetail(12)
    subscriptions[0]!.receive(job())
    const second = tracker.openDetail(13)
    subscriptions[1]!.receive(job(13))
    await second
    oldHistory.resolve({ items: [event(12)] })
    await first
    expect(tracker.detailJob?.id).toBe(13)
    expect(tracker.getJobEvents()).toEqual([event(13)])
    expect(TestEventSource.instances[0]!.close).toHaveBeenCalledOnce()
  })

  it('aborts latest history when its drawer closes and uses a fresh signal on reopening', async () => {
    const pending = deferred<{ items: ReturnType<typeof event>[] }>()
    mocks.listJobEvents.mockReturnValueOnce(pending.promise)
    const opening = tracker.openDetail(12)
    subscriptions[0]!.receive(job())
    const oldSignal = mocks.listJobEvents.mock.calls[0]![1].signal as AbortSignal
    tracker.closeDetail()
    expect(oldSignal.aborted).toBe(true)
    const next = tracker.openDetail(13)
    subscriptions[1]!.receive(job(13))
    await next
    const newSignal = mocks.listJobEvents.mock.calls[1]![1].signal as AbortSignal
    expect(newSignal.aborted).toBe(false)
    pending.resolve({ items: [event()] })
    await opening
    expect(tracker.detailJob?.id).toBe(13)
  })

  it('aborts older history and explicit detail refresh together on close', async () => {
    const opening = tracker.openDetail(12)
    subscriptions[0]!.receive(job())
    await opening
    const history = deferred<{ items: ReturnType<typeof event>[] }>()
    const detail = deferred<ReturnType<typeof job>>()
    mocks.listJobEvents.mockReturnValueOnce(history.promise)
    mocks.queryTranslation.mockReturnValueOnce(detail.promise)
    const older = tracker.loadOlder(),
      refreshing = tracker.refreshDetail()
    const historySignal = mocks.listJobEvents.mock.calls[1]![1].signal as AbortSignal
    const detailSignal = mocks.queryTranslation.mock.calls[0]![1] as AbortSignal
    tracker.closeDetail()
    expect(historySignal.aborted).toBe(true)
    expect(detailSignal.aborted).toBe(true)
    history.resolve({ items: [event()] })
    detail.resolve(job())
    await Promise.all([older, refreshing])
    expect(tracker.detailJob).toBeNull()
    expect(tracker.getJobEvents()).toEqual([])
  })
})
