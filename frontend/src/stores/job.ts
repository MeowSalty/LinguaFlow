import { defineStore } from 'pinia'
import { onScopeDispose, ref, watch, type Ref } from 'vue'
import {
  type ApiSchemas,
  cancelJob as cancelJobRequest,
  createJob as createJobRequest,
  fetchJobs,
  pauseJob as pauseJobRequest,
  resumeJob as resumeJobRequest,
  retryJob as retryJobRequest,
} from '@/api/client'
import {
  assertSessionCurrent,
  captureSession,
  isSessionCurrent,
  onSessionChange,
  StaleSessionError,
} from '@/api/session-context'
import { ApiError, isAccessDenied } from '@/api/utils'
import { t } from '@/i18n'
import { extractErrorMessage } from '@/utils/errors'
import { useOperationsStore } from './operations'

type Job = ApiSchemas['Job']
type CreateJobRequest = ApiSchemas['CreateJobRequest']
type JobAction = 'cancel' | 'retry' | 'pause' | 'resume'
export type JobStatusFilter = Job['status'] | 'all'

export const useJobStore = defineStore('job', () => {
  const operations = useOperationsStore()
  const jobs = ref<Job[]>([])
  const jobsCursor = ref<string | null>(null)
  const loadingJobs = ref(false)
  const jobsError = ref<string | null>(null)
  const creatingJob = ref(false)
  const cancellingJobIds = ref<number[]>([])
  const retryingJobIds = ref<number[]>([])
  const pausingJobIds = ref<number[]>([])
  const resumingJobIds = ref<number[]>([])
  const actionError = ref<string | null>(null)
  const activePollingJobIds = ref<Set<number>>(new Set())
  const jobStatusFilter = ref<JobStatusFilter>('all')
  let project: number | null = null
  let viewGeneration = 0
  let listGeneration = 0
  let disposed = false
  let listFlight: { key: string; promise: Promise<void> } | null = null
  let createFlight: Promise<Job> | null = null
  const mutationFlights = new Map<number, { action: JobAction; promise: Promise<Job> }>()
  const pendingIds: Record<JobAction, Ref<number[]>> = {
    cancel: cancellingJobIds,
    retry: retryingJobIds,
    pause: pausingJobIds,
    resume: resumingJobIds,
  }
  const requests = {
    cancel: cancelJobRequest,
    retry: retryJobRequest,
    pause: pauseJobRequest,
    resume: resumeJobRequest,
  }
  const errors = {
    cancel: 'api.errors.cancelJobFailed',
    retry: 'api.errors.retryJobFailed',
    pause: 'api.errors.pauseJobFailed',
    resume: 'api.errors.resumeJobFailed',
  }

  const invalidateList = (): void => {
    listGeneration++
    listFlight = null
    loadingJobs.value = false
  }
  const clearActions = (): void => {
    createFlight = null
    mutationFlights.clear()
    creatingJob.value = false
    for (const ids of Object.values(pendingIds)) ids.value = []
    actionError.value = null
  }
  const activateProject = (projectId: number): void => {
    if (project === projectId) return
    project = projectId
    viewGeneration++
    invalidateList()
    jobs.value = []
    jobsCursor.value = null
    jobsError.value = null
    clearActions()
    activePollingJobIds.value = new Set()
  }
  watch(
    jobStatusFilter,
    () => {
      invalidateList()
      jobs.value = []
      jobsCursor.value = null
      jobsError.value = null
    },
    { flush: 'sync' },
  )

  const loadJobs = (projectId: number, append = false): Promise<void> => {
    if (disposed) return Promise.resolve()
    activateProject(projectId)
    const status = jobStatusFilter.value
    const cursor = append ? jobsCursor.value : null
    if (append && !cursor) return Promise.resolve()
    const key = JSON.stringify([projectId, status, cursor])
    if (listFlight?.key === key) return listFlight.promise
    const session = captureSession()
    const generation = ++listGeneration
    const current = () =>
      !disposed &&
      isSessionCurrent(session) &&
      generation === listGeneration &&
      project === projectId &&
      jobStatusFilter.value === status
    loadingJobs.value = true
    jobsError.value = null
    const work = async (): Promise<void> => {
      try {
        const response = await fetchJobs(projectId, {
          status: status === 'all' ? undefined : status,
          cursor: cursor ?? undefined,
          limit: 50,
        })
        if (!current()) return
        const items = append ? [...jobs.value, ...response.items] : response.items
        jobs.value = [...new Map(items.map((item) => [item.id, item])).values()]
        jobsCursor.value = response.next_cursor ?? null
      } catch (error) {
        if (!current()) return
        if (isAccessDenied(error)) {
          jobs.value = []
          jobsCursor.value = null
          operations.removeProject(projectId)
        }
        jobsError.value = extractErrorMessage(error, t('api.errors.fetchJobsFailed'))
      } finally {
        if (current()) {
          loadingJobs.value = false
          listFlight = null
        }
      }
    }
    const promise = work()
    listFlight = { key, promise }
    return promise
  }

  const accept = (job: Job): void => {
    if (project !== null && job.project_id !== project) return
    invalidateList()
    const rest = jobs.value.filter((item) => item.id !== job.id)
    jobs.value =
      jobStatusFilter.value === 'all' || job.status === jobStatusFilter.value
        ? [job, ...rest]
        : rest
  }
  const forget = (jobId: number): void => {
    invalidateList()
    jobs.value = jobs.value.filter((item) => item.id !== jobId)
    operations.forget({ task_type: 'translation', task_id: String(jobId) })
  }
  const invalidateOperations = (): void => {
    // A successful write is complete even if the independent refresh subsequently fails.
    void operations.invalidate().catch(() => {})
  }
  const conflictRefresh = async (projectId: number | null, jobId?: number): Promise<void> => {
    const session = captureSession()
    const generation = viewGeneration
    const current = () => !disposed && isSessionCurrent(session) && generation === viewGeneration
    if (jobId !== undefined) {
      try {
        const fresh = await operations.queryTranslation(String(jobId))
        if (current()) accept(fresh)
      } catch (error) {
        if (current() && isAccessDenied(error)) forget(jobId)
      }
    }
    if (current() && projectId !== null) await loadJobs(projectId)
  }

  const createJob = (projectId: number, payload: CreateJobRequest): Promise<Job> => {
    if (disposed) return Promise.reject(new StaleSessionError())
    activateProject(projectId)
    if (createFlight) return createFlight
    const session = captureSession()
    const generation = viewGeneration
    const current = () => !disposed && isSessionCurrent(session) && generation === viewGeneration
    creatingJob.value = true
    actionError.value = null
    const work = async (): Promise<Job> => {
      try {
        const job = await createJobRequest(projectId, payload)
        assertSessionCurrent(session)
        if (disposed) throw new StaleSessionError()
        if (current()) accept(job)
        invalidateOperations()
        return job
      } catch (error) {
        if (current()) {
          if (isAccessDenied(error)) {
            jobs.value = []
            operations.removeProject(projectId)
          }
          if (error instanceof ApiError && error.status === 409) await conflictRefresh(projectId)
          if (current())
            actionError.value = extractErrorMessage(error, t('api.errors.createJobFailed'))
        }
        throw error
      } finally {
        if (current()) {
          creatingJob.value = false
          createFlight = null
        }
      }
    }
    createFlight = work()
    return createFlight
  }

  const mutate = (action: JobAction, jobId: number): Promise<Job> => {
    if (disposed) return Promise.reject(new StaleSessionError())
    const pending = mutationFlights.get(jobId)
    if (pending)
      return pending.action === action
        ? pending.promise
        : Promise.reject(new ApiError(t('workbench.details.conflict'), 409))
    const session = captureSession()
    const generation = viewGeneration
    const projectId = jobs.value.find((item) => item.id === jobId)?.project_id ?? project
    const current = () => !disposed && isSessionCurrent(session) && generation === viewGeneration
    const ids = pendingIds[action]
    ids.value = [...ids.value, jobId]
    actionError.value = null
    const work = async (): Promise<Job> => {
      try {
        const job = await requests[action](jobId)
        assertSessionCurrent(session)
        if (disposed) throw new StaleSessionError()
        if (current()) accept(job)
        invalidateOperations()
        return job
      } catch (error) {
        if (current()) {
          if (isAccessDenied(error)) forget(jobId)
          if (error instanceof ApiError && error.status === 409)
            await conflictRefresh(projectId, jobId)
          if (current()) actionError.value = extractErrorMessage(error, t(errors[action]))
        }
        throw error
      } finally {
        if (current()) {
          ids.value = ids.value.filter((id) => id !== jobId)
          mutationFlights.delete(jobId)
        }
      }
    }
    const promise = work()
    mutationFlights.set(jobId, { action, promise })
    return promise
  }
  const cancelJob = async (jobId: number): Promise<void> => {
    await mutate('cancel', jobId)
  }
  const retryJob = async (jobId: number): Promise<void> => {
    await mutate('retry', jobId)
  }
  const pauseJob = (jobId: number): Promise<Job> => mutate('pause', jobId)
  const resumeJob = async (jobId: number): Promise<void> => {
    await mutate('resume', jobId)
  }
  const startPolling = (jobId: number): void => {
    activePollingJobIds.value = new Set([...activePollingJobIds.value, jobId])
  }
  const stopPolling = (jobId: number): void => {
    activePollingJobIds.value = new Set([...activePollingJobIds.value].filter((id) => id !== jobId))
  }
  const isPolling = (jobId: number): boolean => activePollingJobIds.value.has(jobId)
  const reset = (): void => {
    viewGeneration++
    invalidateList()
    project = null
    jobs.value = []
    jobsCursor.value = null
    jobsError.value = null
    jobStatusFilter.value = 'all'
    clearActions()
    activePollingJobIds.value = new Set()
  }
  onScopeDispose(onSessionChange(reset))
  onScopeDispose(() => {
    disposed = true
    reset()
  })

  return {
    jobs,
    jobsCursor,
    loadingJobs,
    jobsError,
    creatingJob,
    cancellingJobIds,
    retryingJobIds,
    pausingJobIds,
    resumingJobIds,
    actionError,
    jobStatusFilter,
    startPolling,
    stopPolling,
    isPolling,
    loadJobs,
    createJob,
    cancelJob,
    retryJob,
    pauseJob,
    resumeJob,
    reset,
  }
})
