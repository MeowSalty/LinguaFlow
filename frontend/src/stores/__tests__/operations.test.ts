import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, disposePinia, setActivePinia, type Pinia } from 'pinia'
import { useOperationsStore } from '../operations'
import { ApiError } from '@/api/utils'
import { changeSessionContext } from '@/api/session-context'
import type { Operation } from '@/api/operations'

const api = vi.hoisted(() => ({
  list: vi.fn(),
  summary: vi.fn(),
  job: vi.fn(),
  sync: vi.fn(),
  storage: vi.fn(),
}))
vi.mock('@/api/storage', () => ({ getStorageTask: api.storage }))
vi.mock('@/api/operations', () => ({
  listOperations: api.list,
  fetchOperationsSummary: api.summary,
}))
vi.mock('@/api/client', () => ({ fetchJob: api.job, getGlossarySyncTaskStatus: api.sync }))
vi.mock('@/i18n', () => ({ t: (key: string) => key }))

const task = (
  id: string,
  type: Operation['task_type'] = 'translation',
  status: Operation['status'] = 'running',
): Operation =>
  ({
    task_id: id,
    task_type: type,
    project_id: 7,
    project_name: 'Test project',
    status,
    can_delete: type !== 'storage' && ['completed', 'failed', 'cancelled'].includes(status),
    finished_at: null,
    created_at: '2026-09-30T00:00:00Z',
    updated_at: '2026-09-30T00:01:00Z',
    started_at: null,
    supported_actions: ['view', 'cancel'],
    ...(type === 'translation'
      ? {
          trigger_type: 'manual',
          progress: {
            total_resources: 2,
            completed_resources: 1,
            failed_resources: 0,
            progress_total: 100,
            progress_completed: 50,
            queue_position: null,
            queue_size: null,
          },
        }
      : type === 'glossary_sync'
        ? { progress: { processed_segments: 50, total_segments: 100 } }
        : {
            storage_kind: 'source_update',
            phase: 'prepared',
            cleanup_status: 'cleanup_pending',
            error_code: '',
            next_retry_at: null,
          }),
  }) as Operation
const counts = {
  pending: 0,
  running: 1,
  paused: 0,
  waiting_retry: 0,
  needs_action: 0,
  recent_failed: 0,
}
const summary = {
  total: counts,
  by_type: { translation: counts, glossary_sync: counts, storage: counts },
  recent_failed_since: '2026-09-29T00:00:00.123456789Z',
  as_of: '2026-09-30T00:00:00Z',
}
function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((r) => {
    resolve = r
  })
  return { resolve, promise }
}

