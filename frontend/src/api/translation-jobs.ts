import { t } from '@/i18n'

import type { ApiClient, ApiSchemas } from './client'
import { apiClient } from './client'
import { buildRequestFailureError, type DownloadFileResult, getContentDispositionFilename } from './utils'

export const fetchTranslationJobs = async (
  projectId: number,
  params?: {
    status?: 'pending' | 'running' | 'awaiting_review' | 'completed' | 'failed' | 'cancelled'
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
  const { data, error, response } = await client.POST('/translation-jobs/{translationJobId}/cancel', {
    params: { path: { translationJobId } },
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.cancelTranslationJobFailed'), error, response)
  }

  return data
}

export const retryTranslationJob = async (
  translationJobId: number,
  client: ApiClient = apiClient,
): Promise<ApiSchemas['TranslationJob']> => {
  const { data, error, response } = await client.POST('/translation-jobs/{translationJobId}/retry', {
    params: { path: { translationJobId } },
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.retryTranslationJobFailed'), error, response)
  }

  return data
}

export const downloadTranslationJobResult = async (
  translationJobId: number,
  resourceId?: number,
  client: ApiClient = apiClient,
): Promise<DownloadFileResult> => {
  const { data, error, response } = await client.GET(
    '/translation-jobs/{translationJobId}/download',
    {
      params: { path: { translationJobId }, query: { resource_id: resourceId } },
      parseAs: 'blob',
    },
  )

  if (!data) {
    throw buildRequestFailureError(t('api.errors.downloadTranslationJobFailed'), error, response)
  }

  return {
    blob: data as Blob,
    filename: getContentDispositionFilename(response),
  }
}
