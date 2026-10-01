import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, disposePinia, setActivePinia, type Pinia } from 'pinia'
import { useGlossaryStore } from '../glossary'
import { changeSessionContext } from '@/api/session-context'
import { ApiError } from '@/api/utils'
import type { ApiSchemas } from '@/api/client'

type Status = ApiSchemas['GlossarySyncTaskStatusResponse']
const calls = vi.hoisted(() => ({
  impact: vi.fn(),
  execute: vi.fn(),
  cancel: vi.fn(),
  entries: vi.fn(),
  subscribe: vi.fn(),
  query: vi.fn(),
  invalidate: vi.fn(),
  stop: vi.fn(),
}))
vi.mock('@/api/client', () => ({
  analyzeGlossarySyncImpact: calls.impact,
  executeGlossarySync: calls.execute,
  cancelGlossarySyncTask: calls.cancel,
  fetchGlossaryEntries: calls.entries,
}))
vi.mock('@/stores/operations', () => ({
  useOperationsStore: () => ({
    subscribeTask: calls.subscribe,
    querySync: calls.query,
    invalidate: calls.invalidate,
  }),
}))
vi.mock('@/i18n', () => ({ t: (key: string) => key }))

describe('glossary task coordination', () => {
  let pinia: Pinia
  let receive: (status: Status) => void
  let fail: (error: unknown) => void
  beforeEach(() => {
    changeSessionContext('/api/v1', 1, true)
    pinia = createPinia()
    setActivePinia(pinia)
    calls.impact.mockResolvedValue({ resources: [] })
    calls.entries.mockResolvedValue({ items: [] })
    calls.execute.mockResolvedValue({
      task_id: '90071992547409930',
      status: 'pending',
      status_url: '/projects/7/sync-tasks/90071992547409930',
    })
    calls.invalidate.mockResolvedValue(undefined)
    calls.subscribe.mockImplementation((_locator, next, error) => {
      receive = next
      fail = error
      return calls.stop
    })
  })
  afterEach(() => {
    disposePinia(pinia)
    vi.resetAllMocks()
  })

  it('subscribes once using the string task id and recovers from a transient read failure', async () => {
    const store = useGlossaryStore()
    await store.openSyncDialog(7, 1, 'source', 'old', 'new')
    await store.submitSync(7, 'all')
    expect(calls.subscribe.mock.calls[0]?.[0]).toEqual({
      task_type: 'glossary_sync',
      project_id: 7,
      task_id: '90071992547409930',
    })
    fail(new Error('offline'))
    expect(store.syncStep).toBe('executing')
    expect(store.syncError).toBe('offline')
    receive({ task_id: store.syncTaskId!, status: 'running', processed: 3, total: 10 })
    expect(store.syncError).toBeNull()
    expect(store.syncProcessed).toBe(3)
    receive({ task_id: store.syncTaskId!, status: 'completed', processed: 10, total: 10 })
    expect(store.syncStep).toBe('result')
    expect(calls.stop).toHaveBeenCalledOnce()
  })

  it('discards late task callbacks after a session switch and releases the subscription', async () => {
    const store = useGlossaryStore()
    await store.openSyncDialog(7, 1, 'source', 'old', 'new')
    await store.submitSync(7, 'all')
    const oldReceive = receive
    changeSessionContext('https://other.example/api/v1', 1, true)
    oldReceive({ task_id: '90071992547409930', status: 'completed', processed: 10, total: 10 })
    expect(store.syncDialogVisible).toBe(false)
    expect(store.syncTaskId).toBeNull()
    expect(store.syncResult).toBeNull()
    expect(calls.stop).toHaveBeenCalledOnce()
  })

  it('refreshes terminal state after cancellation conflict without retrying the write', async () => {
    const store = useGlossaryStore()
    await store.openSyncDialog(7, 1, 'source', 'old', 'new')
    await store.submitSync(7, 'all')
    calls.cancel.mockRejectedValue(new ApiError('already complete', 409))
    calls.query.mockResolvedValue({
      task_id: store.syncTaskId,
      status: 'completed',
      processed: 10,
      total: 10,
    })
    await store.cancelSyncTask(7)
    expect(calls.cancel).toHaveBeenCalledOnce()
    expect(store.syncStep).toBe('result')
    expect(store.syncTaskStatus).toBe('completed')
  })

  it('closes revoked details and ignores a pending submission after the dialog closes', async () => {
    const store = useGlossaryStore()
    await store.openSyncDialog(7, 1, 'source', 'old', 'new')
    let resolve!: (value: unknown) => void
    calls.execute.mockReturnValueOnce(
      new Promise((done) => {
        resolve = done
      }),
    )
    const submitted = store.submitSync(7, 'all')
    store.closeSyncDialog()
    resolve({ task_id: '5', status: 'pending', status_url: '/projects/7/sync-tasks/5' })
    await submitted
    expect(calls.subscribe).not.toHaveBeenCalled()
    await store.openSyncDialog(7, 1, 'source', 'old', 'new')
    await store.submitSync(7, 'all')
    fail(new ApiError('removed', 403))
    expect(store.syncDialogVisible).toBe(false)
    expect(store.items).toEqual([])
    expect(store.error).toBe('workbench.details.unavailable')
  })
})