describe('operations coordinator', () => {
  let pinia: Pinia
  let fakeDocument: EventTarget & { hidden: boolean }
  beforeEach(() => {
    vi.useFakeTimers()
    fakeDocument = Object.assign(new EventTarget(), { hidden: false })
    vi.stubGlobal('document', fakeDocument)
    changeSessionContext('/api/v1', 1, true)
    pinia = createPinia()
    setActivePinia(pinia)
    api.list.mockReset().mockResolvedValue({ items: [] })
    api.summary.mockReset().mockResolvedValue(summary)
    api.job.mockReset().mockResolvedValue({ status: 'completed', project_id: 7 })
    api.sync.mockReset().mockResolvedValue({ status: 'completed' })
    api.storage.mockReset().mockResolvedValue({
      id: 1,
      status: 'completed',
      cleanup_status: 'done',
      allowed_actions: [],
    })
  })
  afterEach(() => {
    disposePinia(pinia)
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('visits every active page and deduplicates by task type and string identifier', async () => {
    api.list
      .mockResolvedValueOnce({ items: [task('1'), task('2')], next_cursor: 'page-2' })
      .mockResolvedValueOnce({ items: [task('2'), task('1', 'glossary_sync')] })
    const store = useOperationsStore()
    await store.discover()
    expect(api.list.mock.calls.map(([query]) => query.cursor)).toEqual([undefined, 'page-2'])
    expect(store.active.map((item) => `${item.task_type}:${item.task_id}`)).toEqual([
      'translation:1',
      'translation:2',
      'glossary_sync:1',
    ])
    expect(store.discoveryComplete).toBe(true)
  })
  it('an older project read cannot overwrite a later capability projection from another view', () => {
    const store = useOperationsStore()
    const target = {
      kind: 'translation' as const,
      id: '1',
      project_id: 7,
      status: 'completed',
      can_delete: false,
    }
    const old = store.beginCapabilityRead(),
      fresh = store.beginCapabilityRead()
    store.observeCapabilities([{ ...target, can_delete: true }], fresh)
    store.observeCapabilities([target], old)
    expect(store.getTaskCapability(target)?.can_delete).toBe(true)
    store.observeCapabilities([{ ...target, status: 'running' }])
    expect(store.getTaskCapability(target)).toMatchObject({ can_delete: false, status: 'running' })
  })
  it('removal invalidates earlier reads, closes target subscriptions and resets summary/cursor once', async () => {
    const store = useOperationsStore()
    api.list.mockResolvedValue({
      items: [task('1', 'translation', 'completed')],
      next_cursor: 'old-page',
    })
    await store.loadList()
    await store.ensureSummary()
    const late = deferred<{ status: string; project_id: number }>()
    api.job.mockReturnValueOnce(late.promise)
    const received = vi.fn(),
      failed = vi.fn()
    store.subscribeTask({ task_type: 'translation', task_id: '1' }, received, failed, {
      terminalRecheckMs: 30_000,
    })
    store.invalidateTasks([{ kind: 'translation', id: '1', project_id: 7 }])
    late.resolve({ status: 'completed', project_id: 7 })
    await vi.advanceTimersByTimeAsync(0)
    expect(received).not.toHaveBeenCalled()
    expect(failed).not.toHaveBeenCalled()
    expect(store.items).toEqual([])
    expect(store.nextCursor).toBeUndefined()
    expect(store.summary).toBeNull()
    expect(store.revision).toBe(1)
  })
  it('rechecks visible terminal detail capability every thirty seconds and pauses when hidden', async () => {
    const store = useOperationsStore(),
      receive = vi.fn()
    api.job.mockResolvedValue({
      status: 'completed',
      project_id: 7,
      can_delete: false,
      finished_at: null,
    })
    store.start()
    const release = store.subscribeTask(
      { task_type: 'translation', task_id: '1' },
      receive,
      vi.fn(),
      { terminalRecheckMs: 30_000 },
    )
    await vi.advanceTimersByTimeAsync(29_999)
    expect(receive).toHaveBeenCalledTimes(1)
    api.job.mockResolvedValue({
      status: 'completed',
      project_id: 7,
      can_delete: true,
      finished_at: null,
    })
    await vi.advanceTimersByTimeAsync(1)
    expect(receive).toHaveBeenLastCalledWith(expect.objectContaining({ can_delete: true }))
    expect(receive).toHaveBeenCalledTimes(2)
    fakeDocument.hidden = true
    fakeDocument.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(60_000)
    expect(receive).toHaveBeenCalledTimes(2)
    fakeDocument.hidden = false
    fakeDocument.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(0)
    expect(receive).toHaveBeenCalledTimes(3)
    release()
  })

  it('preserves the previous collection if a later page fails', async () => {
    const store = useOperationsStore()
    api.list.mockResolvedValueOnce({ items: [task('previous')] })
    await store.discover()
    api.list
      .mockResolvedValueOnce({ items: [task('new')], next_cursor: 'second' })
      .mockRejectedValueOnce(new Error('offline'))
    await store.discover()
    expect(store.active.map((item) => item.task_id)).toEqual(['new', 'previous'])
    expect(store.discoveryComplete).toBe(false)
    expect(store.discoveryError).toBe('offline')
    expect(api.job).not.toHaveBeenCalled()
  })

  it('confirms disappearance through detail before recording a terminal task', async () => {
    const store = useOperationsStore()
    api.list.mockResolvedValueOnce({ items: [task('1'), task('2', 'glossary_sync')] })
    await store.discover()
    api.list.mockResolvedValueOnce({ items: [] })
    api.job.mockResolvedValueOnce({ status: 'completed', project_id: 7 })
    api.sync.mockResolvedValueOnce({ status: 'running' })
    await store.discover()
    expect(api.job).toHaveBeenCalledWith(1, undefined, { signal: expect.any(AbortSignal) })
    expect(api.sync).toHaveBeenCalledWith(7, '2', undefined, { signal: expect.any(AbortSignal) })
    expect(store.active.map((item) => item.task_id)).toEqual(['2'])
    expect(store.terminal.map((item) => item.task_id)).toEqual(['1'])
  })

  it('retains disappeared tasks when detail retrieval fails temporarily', async () => {
    const store = useOperationsStore()
    api.list.mockResolvedValueOnce({ items: [task('1')] })
    await store.discover()
    api.list.mockResolvedValueOnce({ items: [] })
    api.job.mockRejectedValueOnce(new Error('offline'))
    await store.discover()
    expect(store.active).toHaveLength(1)
    expect(store.terminal).toHaveLength(0)
    api.list.mockRejectedValueOnce(new Error('offline'))
    await store.discover()
    expect(store.active).toHaveLength(1)
  })

  it('removes a disappeared task only after explicit loss of access', async () => {
    const store = useOperationsStore()
    api.list.mockResolvedValueOnce({ items: [task('1')] })
    await store.discover()
    api.list.mockResolvedValueOnce({ items: [] })
    api.job.mockRejectedValueOnce(new ApiError('revoked', 403))
    await store.discover()
    expect(store.active).toHaveLength(0)
    expect(store.terminal).toHaveLength(0)
  })

  it('merges summaries in flight and caches successful results for ten seconds', async () => {
    const pending = deferred<typeof summary>()
    api.summary.mockReturnValueOnce(pending.promise)
    const store = useOperationsStore()
    const first = store.querySummary()
    const second = store.querySummary()
    expect(api.summary).toHaveBeenCalledTimes(1)
    pending.resolve(summary)
    await Promise.all([first, second])
    await vi.advanceTimersByTimeAsync(9_999)
    await store.querySummary()
    expect(api.summary).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(1)
    await store.querySummary()
    expect(api.summary).toHaveBeenCalledTimes(2)
  })

  it('never overlaps discovery cycles and starts its interval after completion', async () => {
    const pending = deferred<{ items: Operation[] }>()
    api.list.mockImplementation((query) =>
      query.state === 'active' ? pending.promise : Promise.resolve({ items: [] }),
    )
    const store = useOperationsStore()
    store.start()
    await vi.advanceTimersByTimeAsync(20_000)
    expect(api.list.mock.calls.filter(([query]) => query.state === 'active')).toHaveLength(1)
    pending.resolve({ items: [task('1')] })
    await store.refresh()
    await vi.advanceTimersByTimeAsync(2_999)
    expect(api.list.mock.calls.filter(([query]) => query.state === 'active')).toHaveLength(1)
    await vi.advanceTimersByTimeAsync(1)
    expect(api.list.mock.calls.filter(([query]) => query.state === 'active')).toHaveLength(2)
  })

  it('continues discovery while idle and stops its timer while hidden', async () => {
    const store = useOperationsStore()
    store.start()
    await store.refresh()
    await vi.advanceTimersByTimeAsync(10_000)
    expect(api.list.mock.calls.filter(([query]) => query.state === 'active')).toHaveLength(2)
    fakeDocument.hidden = true
    fakeDocument.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(20_000)
    expect(api.list.mock.calls.filter(([query]) => query.state === 'active')).toHaveLength(2)
    fakeDocument.hidden = false
    fakeDocument.dispatchEvent(new Event('visibilitychange'))
    await store.refresh()
    expect(api.list.mock.calls.filter(([query]) => query.state === 'active')).toHaveLength(3)
  })

  it('loads more explicitly and resets pagination on refresh', async () => {
    api.list
      .mockResolvedValueOnce({ items: [task('1')], next_cursor: 'next' })
      .mockResolvedValueOnce({ items: [task('2')] })
      .mockResolvedValueOnce({ items: [task('3')] })
    const store = useOperationsStore()
    await store.loadList()
    await store.loadList(true)
    expect(store.items.map((item) => item.task_id)).toEqual(['1', '2'])
    await store.loadList()
    expect(store.items.map((item) => item.task_id)).toEqual(['3'])
    expect(api.list.mock.calls.map(([query]) => query.cursor)).toEqual([
      undefined,
      'next',
      undefined,
    ])
  })

  it.each(['removeProject', 'forget', 'invalidate'] as const)(
    'rejects pre-mutation flights after %s instead of restoring stale cached data',
    async (action) => {
      const pending = deferred<{ items: Operation[] }>()
      api.list.mockImplementation((query) =>
        query.project_id === 7 ? pending.promise : Promise.resolve({ items: [] }),
      )
      const store = useOperationsStore()
      const obsolete = store.queryOperations({ project_id: 7 })
      const expectedRejection = expect(obsolete).rejects.toThrow('Session changed')
      const signal = api.list.mock.calls[0]?.[1]?.signal as AbortSignal
      if (action === 'removeProject') store.removeProject(7)
      else if (action === 'forget') store.forget({ task_type: 'translation', task_id: '1' })
      else await store.invalidate()
      expect(signal.aborted).toBe(true)
      api.list.mockResolvedValue({ items: [task('new')] })
      const current = await store.queryOperations({ project_id: 7 })
      pending.resolve({ items: [task('old')] })
      await expectedRejection
      expect(current.items.map((item) => item.task_id)).toEqual(['new'])
      expect(
        (await store.queryOperations({ project_id: 7 })).items.map((item) => item.task_id),
      ).toEqual(['new'])
    },
  )

  it('rejects a stale summary after a removed project changes its visibility', async () => {
    const pending = deferred<typeof summary>()
    api.summary.mockReturnValueOnce(pending.promise)
    const store = useOperationsStore()
    const obsolete = store.querySummary()
    const expectedRejection = expect(obsolete).rejects.toThrow('Session changed')
    store.removeProject(7)
    const current = { ...summary, total: { ...counts, running: 0 } }
    api.summary.mockResolvedValue(current)
    await store.ensureSummary()
    pending.resolve(summary)
    await expectedRejection
    expect(store.summary?.total.running).toBe(0)
    expect((await store.querySummary()).total.running).toBe(0)
  })

  it('shares translation detail across project-constrained consumers and validates each separately', async () => {
    const pending = deferred<{ status: string; project_id: number }>()
    api.job.mockReturnValue(pending.promise)
    const store = useOperationsStore()
    const unconstrained = store.queryTranslation('1')
    const valid = store.queryTask({ task_type: 'translation', task_id: '1', project_id: 7 })
    const wrongProject = store.queryTask({ task_type: 'translation', task_id: '1', project_id: 8 })
    const mismatch = expect(wrongProject).rejects.toThrow('operations.inaccessible')
    await Promise.resolve()
    expect(api.job).toHaveBeenCalledTimes(1)
    pending.resolve({ status: 'running', project_id: 7 })
    await expect(unconstrained).resolves.toMatchObject({ project_id: 7 })
    await expect(valid).resolves.toMatchObject({ project_id: 7 })
    await mismatch
  })

  it('aborts a detail transport when its last subscription closes and ignores late delivery', async () => {
    const pending = deferred<{ status: string; project_id: number }>()
    api.job.mockReturnValue(pending.promise)
    const store = useOperationsStore()
    const receive = vi.fn(),
      failure = vi.fn()
    const close = store.subscribeTask({ task_type: 'translation', task_id: '1' }, receive, failure)
    await Promise.resolve()
    const signal = api.job.mock.calls[0]?.[2]?.signal as AbortSignal
    expect(signal.aborted).toBe(false)
    close()
    expect(signal.aborted).toBe(true)
    pending.resolve({ status: 'running', project_id: 7 })
    await vi.advanceTimersByTimeAsync(0)
    expect(receive).not.toHaveBeenCalled()
    expect(failure).not.toHaveBeenCalled()
  })

  it('keeps a shared transport alive for discovery after the detail subscriber closes', async () => {
    const pending = deferred<{ status: string; project_id: number }>()
    api.job.mockReturnValue(pending.promise)
    const store = useOperationsStore()
    const discoveryRead = store.queryTask({ task_type: 'translation', task_id: '1', project_id: 7 })
    const close = store.subscribeTask({ task_type: 'translation', task_id: '1' }, vi.fn(), vi.fn())
    await Promise.resolve()
    const signal = api.job.mock.calls[0]?.[2]?.signal as AbortSignal
    close()
    expect(signal.aborted).toBe(false)
    pending.resolve({ status: 'running', project_id: 7 })
    await expect(discoveryRead).resolves.toMatchObject({ status: 'running' })
    expect(api.job).toHaveBeenCalledTimes(1)
  })

  it('forwards cancellation to sync details and renews the flight after every consumer closes', async () => {
    const pending = deferred<{ status: string }>()
    api.sync.mockReturnValueOnce(pending.promise)
    const store = useOperationsStore()
    const first = new AbortController()
    const request = store.querySync(7, '1', first.signal)
    const cancelled = expect(request).rejects.toMatchObject({ name: 'AbortError' })
    await Promise.resolve()
    const signal = api.sync.mock.calls[0]?.[3]?.signal as AbortSignal
    first.abort()
    expect(signal.aborted).toBe(true)
    await cancelled
    await expect(store.querySync(7, '1')).resolves.toMatchObject({ status: 'completed' })
    expect(api.sync).toHaveBeenCalledTimes(2)
    pending.resolve({ status: 'running' })
  })

  it('a confirmed denial removes expanded historical rows and fences stale list requests', async () => {
    const store = useOperationsStore()
    api.list
      .mockResolvedValueOnce({ items: [task('1')], next_cursor: 'second' })
      .mockResolvedValueOnce({ items: [task('2')] })
    await store.loadList()
    await store.loadList(true)
    store.active = [task('1')]
    const oldPage = deferred<{ items: Operation[] }>()
    api.list.mockReturnValueOnce(oldPage.promise).mockResolvedValueOnce({ items: [] })
    const obsolete = store.queryOperations({ project_id: 7 })
    const rejected = expect(obsolete).rejects.toThrow('Session changed')
    api.job.mockRejectedValueOnce(new ApiError('access revoked', 403))
    await store.discover()
    expect(store.active).toHaveLength(0)
    expect(store.items.map((item) => item.task_id)).toEqual(['2'])
    expect(store.discoveryComplete).toBe(true)
    oldPage.resolve({ items: [task('1')] })
    await rejected
    expect(store.items.map((item) => item.task_id)).toEqual(['2'])
  })

  it('a late terminal snapshot cannot duplicate a task already observed active after retry', async () => {
    const pending = deferred<{ items: Operation[] }>()
    api.list.mockImplementation((query) =>
      query.state === 'terminal' ? pending.promise : Promise.resolve({ items: [task('1')] }),
    )
    const store = useOperationsStore()
    const refreshing = store.refresh()
    await store.discover()
    pending.resolve({
      items: [task('1', 'translation', 'failed'), task('2', 'translation', 'completed')],
    })
    await refreshing
    expect(store.active.map((item) => item.task_id)).toEqual(['1'])
    expect(store.terminal.map((item) => item.task_id)).toEqual(['2'])
  })

  it('routes storage details to the scoped storage API and shares matching reads only', async () => {
    const pending = deferred<{ id: number; status: string; cleanup_status: string }>()
    api.storage.mockReturnValue(pending.promise)
    const store = useOperationsStore()
    const first = store.queryStorage(7, '42')
    const matching = store.queryTask({ task_type: 'storage', project_id: 7, task_id: '42' })
    const anotherProject = store.queryStorage(8, '42')
    await Promise.resolve()
    expect(api.storage).toHaveBeenCalledTimes(2)
    expect(api.storage).toHaveBeenCalledWith(7, 42, { signal: expect.any(AbortSignal) })
    expect(api.job).not.toHaveBeenCalled()
    expect(api.sync).not.toHaveBeenCalled()
    pending.resolve({ id: 42, status: 'needs_action', cleanup_status: 'blocked' })
    await expect(first).resolves.toMatchObject({ status: 'needs_action' })
    await Promise.all([matching, anotherProject])
  })

  it('rejects incomplete and unsafe storage locators before any transport starts', async () => {
    const store = useOperationsStore()
    await expect(store.queryTask({ task_type: 'storage', task_id: '1' })).rejects.toThrow(
      'unsafe-id',
    )
    await expect(store.queryStorage(7, '9007199254740992')).rejects.toThrow('unsafe-id')
    await expect(store.queryStorage(-7, '1')).rejects.toThrow('unsafe-id')
    expect(api.storage).not.toHaveBeenCalled()
    expect(api.job).not.toHaveBeenCalled()
    expect(api.sync).not.toHaveBeenCalled()
  })

  it.each(['waiting_retry', 'needs_action'] as const)(
    'keeps %s active after a discovery disappearance',
    async (status) => {
      const store = useOperationsStore()
      api.list.mockResolvedValueOnce({ items: [task('1', 'storage', status)] })
      await store.discover()
      api.storage.mockResolvedValue({ id: 1, status, cleanup_status: 'blocked' })
      await store.discover()
      expect(store.active).toHaveLength(1)
      expect(store.active[0]?.status).toBe(status)
      expect(store.terminal).toHaveLength(0)
    },
  )

  it.each(['cleanup_pending', 'running', 'blocked'] as const)(
    'polls terminal %s cleanup every thirty seconds only while subscribed',
    async (cleanup_status) => {
      api.storage.mockResolvedValue({ id: 1, status: 'completed', cleanup_status })
      const store = useOperationsStore()
      const receive = vi.fn()
      const close = store.subscribeTask(
        { task_type: 'storage', project_id: 7, task_id: '1' },
        receive,
        vi.fn(),
      )
      await vi.advanceTimersByTimeAsync(0)
      store.start()
      await store.refresh()
      await vi.advanceTimersByTimeAsync(29_999)
      expect(api.storage).toHaveBeenCalledTimes(1)
      await vi.advanceTimersByTimeAsync(1)
      expect(api.storage).toHaveBeenCalledTimes(2)
      expect(receive).toHaveBeenCalledTimes(2)
      close()
      await vi.advanceTimersByTimeAsync(60_000)
      expect(api.storage).toHaveBeenCalledTimes(2)
    },
  )

  it.each(['done'] as const)(
    'stops terminal %s cleanup polling and still supports explicit reads',
    async (cleanup_status) => {
      api.storage.mockResolvedValue({ id: 1, status: 'completed', cleanup_status })
      const store = useOperationsStore()
      store.subscribeTask({ task_type: 'storage', project_id: 7, task_id: '1' }, vi.fn(), vi.fn())
      await vi.advanceTimersByTimeAsync(0)
      store.start()
      await store.refresh()
      await vi.advanceTimersByTimeAsync(60_000)
      expect(api.storage).toHaveBeenCalledTimes(1)
      await store.queryStorage(7, '1')
      expect(api.storage).toHaveBeenCalledTimes(2)
    },
  )

  it('pauses pending cleanup reads while hidden and refreshes on becoming visible', async () => {
    api.storage.mockResolvedValue({ id: 1, status: 'completed', cleanup_status: 'cleanup_pending' })
    const store = useOperationsStore()
    store.subscribeTask({ task_type: 'storage', project_id: 7, task_id: '1' }, vi.fn(), vi.fn())
    await vi.advanceTimersByTimeAsync(0)
    store.start()
    await store.refresh()
    fakeDocument.hidden = true
    fakeDocument.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(60_000)
    expect(api.storage).toHaveBeenCalledTimes(1)
    fakeDocument.hidden = false
    fakeDocument.dispatchEvent(new Event('visibilitychange'))
    await store.refresh()
    expect(api.storage).toHaveBeenCalledTimes(2)
  })

  it('keeps failed terminal cleanup reads at low frequency alongside a running translation', async () => {
    api.list.mockImplementation((query) =>
      Promise.resolve({ items: query.state === 'active' ? [task('2')] : [] }),
    )
    api.storage
      .mockResolvedValueOnce({ id: 1, status: 'completed', cleanup_status: 'cleanup_pending' })
      .mockRejectedValue(new Error('offline'))
    const store = useOperationsStore()
    const fail = vi.fn()
    store.subscribeTask({ task_type: 'storage', project_id: 7, task_id: '1' }, vi.fn(), fail)
    await vi.advanceTimersByTimeAsync(0)
    store.start()
    await store.refresh()
    await vi.advanceTimersByTimeAsync(30_000)
    expect(api.storage).toHaveBeenCalledTimes(2)
    await vi.advanceTimersByTimeAsync(27_000)
    expect(api.storage).toHaveBeenCalledTimes(2)
    expect(fail).toHaveBeenCalledTimes(1)
  })

  it('does not deliver storage reads from an earlier session', async () => {
    const pending = deferred<{ id: number; status: string; cleanup_status: string }>()
    api.storage.mockReturnValueOnce(pending.promise)
    const store = useOperationsStore()
    const receive = vi.fn(),
      fail = vi.fn()
    store.subscribeTask({ task_type: 'storage', project_id: 7, task_id: '1' }, receive, fail)
    await Promise.resolve()
    changeSessionContext('/api/v1', 2, true)
    pending.resolve({ id: 1, status: 'completed', cleanup_status: 'done' })
    await vi.advanceTimersByTimeAsync(0)
    expect(receive).not.toHaveBeenCalled()
    expect(fail).not.toHaveBeenCalled()
  })
})
