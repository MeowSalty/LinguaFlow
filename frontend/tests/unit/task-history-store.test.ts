import { beforeEach, afterEach, describe, it, expect, vi } from 'vitest'
import { createPinia, setActivePinia, disposePinia, type Pinia } from 'pinia'
import { reactive, nextTick } from 'vue'
import { useTaskHistoryStore } from '@/stores/taskHistory'
import { changeSessionContext } from '@/api/session-context'
import { TaskHistoryApiError, type TaskHistoryItem } from '@/api/task-history'
const calls = vi.hoisted(() => ({ single: vi.fn(), batch: vi.fn() }))
vi.mock('@/i18n', () => ({ t: (key: string) => key }))
vi.mock('@/api/task-history', async (original) => ({
  ...(await original<typeof import('@/api/task-history')>()),
  deleteTaskHistory: calls.single,
  batchDeleteTaskHistory: calls.batch,
}))
const context = vi.hoisted(() => ({
  operations: null as unknown,
  tracker: null as unknown,
  jobs: null as unknown,
  glossary: null as unknown,
  stats: null as unknown,
  preferences: null as unknown,
}))
vi.mock('@/stores/operations', () => ({ useOperationsStore: () => context.operations }))
vi.mock('@/stores/globalJobTracker', () => ({ useGlobalJobTrackerStore: () => context.tracker }))
vi.mock('@/stores/job', () => ({ useJobStore: () => context.jobs }))
vi.mock('@/stores/glossary', () => ({ useGlossaryStore: () => context.glossary }))
vi.mock('@/stores/stats', () => ({ useStatsStore: () => context.stats }))
vi.mock('@/stores/preferences', () => ({ usePreferencesStore: () => context.preferences }))
const item = (id = '1', kind: TaskHistoryItem['kind'] = 'translation'): TaskHistoryItem => ({
  kind,
  id,
  project_id: 7,
  can_delete: true,
  status: 'completed',
  project_name: 'Project',
})
function setup() {
  const operations = reactive({
    allDiscovered: [],
    items: [],
    queryTranslation: vi.fn().mockResolvedValue({ can_delete: false, status: 'completed' }),
    capabilityRevision: 0,
    getTaskCapability: vi.fn(),
    querySync: vi.fn().mockResolvedValue({ can_delete: false, status: 'completed' }),
    projectTask: vi.fn(),
    invalidateReads: vi.fn(),
    invalidateTasks: vi.fn(),
    publishHistoryChange: vi.fn(),
    refresh: vi.fn().mockResolvedValue(undefined),
    listError: null,
    summaryError: null,
    filteredSummaryError: null,
  })
  const tracker = reactive({
    drawerJobId: 1 as number | null,
    detailJob: null,
    closeDetail: vi.fn(),
    refreshDetail: vi.fn().mockResolvedValue(undefined),
  })
  const jobs = reactive({
    jobs: [],
    jobsError: null,
    removeHistories: vi.fn(),
    refreshHistory: vi.fn().mockResolvedValue(undefined),
  })
  const glossary = reactive({
    syncTaskSnapshot: null,
    syncTaskProjectId: null,
    syncTaskId: null,
    removeTaskHistories: vi.fn(),
  })
  const stats = { invalidate: vi.fn(), publishInvalidation: vi.fn() },
    preferences = { removeHiddenTerminals: vi.fn() }
  Object.assign(context, { operations, tracker, jobs, glossary, stats, preferences })
  return { operations, tracker, jobs, glossary, stats, preferences }
}
let pinia: Pinia
let state: ReturnType<typeof setup>
beforeEach(() => {
  vi.resetAllMocks()
  pinia = createPinia()
  setActivePinia(pinia)
  changeSessionContext('/api/v1', 1, true)
  state = setup()
  calls.single.mockResolvedValue(undefined)
})
afterEach(() => {
  disposePinia(pinia)
  vi.useRealTimers()
})
describe('shared task history actions', () => {
  it('cancelling never writes or changes local hidden preferences', async () => {
    const store = useTaskHistoryStore()
    store.requestDelete([item()])
    store.cancel()
    await store.submit()
    expect(calls.single).not.toHaveBeenCalled()
    expect(state.preferences.removeHiddenTerminals).not.toHaveBeenCalled()
  })
  it('mixing deleted, missing and forbidden results publishes one removal and refresh', async () => {
    const store = useTaskHistoryStore(),
      targets = [item(), item('1', 'glossary_sync'), item('3')]
    calls.batch.mockResolvedValue({
      items: targets.map((target, i) => ({
        ...target,
        status: ['deleted', 'not_found', 'forbidden'][i],
      })),
      contractError: false,
    })
    state.operations.queryTranslation.mockRejectedValueOnce(new TaskHistoryApiError(403))
    store.requestDelete(targets)
    await store.submit()
    expect(calls.batch).toHaveBeenCalledOnce()
    expect(state.operations.invalidateTasks).toHaveBeenCalledTimes(2)
    expect(state.operations.invalidateTasks.mock.calls[0]![0]).toHaveLength(2)
    expect(state.operations.invalidateTasks.mock.calls[1]![0]).toHaveLength(1)
    expect(state.operations.publishHistoryChange).toHaveBeenCalledOnce()
    expect(state.operations.refresh).toHaveBeenCalledOnce()
    expect(state.stats.publishInvalidation).toHaveBeenCalledOnce()
    expect(store.results.map((result) => result.status)).toEqual([
      'deleted',
      'not_found',
      'forbidden',
    ])
  })
  it('delete refusal retains readable details and refreshes capability without replay', async () => {
    const store = useTaskHistoryStore()
    calls.single.mockRejectedValue(new TaskHistoryApiError(403))
    store.requestDelete([item()])
    await store.submit()
    expect(calls.single).toHaveBeenCalledOnce()
    expect(state.operations.projectTask).toHaveBeenCalledOnce()
    expect(state.tracker.closeDetail).not.toHaveBeenCalled()
    expect(state.preferences.removeHiddenTerminals).not.toHaveBeenCalled()
    expect(store.results[0]?.status).toBe('forbidden')
  })
  it('a lost response remains unknown and is only followed by reads', async () => {
    const store = useTaskHistoryStore()
    calls.single.mockRejectedValue(new TypeError('connection reset'))
    store.requestDelete([item()])
    await store.submit()
    expect(calls.single).toHaveBeenCalledOnce()
    expect(store.results[0]?.status).toBe('unknown')
    expect(store.error).toBe('taskHistoryErrors.resultUnknown')
    expect(state.operations.queryTranslation).toHaveBeenCalledOnce()
    expect(state.operations.invalidateTasks).not.toHaveBeenCalled()
  })
  it('a session switch discards a late deletion result and all associated cleanup', async () => {
    const store = useTaskHistoryStore()
    let finish!: () => void
    calls.single.mockReturnValueOnce(
      new Promise<void>((resolve) => {
        finish = resolve
      }),
    )
    store.requestDelete([item()])
    const pending = store.submit()
    changeSessionContext('/api/v1', 2)
    finish()
    await pending
    expect(store.results).toEqual([])
    expect(state.operations.invalidateTasks).not.toHaveBeenCalled()
    expect(store.submitting).toBe(false)
  })
  it('background capability changes invalidate the frozen confirmation', async () => {
    const store = useTaskHistoryStore()
    store.requestDelete([item()])
    state.operations.getTaskCapability.mockReturnValue({ can_delete: false, status: 'running' })
    state.operations.capabilityRevision++
    await nextTick()
    await store.submit()
    expect(store.confirming).toBe(false)
    expect(calls.single).not.toHaveBeenCalled()
    expect(store.error).toBe('taskHistoryErrors.changed')
  })
  it('a hanging refresh cannot hold a successful deletion or its mutation lock indefinitely', async () => {
    vi.useFakeTimers()
    const store = useTaskHistoryStore()
    state.operations.refresh.mockReturnValue(new Promise(() => {}))
    store.requestDelete([item()])
    const submit = store.submit()
    await vi.advanceTimersByTimeAsync(0)
    expect(store.submitting).toBe(false)
    expect(store.confirming).toBe(false)
    expect(store.results[0]?.status).toBe('deleted')
    expect(state.tracker.closeDetail).toHaveBeenCalledOnce()
    expect(state.operations.invalidateTasks).toHaveBeenCalledOnce()
    await vi.advanceTimersByTimeAsync(15_000)
    await submit
    expect(store.error).toBe('taskHistoryErrors.refreshFailed')
  })
  it('confirmed deletions are fenced before awaiting verification of another batch item', async () => {
    vi.useFakeTimers()
    const store = useTaskHistoryStore(),
      targets = [item(), item('2')]
    let finish!: (value: unknown) => void
    state.operations.queryTranslation.mockReturnValueOnce(
      new Promise((resolve) => {
        finish = resolve
      }),
    )
    calls.batch.mockResolvedValue({
      items: [
        { ...targets[0], status: 'deleted' },
        { ...targets[1], status: 'forbidden' },
      ],
      contractError: false,
    })
    store.requestDelete(targets)
    const submit = store.submit()
    await vi.advanceTimersByTimeAsync(0)
    expect(state.operations.invalidateTasks).toHaveBeenCalledWith(
      [expect.objectContaining({ id: '1' })],
      false,
    )
    expect(state.operations.publishHistoryChange).not.toHaveBeenCalled()
    finish({ can_delete: false, status: 'completed' })
    await submit
    expect(state.operations.publishHistoryChange).toHaveBeenCalledOnce()
  })
})
