import { t } from '@/i18n'

import type { ApiClient, ApiSchemas } from './client'
import { apiClient } from './client'
import { buildRequestFailureError } from './utils'

type PrunePromptTemplate = ApiSchemas['PrunePromptTemplate']
type CreatePrunePromptTemplateRequest = ApiSchemas['CreatePrunePromptTemplateRequest']
type UpdatePrunePromptTemplateRequest = ApiSchemas['UpdatePrunePromptTemplateRequest']

export const fetchPrunePromptTemplates = async (
  client: ApiClient = apiClient,
): Promise<ApiSchemas['PrunePromptTemplateListResponse']> => {
  const { data, error, response } = await client.GET('/prune-prompt-templates')

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchPrunePromptTemplatesFailed'), error, response)
  }

  return data
}

export const createPrunePromptTemplate = async (
  payload: CreatePrunePromptTemplateRequest,
  client: ApiClient = apiClient,
): Promise<PrunePromptTemplate> => {
  const { data, error, response } = await client.POST('/prune-prompt-templates', {
    body: payload,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.createPrunePromptTemplateFailed'), error, response)
  }

  return data
}

export const getPrunePromptTemplate = async (
  prunePromptTemplateId: number,
  client: ApiClient = apiClient,
): Promise<PrunePromptTemplate> => {
  const { data, error, response } = await client.GET(
    '/prune-prompt-templates/{prunePromptTemplateId}',
    { params: { path: { prunePromptTemplateId } } },
  )

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchPrunePromptTemplateFailed'), error, response)
  }

  return data
}

export const updatePrunePromptTemplate = async (
  prunePromptTemplateId: number,
  payload: UpdatePrunePromptTemplateRequest,
  client: ApiClient = apiClient,
): Promise<PrunePromptTemplate> => {
  const { data, error, response } = await client.PUT(
    '/prune-prompt-templates/{prunePromptTemplateId}',
    {
      params: { path: { prunePromptTemplateId } },
      body: payload,
    },
  )

  if (!data) {
    throw buildRequestFailureError(t('api.errors.updatePrunePromptTemplateFailed'), error, response)
  }

  return data
}

export const deletePrunePromptTemplate = async (
  prunePromptTemplateId: number,
  client: ApiClient = apiClient,
): Promise<void> => {
  const { error, response } = await client.DELETE(
    '/prune-prompt-templates/{prunePromptTemplateId}',
    { params: { path: { prunePromptTemplateId } } },
  )

  if (response && !response.ok) {
    throw buildRequestFailureError(t('api.errors.deletePrunePromptTemplateFailed'), error, response)
  }
}
