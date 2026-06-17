import { defineStore } from 'pinia'
import { ref } from 'vue'

import {
  type ApiSchemas,
  cancelTranslationJob as cancelTranslationJobRequest,
  createTranslationJob as createTranslationJobRequest,
  fetchTranslationJob,
  fetchTranslationJobs,
  retryTranslationJob as retryTranslationJobRequest,
} from '@/api/client'
import { t } from '@/i18n'

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

  const reset = (): void => {
    jobs.value = []
    selectedJob.value = null
    jobsCursor.value = null
    jobsError.value = null
    jobDetailError.value = null
    jobStatusFilter.value = 'all'
    actionError.value = null
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
    loadJobs,
    loadJobDetail,
    createJob,
    cancelJob,
    retryJob,
    reset,
  }
})
