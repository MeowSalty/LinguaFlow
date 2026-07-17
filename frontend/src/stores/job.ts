import { defineStore } from 'pinia'
import { ref } from 'vue'

import {
  type ApiSchemas,
  cancelJob as cancelJobRequest,
  createJob as createJobRequest,
  fetchJobs,
  retryJob as retryJobRequest,
} from '@/api/client'
import { t } from '@/i18n'

type Job = ApiSchemas['Job']
type CreateJobRequest = ApiSchemas['CreateJobRequest']

export type JobStatusFilter = Job['status'] | 'all'

const getErrorMessage = (error: unknown, fallback: string): string =>
  error instanceof Error ? error.message : fallback

const upsertById = <T extends { id: number }>(items: T[], item: T): T[] => [
  item,
  ...items.filter((current) => current.id !== item.id),
]

export const useJobStore = defineStore('job', () => {
  // ── 任务状态 ──
  const jobs = ref<Job[]>([])
  const jobsCursor = ref<string | null>(null)
  const loadingJobs = ref(false)
  const jobsError = ref<string | null>(null)
  const creatingJob = ref(false)
  const cancellingJobIds = ref<number[]>([])
  const retryingJobIds = ref<number[]>([])
  const actionError = ref<string | null>(null)

  // ── 轮询状态 ──
  const activePollingJobIds = ref<Set<number>>(new Set())

  // ── 筛选器 ──
  const jobStatusFilter = ref<JobStatusFilter>('all')

  // ── Actions ──

  const loadJobs = async (projectId: number, append = false): Promise<void> => {
    loadingJobs.value = true
    jobsError.value = null

    try {
      const response = await fetchJobs(projectId, {
        status: jobStatusFilter.value === 'all' ? undefined : jobStatusFilter.value,
        cursor: append ? (jobsCursor.value ?? undefined) : undefined,
        limit: 50,
      })
      jobs.value = append ? [...jobs.value, ...response.items] : response.items
      jobsCursor.value = response.next_cursor ?? null
    } catch (error) {
      jobsError.value = getErrorMessage(error, t('api.errors.fetchJobsFailed'))
    } finally {
      loadingJobs.value = false
    }
  }

  const createJob = async (projectId: number, payload: CreateJobRequest): Promise<Job> => {
    creatingJob.value = true
    actionError.value = null

    try {
      const job = await createJobRequest(projectId, payload)
      jobs.value = upsertById(jobs.value, job)
      return job
    } catch (error) {
      actionError.value = getErrorMessage(error, t('api.errors.createJobFailed'))
      throw error
    } finally {
      creatingJob.value = false
    }
  }

  const cancelJob = async (jobId: number): Promise<void> => {
    cancellingJobIds.value = [...cancellingJobIds.value, jobId]
    actionError.value = null

    try {
      const job = await cancelJobRequest(jobId)
      jobs.value = jobs.value.map((item) => (item.id === job.id ? job : item))
    } catch (error) {
      actionError.value = getErrorMessage(error, t('api.errors.cancelJobFailed'))
      throw error
    } finally {
      cancellingJobIds.value = cancellingJobIds.value.filter((id) => id !== jobId)
    }
  }

  const retryJob = async (jobId: number): Promise<void> => {
    retryingJobIds.value = [...retryingJobIds.value, jobId]
    actionError.value = null

    try {
      const job = await retryJobRequest(jobId)
      jobs.value = jobs.value.map((item) => (item.id === job.id ? job : item))
    } catch (error) {
      actionError.value = getErrorMessage(error, t('api.errors.retryJobFailed'))
      throw error
    } finally {
      retryingJobIds.value = retryingJobIds.value.filter((id) => id !== jobId)
    }
  }

  // ── 轮询控制 ──
  const startPolling = (jobId: number): void => {
    activePollingJobIds.value = new Set([...activePollingJobIds.value, jobId])
  }

  const stopPolling = (jobId: number): void => {
    const next = new Set(activePollingJobIds.value)
    next.delete(jobId)
    activePollingJobIds.value = next
  }

  const isPolling = (jobId: number): boolean => activePollingJobIds.value.has(jobId)

  const reset = (): void => {
    jobs.value = []
    jobsCursor.value = null
    jobsError.value = null
    jobStatusFilter.value = 'all'
    actionError.value = null
    activePollingJobIds.value = new Set()
  }

  return {
    jobs,
    jobsCursor,
    loadingJobs,
    jobsError,
    creatingJob,
    cancellingJobIds,
    retryingJobIds,
    actionError,
    jobStatusFilter,
    startPolling,
    stopPolling,
    isPolling,
    loadJobs,
    createJob,
    cancelJob,
    retryJob,
    reset,
  }
})
