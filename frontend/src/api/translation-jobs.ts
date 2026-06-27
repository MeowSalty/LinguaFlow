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
): Promise<ApiSchemas['TranslationJobListResponse']> => {
  const { data, error, response } = await client.GET('/projects/{projectId}/translation-jobs', {
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
): Promise<ApiSchemas['TranslationJob']> => {
  const { data, error, response } = await client.POST('/projects/{projectId}/translation-jobs', {
    params: { path: { projectId } },
    body: payload,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.createTranslationJobFailed'), error, response)
  }

  return data
}

export const fetchTranslationJob = async (
  translationJobId: number,
  client: ApiClient = apiClient,
): Promise<ApiSchemas['TranslationJob']> => {
  const { data, error, response } = await client.GET('/translation-jobs/{translationJobId}', {
    params: { path: { translationJobId } },
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchTranslationJobFailed'), error, response)
  }

  return data
}

export const cancelTranslationJob = async (
  translationJobId: number,
  client: ApiClient = apiClient,
): Promise<ApiSchemas['TranslationJob']> => {
  const { data, error, response } = await client.POST(
    '/translation-jobs/{translationJobId}/cancel',
    {
      params: { path: { translationJobId } },
    },
  )

  if (!data) {
    throw buildRequestFailureError(t('api.errors.cancelTranslationJobFailed'), error, response)
  }

  return data
}

export const fetchJobEvents = async (
  translationJobId: number,
  params?: { limit?: number },
  client: ApiClient = apiClient,
): Promise<ApiSchemas['JobEvent'][]> => {
  const { data, error, response } = await client.GET(
    '/translation-jobs/{translationJobId}/events',
    {
      params: {
        path: { translationJobId },
        query: { limit: params?.limit ?? 50 },
      },
    },
  )

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchJobEventsFailed'), error, response)
  }

  return data
}

export const retryTranslationJob = async (
  translationJobId: number,
  client: ApiClient = apiClient,
): Promise<ApiSchemas['TranslationJob']> => {
  const { data, error, response } = await client.POST(
    '/translation-jobs/{translationJobId}/retry',
    {
      params: { path: { translationJobId } },
    },
  )

  if (!data) {
    throw buildRequestFailureError(t('api.errors.retryTranslationJobFailed'), error, response)
  }

  return data
}
