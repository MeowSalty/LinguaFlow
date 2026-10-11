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
  projectTask: vi.fn(),
  projectJobObservation: vi.fn((job) => job),
}))
vi.mock('@/i18n', () => ({ t: (key: string) => key }))
vi.mock('@/api/client', () => ({ listJobEvents: mocks.listJobEvents }))
vi.mock('@/stores/operations', () => ({
  useOperationsStore: () => ({ ...mocks, initialized: true, allDiscovered: [] }),
}))
vi.mock('@/composables/sseShared', () => ({
  KNOWN_EVENT_TYPES: ['batch', 'stage_counts', 'job_pausing', 'job_paused', 'job_cancelled'],
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
  listeners = new Map<string, (event: { data: string }) => void>()
  addEventListener = vi.fn((type: string, receive: (event: { data: string }) => void) => {
    this.listeners.set(type, receive)
  })
  emit(value: ReturnType<typeof event> & { metadata?: Record<string, unknown> }) {
    this.listeners.get(value.type)?.({ data: JSON.stringify(value) })
  }
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
const job = (id = 12, project = 7) => ({
  id,
  project_id: project,
  status: 'running',
  can_delete: false,
  finished_at: null,
})
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
  mocks.projectJobObservation.mockImplementation((job) => job)
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
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

const stageCounts = (as_of = '2026-10-09T11:00:00.123456789Z') => ({
  main_requests: 1,
  pending_alignment: 2,
  alignment_requests: 1,
  saving_requests: 0,
  ready_to_commit: 0,
  confirmed_work: 3,
  unknown_requests: 0,
  draining_requests: 2,
  as_of,
})
const observedJob = () => ({
  ...job(),
  updated_at: '2026-10-09T11:00:00Z',
  progress: { progress_completed: 10, progress_total: 20, stages: stageCounts() },
})
const stageEvent = (stages: unknown = stageCounts(), id = 12, seq = 6) => ({
  ...event(id, seq),
  type: 'stage_counts',
  metadata: { stages },
})

describe('live job stage observations', () => {
  it('revalidates through the coordinator after SSE reconnect and ignores obsolete streams', async () => {
    const opening = tracker.openDetail(12)
    subscriptions[0]!.receive({ ...observedJob(), status: 'pausing' })
    await opening
    const first = TestEventSource.instances[0]!
    first.onopen?.()
    expect(mocks.refresh).not.toHaveBeenCalled()
    first.onerror?.()
    expect(tracker.isJobSSEConnected()).toBe(false)
    subscriptions[0]!.receive({ ...observedJob(), status: 'pausing' })
    const second = TestEventSource.instances[1]!
    second.onopen?.()
    expect(mocks.refresh).toHaveBeenCalledTimes(1)
    expect(tracker.isJobSSEConnected()).toBe(true)
    first.onopen?.()
    first.onerror?.()
    expect(mocks.refresh).toHaveBeenCalledTimes(1)
    expect(tracker.isJobSSEConnected()).toBe(true)
    tracker.closeDetail()
    second.onopen?.()
    expect(mocks.refresh).toHaveBeenCalledTimes(1)
  })
  it('publishes actual cancellation before old pause callbacks and preserves newer retry responses', async () => {
    const opening = tracker.openDetail(12)
    subscriptions[0]!.receive({ ...observedJob(), status: 'pausing' })
    await opening
    const source = TestEventSource.instances[0]!
    const cancelled = {
      ...observedJob(),
      status: 'cancelled' as const,
      updated_at: '2026-10-09T11:00:00.123456790Z',
    }
    tracker.trackJob(cancelled as Parameters<typeof tracker.trackJob>[0])
    expect(mocks.projectTask).toHaveBeenCalledWith(
      { task_type: 'translation', task_id: '12', project_id: 7 },
      cancelled,
    )
    subscriptions[0]!.receive({
      ...observedJob(),
      status: 'paused',
      updated_at: '2026-10-09T11:00:00.123456789Z',
    })
    source.emit({ ...event(12, 10), type: 'job_paused' })
    expect(tracker.detailJob?.status).toBe('cancelled')
    expect(tracker.isJobSSEConnected()).toBe(false)
    subscriptions[0]!.receive({
      ...observedJob(),
      status: 'pending',
      updated_at: '2026-10-09T11:00:00.123456791Z',
    })
    expect(tracker.detailJob?.status).toBe('pending')
    expect(TestEventSource.instances).toHaveLength(2)
  })
  it('rejects malformed REST and live event envelopes before log rendering', async () => {
    mocks.listJobEvents.mockResolvedValueOnce({
      items: [{ job_id: 12, seq: 1, type: 'job_paused' }, event()],
    })
    const opening = tracker.openDetail(12)
    subscriptions[0]!.receive(observedJob())
    await opening
    const source = TestEventSource.instances[0]!
    source.listeners.get('job_paused')?.({
      data: JSON.stringify({ job_id: 12, seq: 9, type: 'job_paused' }),
    })
    source.emit({ ...event(12, 10), created_at: 'invalid', type: 'job_paused' })
    expect(tracker.getJobEvents()).toEqual([event()])
    expect(mocks.queryTranslation).not.toHaveBeenCalled()
  })

  it('clearing logs invalidates in-flight history without interrupting live stage updates', async () => {
    const opening = tracker.openDetail(12)
    subscriptions[0]!.receive(observedJob())
    await opening
    const history = deferred<{ items: ReturnType<typeof event>[] }>()
    mocks.listJobEvents.mockReturnValueOnce(history.promise)
    const older = tracker.loadOlder()
    tracker.clearJobEvents()
    const next = stageCounts('2026-10-09T11:00:01Z')
    TestEventSource.instances[0]!.emit(stageEvent(next))
    history.resolve({ items: [event(12, 4)] })
    await older
    expect(tracker.getJobEvents().map((item) => item.seq)).toEqual([6])
    expect(tracker.detailJob?.progress.stages).toEqual(next)
    expect(tracker.loadingOlder).toBe(false)
  })

  it('merges only newer live observations and leaves history and effective progress independent', async () => {
    const opening = tracker.openDetail(12)
    subscriptions[0]!.receive(observedJob())
    await opening
    const source = TestEventSource.instances[0]!
    const newer = { ...stageCounts('2026-10-09T11:00:00.123456790Z'), alignment_requests: 0 }
    source.emit(stageEvent(newer))
    source.emit(stageEvent(stageCounts(), 12, 7))
    source.emit(stageEvent({ main_requests: 90 }, 12, 8))
    source.emit(stageEvent(stageCounts('2026-10-09T12:00:00Z'), 13, 9))
    subscriptions[0]!.receive(observedJob())
    expect(tracker.detailJob?.progress.stages).toEqual(newer)
    expect(tracker.detailJob?.progress.progress_completed).toBe(10)
    expect(tracker.detailJob?.status).toBe('running')
    mocks.listJobEvents.mockResolvedValueOnce({
      items: [stageEvent(stageCounts('2026-10-10T00:00:00Z'), 12, 4)],
    })
    await tracker.loadOlder()
    expect(tracker.detailJob?.progress.stages).toEqual(newer)
    expect(tracker.getJobEvents().map((item) => item.seq)).toEqual([4, 5, 6, 7, 8])
  })

  it('coalesces lifecycle events into a server refresh and never infers paused from zero draining', async () => {
    vi.useFakeTimers()
    const opening = tracker.openDetail(12)
    subscriptions[0]!.receive({ ...observedJob(), status: 'pausing' })
    await opening
    const source = TestEventSource.instances[0]!
    source.emit(stageEvent({ ...stageCounts('2026-10-09T11:00:01Z'), draining_requests: 0 }))
    expect(tracker.detailJob?.status).toBe('pausing')
    mocks.queryTranslation.mockResolvedValue({
      ...observedJob(),
      status: 'paused',
      updated_at: '2026-10-09T11:00:02Z',
    })
    source.emit({ ...event(12, 7), type: 'job_pausing' })
    source.emit({ ...event(12, 8), type: 'job_paused' })
    expect(mocks.queryTranslation).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(100)
    expect(mocks.queryTranslation).toHaveBeenCalledTimes(1)
    expect(tracker.detailJob?.status).toBe('paused')
  })

  it('drops closed streams, pending lifecycle refreshes and observations from a different session', async () => {
    vi.useFakeTimers()
    const opening = tracker.openDetail(12)
    subscriptions[0]!.receive(observedJob())
    await opening
    const source = TestEventSource.instances[0]!
    source.emit({ ...event(12, 7), type: 'job_pausing' })
    tracker.closeDetail()
    await vi.advanceTimersByTimeAsync(100)
    expect(mocks.queryTranslation).not.toHaveBeenCalled()
    const next = tracker.openDetail(13)
    subscriptions[1]!.receive({
      ...job(13),
      progress: { progress_completed: 0, progress_total: 3 },
    })
    await next
    source.emit(stageEvent(stageCounts('2026-10-09T12:00:00Z')))
    expect(tracker.detailJob?.progress.stages).toBeUndefined()
    changeSessionContext('https://another.test/api/v1', 2, true)
    TestEventSource.instances[1]!.emit(stageEvent(stageCounts(), 13))
    expect(tracker.detailJob?.progress.stages).toBeUndefined()
  })
})

describe('translation detail authorization and response ordering', () => {
  for (const read of ['latest', 'older'] as const) {
    it.each([403, 404])(
      `clearing logs cannot suppress ${read} history denial (%i)`,
      async (status) => {
        const opening = tracker.openDetail(12)
        subscriptions[0]!.receive(observedJob())
        await opening
        const history = deferred<{ items: ReturnType<typeof event>[] }>()
        mocks.listJobEvents.mockReturnValueOnce(history.promise)
        const pending = read === 'latest' ? tracker.loadLatestPage(12) : tracker.loadOlder()
        const source = TestEventSource.instances[0]!
        tracker.clearJobEvents()
        history.reject(new ApiError('inaccessible', status))
        await pending
        source.emit(stageEvent(stageCounts('2026-10-09T12:00:00Z')))
        subscriptions[0]!.receive(observedJob())
        expect(tracker.detailJob).toBeNull()
        expect(tracker.getJobEvents()).toEqual([])
        expect(tracker.detailError).toBe(
          status === 404 ? 'taskHistoryErrors.notFound' : 'operations.inaccessible',
        )
        expect(source.close).toHaveBeenCalledOnce()
        expect(subscriptions[0]!.release).toHaveBeenCalledOnce()
      },
    )

    it(`clearing logs discards temporary ${read} history errors without stopping stages`, async () => {
      const opening = tracker.openDetail(12)
      subscriptions[0]!.receive(observedJob())
      await opening
      const history = deferred<{ items: ReturnType<typeof event>[] }>()
      mocks.listJobEvents.mockReturnValueOnce(history.promise)
      const pending = read === 'latest' ? tracker.loadLatestPage(12) : tracker.loadOlder()
      tracker.clearJobEvents()
      history.reject(new ApiError('temporary', 503))
      await pending
      const next = stageCounts('2026-10-09T12:00:00Z')
      TestEventSource.instances[0]!.emit(stageEvent(next))
      expect(tracker.detailError).toBeNull()
      expect(tracker.detailJob?.progress.stages).toEqual(next)
    })
  }

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
    mocks.listJobEvents.mockRejectedValueOnce(
      new ApiError('temporary history failure', 503, { title: 'history_unavailable' }),
    )
    await expect(tracker.loadOlder()).resolves.toBeUndefined()
    subscriptions[0]!.receive(job())
    expect(tracker.detailJob?.id).toBe(12)
    expect(tracker.getJobEvents()).toEqual([event()])
    expect(tracker.detailError).toBe('taskHistoryErrors.historyUnavailable')
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
