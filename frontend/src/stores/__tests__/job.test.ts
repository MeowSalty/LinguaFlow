import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, disposePinia, setActivePinia, type Pinia } from 'pinia'
import { useJobStore } from '../job'
import { changeSessionContext, StaleSessionError } from '@/api/session-context'
import { ApiError } from '@/api/utils'
import type { ApiSchemas } from '@/api/client'

type Job = ApiSchemas['Job']
const api = vi.hoisted(() => ({
  list: vi.fn(),
  create: vi.fn(),
  cancel: vi.fn(),
  retry: vi.fn(),
  pause: vi.fn(),
  resume: vi.fn(),
  invalidate: vi.fn(),
  detail: vi.fn(),
  forget: vi.fn(),
  removeProject: vi.fn(),
}))
vi.mock('@/api/client', () => ({
  fetchJobs: api.list,
  createJob: api.create,
  cancelJob: api.cancel,
  retryJob: api.retry,
  pauseJob: api.pause,
  resumeJob: api.resume,
}))
vi.mock('@/stores/operations', () => ({
  useOperationsStore: () => ({
    invalidate: api.invalidate,
    queryTranslation: api.detail,
    forget: api.forget,
    removeProject: api.removeProject,
  }),
}))
vi.mock('@/i18n', () => ({ t: (key: string) => key }))
const job = (id = 1, status: Job['status'] = 'running', project = 7): Job => ({
  id,
  project_id: project,
  execution_plan_id: 1,
  status,
  trigger_type: 'manual',
  created_at: '2026-09-30T00:00:00Z',
  updated_at: '2026-09-30T00:00:00Z',
  progress: {
    total_resources: 1,
    completed_resources: 0,
    failed_resources: 0,
    progress_total: 1,
    progress_completed: 0,
  },
  execution_config: {},
})
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((yes, no) => {
    resolve = yes
    reject = no
  })
  return { promise, resolve, reject }
}

