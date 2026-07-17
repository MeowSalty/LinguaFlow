import { t } from '@/i18n'

import type { ApiClient, ApiSchemas } from './client'
import { apiClient } from './client'
import { buildRequestFailureError } from './utils'

export const fetchTranslationJobs = async (
  projectId: number,
  params?: {
    status?: 'pending' | 'running' | 'completed' | 'failed' | 'cancelled'
    trigger_type?: 'manual' | 'file_update' | 'glossary_change' | 'web_edit'
    cursor?: string
    limit?: number
  },
  client: ApiClient = apiClient,
): Promise<ApiSchemas['JobListResponse']> => {
  const { data, error, response } = await client.GET('/projects/{projectId}/jobs', {
    params: { path: { projectId }, query: params },
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchTranslationJobsFailed'), error, response)
  }

  return data
}

export const createTranslationJob = async (
  projectId: number,
  payload?: ApiSchemas['CreateTranslationJobRequest'],
  client: ApiClient = apiClient,
): Promise<ApiSchemas['Job']> => {
  const { data, error, response } = await client.POST('/projects/{projectId}/jobs', {
    params: { path: { projectId } },
    body: payload,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.createTranslationJobFailed'), error, response)
  }

  return data
}

export const fetchTranslationJob = async (
  jobId: number,
  client: ApiClient = apiClient,
): Promise<ApiSchemas['Job']> => {
  const { data, error, response } = await client.GET('/jobs/{jobId}', {
    params: { path: { jobId } },
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchTranslationJobFailed'), error, response)
  }

  return data
}

export const cancelTranslationJob = async (
  jobId: number,
  client: ApiClient = apiClient,
): Promise<ApiSchemas['Job']> => {
  const { data, error, response } = await client.POST('/jobs/{jobId}/cancel', {
    params: { path: { jobId } },
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.cancelTranslationJobFailed'), error, response)
  }

  return data
}

export const retryTranslationJob = async (
  jobId: number,
  client: ApiClient = apiClient,
): Promise<ApiSchemas['Job']> => {
  const { data, error, response } = await client.POST('/jobs/{jobId}/retry', {
    params: { path: { jobId } },
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.retryTranslationJobFailed'), error, response)
  }

  return data
}
