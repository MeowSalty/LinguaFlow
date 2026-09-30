import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope, ref, type EffectScope } from 'vue'
import { useJobActions } from '../useJobActions'
import { changeSessionContext } from '@/api/session-context'
import { ApiError } from '@/api/utils'
import type { ApiSchemas } from '@/api/client'

const state = vi.hoisted(() => ({
  workspace: {
    createJob: vi.fn(),
    cancelJob: vi.fn(),
    retryJob: vi.fn(),
    pauseJob: vi.fn(),
    resumeJob: vi.fn(),
    selectedResourceIds: [1],
    activeResourceId: 1,
    clearSelectedResources: vi.fn(),
    project: { id: 7, name: 'project' },
    creatingJob: false,
    actionError: '',
    cancellingJobIds: [] as number[],
    retryingJobIds: [] as number[],
    pausingJobIds: [] as number[],
    resumingJobIds: [] as number[],
  },
  message: { success: vi.fn(), error: vi.fn(), warning: vi.fn() },
  tracker: {
    drawerJobId: null as number | null,
    trackJob: vi.fn(),
    openDetail: vi.fn(),
    refreshDetail: vi.fn(),
    closeDetail: vi.fn(),
  },
  templates: { items: [{ id: 1, name: 'Plan', rounds: [] }], loadTemplates: vi.fn() },
}))
vi.mock('naive-ui', () => ({ useMessage: () => state.message }))
vi.mock('@/stores/projectWorkspace', () => ({ useProjectWorkspaceStore: () => state.workspace }))
vi.mock('@/stores/globalJobTracker', () => ({ useGlobalJobTrackerStore: () => state.tracker }))
vi.mock('@/stores/executionPlanTemplates', () => ({
  useExecutionPlanTemplatesStore: () => state.templates,
}))
vi.mock('@/i18n', () => ({ t: (key: string) => key }))
const job = { id: 1, project_id: 7, status: 'running' } as ApiSchemas['Job']
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((yes, no) => {
    resolve = yes
    reject = no
  })
  return { promise, resolve, reject }
}

describe('workspace task messages', () => {
  let scope: EffectScope
  beforeEach(() => {
    changeSessionContext('/api/v1', 1, true)
    scope = effectScope()
    state.tracker.drawerJobId = null
  })
  afterEach(() => {
    scope.stop()
    vi.resetAllMocks()
  })
  const actions = (projectId = ref<number | null>(7)) => scope.run(() => useJobActions(projectId))!

  it('uses pause requested when the server has not paused the job yet', async () => {
    state.workspace.pauseJob.mockResolvedValue(job)
    await actions().pauseJob(job)
    expect(state.message.success).toHaveBeenCalledWith('workbench.details.pauseRequested')
  })

  it('does not show stale successes or errors after an identity change', async () => {
    const a = deferred<void>(),
      b = deferred<void>()
    state.workspace.cancelJob.mockReturnValue(a.promise)
    state.workspace.resumeJob.mockReturnValue(b.promise)
    const api = actions()
    const cancel = api.cancelJob(job),
      resume = api.resumeJob(job)
    changeSessionContext('/api/v1', 2, true)
    a.resolve()
    b.reject(new Error('old failure'))
    await Promise.all([cancel, resume])
    expect(state.message.success).not.toHaveBeenCalled()
    expect(state.message.error).not.toHaveBeenCalled()
  })

  it('does not track or announce a late creation after changing projects', async () => {
    const pending = deferred<ApiSchemas['Job']>()
    state.workspace.createJob.mockReturnValue(pending.promise)
    const projectId = ref<number | null>(7)
    const api = actions(projectId)
    api.jobForm.execution_plan_id = 1
    const request = api.submitJob()
    projectId.value = 8
    pending.resolve(job)
    expect(await request).toBe(false)
    expect(state.tracker.trackJob).not.toHaveBeenCalled()
    expect(state.message.success).not.toHaveBeenCalled()
  })

  it('waits for the open detail refresh before displaying a conflict and never retries', async () => {
    const refreshed = deferred<void>()
    state.workspace.cancelJob.mockRejectedValue(new ApiError('conflict', 409))
    state.tracker.drawerJobId = 1
    state.tracker.refreshDetail.mockReturnValue(refreshed.promise)
    const request = actions().cancelJob(job)
    await Promise.resolve()
    expect(state.message.warning).not.toHaveBeenCalled()
    refreshed.resolve()
    await request
    expect(state.workspace.cancelJob).toHaveBeenCalledOnce()
    expect(state.tracker.refreshDetail).toHaveBeenCalledOnce()
    expect(state.message.warning).toHaveBeenCalledWith('workbench.details.conflict')
  })

  it('closes a revoked detail and reports access loss', async () => {
    state.workspace.retryJob.mockRejectedValue(new ApiError('removed', 403))
    state.tracker.drawerJobId = 1
    await actions().retryJob(job)
    expect(state.tracker.closeDetail).toHaveBeenCalledOnce()
    expect(state.message.error).toHaveBeenCalledWith('operations.inaccessible')
  })
})
