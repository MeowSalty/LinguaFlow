import { t } from '@/i18n'

import type { ApiClient, ApiSchemas } from './client'
import { apiClient } from './client'
import { buildRequestFailureError } from './utils'

type PromptTemplate = ApiSchemas['PromptTemplate']
type CreatePromptTemplateRequest = ApiSchemas['CreatePromptTemplateRequest']
type UpdatePromptTemplateRequest = ApiSchemas['UpdatePromptTemplateRequest']

export const fetchPromptTemplates = async (
  client: ApiClient = apiClient,
): Promise<ApiSchemas['PromptTemplateListResponse']> => {
  const { data, error, response } = await client.GET('/prompt-templates')

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchPromptTemplatesFailed'), error, response)
  }

  return data
}

export const createPromptTemplate = async (
  payload: CreatePromptTemplateRequest,
  client: ApiClient = apiClient,
): Promise<PromptTemplate> => {
  const { data, error, response } = await client.POST('/prompt-templates', {
    body: payload,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.createPromptTemplateFailed'), error, response)
  }

  return data
}

export const getPromptTemplate = async (
  promptTemplateId: number,
  client: ApiClient = apiClient,
): Promise<PromptTemplate> => {
  const { data, error, response } = await client.GET('/prompt-templates/{promptTemplateId}', {
    params: { path: { promptTemplateId } },
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchPromptTemplateFailed'), error, response)
  }

  return data
}

export const updatePromptTemplate = async (
  promptTemplateId: number,
  payload: UpdatePromptTemplateRequest,
  client: ApiClient = apiClient,
): Promise<PromptTemplate> => {
  const { data, error, response } = await client.PUT('/prompt-templates/{promptTemplateId}', {
    params: { path: { promptTemplateId } },
    body: payload,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.updatePromptTemplateFailed'), error, response)
  }

  return data
}

export const deletePromptTemplate = async (
  promptTemplateId: number,
  client: ApiClient = apiClient,
): Promise<void> => {
  const { error, response } = await client.DELETE('/prompt-templates/{promptTemplateId}', {
    params: { path: { promptTemplateId } },
  })

  if (response && !response.ok) {
    throw buildRequestFailureError(t('api.errors.deletePromptTemplateFailed'), error, response)
  }
}
