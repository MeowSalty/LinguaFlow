import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import {
  type ApiSchemas,
  cancelTranslationJob as cancelTranslationJobRequest,
  createTranslationJob as createTranslationJobRequest,
  fetchTranslationJob,
  fetchTranslationJobs,
  retryTranslationJob as retryTranslationJobRequest,
} from '@/api/client'
import { t } from '@/i18n'
import { getJobProgress } from '@/composables/useWorkspaceUtils'

type TranslationJob = ApiSchemas['TranslationJob']
type CreateTranslationJobPayload = ApiSchemas['CreateTranslationJobRequest']

export type JobStatusFilter = TranslationJob['status'] | 'all'

const getErrorMessage = (error: unknown, fallback: string): string =>
  error instanceof Error ? error.message : fallback

const upsertById = <T extends { id: number }>(items: T[], item: T): T[] => [
  item,
  ...items.filter((current) => current.id !== item.id),
]

export const useTranslationJobStore = defineStore('translationJob', () => {
  // ── 任务状态 ──
  const jobs = ref<TranslationJob[]>([])
  const selectedJob = ref<TranslationJob | null>(null)
  const jobsCursor = ref<string | null>(null)
  const loadingJobs = ref(false)
  const loadingJobDetail = ref(false)
  const jobsError = ref<string | null>(null)
  const jobDetailError = ref<string | null>(null)
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
      const response = await fetchTranslationJobs(projectId, {
        status: jobStatusFilter.value === 'all' ? undefined : jobStatusFilter.value,
        cursor: append ? (jobsCursor.value ?? undefined) : undefined,
        limit: 50,
      })
      jobs.value = append ? [...jobs.value, ...response.items] : response.items
      jobsCursor.value = response.next_cursor ?? null
    } catch (error) {
      jobsError.value = getErrorMessage(error, t('api.errors.fetchTranslationJobsFailed'))
    } finally {
      loadingJobs.value = false
    }
  }

  const loadJobDetail = async (translationJobId: number): Promise<TranslationJob> => {
    loadingJobDetail.value = true
    jobDetailError.value = null

    try {
      const job = await fetchTranslationJob(translationJobId)
      selectedJob.value = job
      jobs.value = jobs.value.map((item) => (item.id === job.id ? job : item))
      return job
    } catch (error) {
      jobDetailError.value = getErrorMessage(error, t('api.errors.fetchTranslationJobFailed'))
      throw error
    } finally {
      loadingJobDetail.value = false
    }
  }

  const createJob = async (
    projectId: number,
    payload: CreateTranslationJobPayload,
  ): Promise<TranslationJob> => {
    creatingJob.value = true
    actionError.value = null

    try {
      const job = await createTranslationJobRequest(projectId, payload)
      jobs.value = upsertById(jobs.value, job)
      return job
    } catch (error) {
      actionError.value = getErrorMessage(error, t('api.errors.createTranslationJobFailed'))
      throw error
    } finally {
      creatingJob.value = false
    }
  }

  const cancelJob = async (translationJobId: number): Promise<void> => {
    cancellingJobIds.value = [...cancellingJobIds.value, translationJobId]
    actionError.value = null

    try {
      const job = await cancelTranslationJobRequest(translationJobId)
      jobs.value = jobs.value.map((item) => (item.id === job.id ? job : item))
    } catch (error) {
      actionError.value = getErrorMessage(error, t('api.errors.cancelTranslationJobFailed'))
      throw error
    } finally {
      cancellingJobIds.value = cancellingJobIds.value.filter((id) => id !== translationJobId)
    }
  }

  const retryJob = async (translationJobId: number): Promise<void> => {
    retryingJobIds.value = [...retryingJobIds.value, translationJobId]
    actionError.value = null

    try {
      const job = await retryTranslationJobRequest(translationJobId)
      jobs.value = jobs.value.map((item) => (item.id === job.id ? job : item))
    } catch (error) {
      actionError.value = getErrorMessage(error, t('api.errors.retryTranslationJobFailed'))
      throw error
    } finally {
      retryingJobIds.value = retryingJobIds.value.filter((id) => id !== translationJobId)
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

  // ── Getters ──
  /** 获取 selectedJob 的进度百分比（优先使用后端计算值） */
  const selectedJobProgress = computed<number>(() => {
    const job = selectedJob.value
    if (!job) return 0
    if (job.progress_percentage != null) return Math.round(job.progress_percentage)
    return getJobProgress(job) // 回退到前端计算
  })

  const reset = (): void => {
    jobs.value = []
    selectedJob.value = null
    jobsCursor.value = null
    jobsError.value = null
    jobDetailError.value = null
    jobStatusFilter.value = 'all'
    actionError.value = null
    activePollingJobIds.value = new Set()
  }

  return {
    jobs,
    selectedJob,
    jobsCursor,
    loadingJobs,
    loadingJobDetail,
    jobsError,
    jobDetailError,
    creatingJob,
    cancellingJobIds,
    retryingJobIds,
    actionError,
    jobStatusFilter,
    startPolling,
    stopPolling,
    isPolling,
    selectedJobProgress,
    loadJobs,
    loadJobDetail,
    createJob,
    cancelJob,
    retryJob,
    reset,
  }
})
