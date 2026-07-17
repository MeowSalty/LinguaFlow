import { t } from '@/i18n'

import type { ApiClient, ApiSchemas } from './client'
import { apiClient } from './client'
import { buildRequestFailureError } from './utils'

export const fetchJobs = async (
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
    throw buildRequestFailureError(t('api.errors.fetchJobsFailed'), error, response)
  }

  return data
}

export const createJob = async (
  projectId: number,
  payload?: ApiSchemas['CreateJobRequest'],
  client: ApiClient = apiClient,
): Promise<ApiSchemas['Job']> => {
  const { data, error, response } = await client.POST('/projects/{projectId}/jobs', {
    params: { path: { projectId } },
    body: payload,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.createJobFailed'), error, response)
  }

  return data
}

export const fetchJob = async (
  jobId: number,
  client: ApiClient = apiClient,
): Promise<ApiSchemas['Job']> => {
  const { data, error, response } = await client.GET('/jobs/{jobId}', {
    params: { path: { jobId } },
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchJobFailed'), error, response)
  }

  return data
}

export const cancelJob = async (
  jobId: number,
  client: ApiClient = apiClient,
): Promise<ApiSchemas['Job']> => {
  const { data, error, response } = await client.POST('/jobs/{jobId}/cancel', {
    params: { path: { jobId } },
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.cancelJobFailed'), error, response)
  }

  return data
}

export const retryJob = async (
  jobId: number,
  client: ApiClient = apiClient,
): Promise<ApiSchemas['Job']> => {
  const { data, error, response } = await client.POST('/jobs/{jobId}/retry', {
    params: { path: { jobId } },
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.retryJobFailed'), error, response)
  }

  return data
}