describe('workspace job requests', () => {
  let pinia: Pinia
  beforeEach(() => {
    changeSessionContext('/api/v1', 1, true)
    pinia = createPinia()
    setActivePinia(pinia)
    api.list.mockResolvedValue({ items: [job()] })
    api.invalidate.mockResolvedValue(undefined)
    api.detail.mockResolvedValue(job(1, 'completed'))
  })
  afterEach(() => {
    disposePinia(pinia)
    vi.resetAllMocks()
  })

  it('merges identical list requests and refuses old project success or loading updates', async () => {
    const a = deferred<{ items: Job[] }>(),
      b = deferred<{ items: Job[] }>()
    api.list.mockReturnValueOnce(a.promise).mockReturnValueOnce(b.promise)
    const store = useJobStore()
    const first = store.loadJobs(7),
      duplicate = store.loadJobs(7)
    expect(api.list).toHaveBeenCalledOnce()
    const other = store.loadJobs(8)
    a.resolve({ items: [job()] })
    await Promise.all([first, duplicate])
    expect(store.jobs).toEqual([])
    expect(store.loadingJobs).toBe(true)
    b.resolve({ items: [job(2, 'pending', 8)] })
    await other
    expect(store.jobs.map((item) => item.id)).toEqual([2])
    expect(store.loadingJobs).toBe(false)
  })

  it('invalidates old filtered requests and preserves the active filter on pagination', async () => {
    const old = deferred<{ items: Job[] }>()
    api.list
      .mockReturnValueOnce(old.promise)
      .mockResolvedValueOnce({ items: [job(2, 'paused')], next_cursor: 'next' })
      .mockResolvedValueOnce({ items: [job(2, 'paused'), job(3, 'paused')] })
    const store = useJobStore()
    const first = store.loadJobs(7)
    store.jobStatusFilter = 'paused'
    await store.loadJobs(7)
    old.reject(new Error('old error'))
    await first
    expect(store.jobsError).toBeNull()
    await store.loadJobs(7, true)
    expect(api.list.mock.calls[2]?.[1]).toEqual({ status: 'paused', cursor: 'next', limit: 50 })
    expect(store.jobs.map((item) => item.id)).toEqual([2, 3])
  })

  it.each(['cancel', 'retry', 'pause', 'resume'] as const)(
    'invalidates operations after %s without waiting for the refresh',
    async (action) => {
      const store = useJobStore()
      await store.loadJobs(7)
      api[action].mockResolvedValue(job(1, 'running'))
      api.invalidate.mockReturnValue(new Promise(() => {}))
      const method = {
        cancel: store.cancelJob,
        retry: store.retryJob,
        pause: store.pauseJob,
        resume: store.resumeJob,
      }[action]
      const response = await method(1)
      expect(api.invalidate).toHaveBeenCalledOnce()
      expect(store.jobs[0]?.status).toBe('running')
      if (action === 'pause') expect(response?.status).toBe('running')
    },
  )

  it('creates once, invalidates operations once, and deduplicates simultaneous identical writes', async () => {
    const pending = deferred<Job>()
    api.create.mockReturnValue(pending.promise)
    const store = useJobStore()
    const first = store.createJob(7, { execution_plan_id: 1 })
    const second = store.createJob(7, { execution_plan_id: 1 })
    expect(api.create).toHaveBeenCalledOnce()
    pending.resolve(job(4, 'pending'))
    await Promise.all([first, second])
    expect(api.invalidate).toHaveBeenCalledOnce()
    expect(store.jobs.map((item) => item.id)).toEqual([4])
    expect(store.creatingJob).toBe(false)
  })

  it('does not commit old write success, errors, or finally state into a new session', async () => {
    const old = deferred<Job>(),
      fresh = deferred<Job>()
    api.pause.mockReturnValueOnce(old.promise).mockReturnValueOnce(fresh.promise)
    const store = useJobStore()
    await store.loadJobs(7)
    const first = store.pauseJob(1)
    const rejection = expect(first).rejects.toBeInstanceOf(StaleSessionError)
    changeSessionContext('https://other.example/api', 1, true)
    await store.loadJobs(7)
    const second = store.pauseJob(1)
    old.resolve(job(1, 'paused'))
    await rejection
    expect(store.jobs[0]?.status).toBe('running')
    expect(store.pausingJobIds).toEqual([1])
    expect(api.invalidate).not.toHaveBeenCalled()
    fresh.resolve(job(1, 'paused'))
    await second
    expect(store.pausingJobIds).toEqual([])
    expect(api.invalidate).toHaveBeenCalledOnce()
  })

  it('rejects an old error without replacing a new list error or loading flag', async () => {
    const pending = deferred<Job>()
    const store = useJobStore()
    api.cancel.mockReturnValueOnce(pending.promise)
    await store.loadJobs(7)
    const old = store.cancelJob(1)
    const rejection = expect(old).rejects.toThrow('old denial')
    changeSessionContext('/api/v1', 2, true)
    store.actionError = 'new context error'
    pending.reject(new ApiError('old denial', 403))
    await rejection
    expect(store.actionError).toBe('new context error')
    expect(api.forget).not.toHaveBeenCalled()
  })

  it('refreshes a conflicting task and list before rejecting, without retrying the write', async () => {
    const store = useJobStore()
    await store.loadJobs(7)
    api.cancel.mockRejectedValue(new ApiError('already complete', 409))
    api.list.mockResolvedValueOnce({ items: [job(1, 'completed')] })
    await expect(store.cancelJob(1)).rejects.toMatchObject({ status: 409 })
    expect(api.cancel).toHaveBeenCalledOnce()
    expect(api.detail).toHaveBeenCalledWith('1')
    expect(api.list).toHaveBeenCalledTimes(2)
    expect(store.jobs[0]?.status).toBe('completed')
    expect(api.invalidate).not.toHaveBeenCalled()
  })

  it('forgets explicitly inaccessible tasks and rejects stale list responses after a write', async () => {
    const store = useJobStore()
    await store.loadJobs(7)
    const stale = deferred<{ items: Job[] }>()
    api.list.mockReturnValueOnce(stale.promise)
    const request = store.loadJobs(7)
    api.cancel.mockResolvedValueOnce(job(1, 'cancelled'))
    await store.cancelJob(1)
    stale.resolve({ items: [job(1, 'running')] })
    await request
    expect(store.jobs[0]?.status).toBe('cancelled')
    api.retry.mockRejectedValue(new ApiError('removed', 404))
    await expect(store.retryJob(1)).rejects.toMatchObject({ status: 404 })
    expect(store.jobs).toEqual([])
    expect(api.forget).toHaveBeenCalledWith({ task_type: 'translation', task_id: '1' })
  })
})
