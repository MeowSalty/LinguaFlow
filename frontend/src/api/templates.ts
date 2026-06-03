import { t } from '@/i18n'

import type { ApiClient, ApiSchemas } from './client'
import { apiClient } from './client'
import { buildRequestFailureError } from './utils'

type TranslationTemplate = ApiSchemas['TranslationTemplate']
type CreateTemplatePayload = ApiSchemas['CreateTemplateRequest']
type UpdateTemplatePayload = ApiSchemas['UpdateTemplateRequest']

export const fetchTemplates = async (
  client: ApiClient = apiClient,
): Promise<ApiSchemas['TemplateListResponse']> => {
  const { data, error, response } = await client.GET('/templates')

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchTemplatesFailed'), error, response)
  }

  return data
}

export const createTemplate = async (
  payload: CreateTemplatePayload,
  client: ApiClient = apiClient,
): Promise<TranslationTemplate> => {
  const { data, error, response } = await client.POST('/templates', {
    body: payload,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.createTemplateFailed'), error, response)
  }

  return data
}

export const getTemplate = async (
  templateId: number,
  client: ApiClient = apiClient,
): Promise<TranslationTemplate> => {
  const { data, error, response } = await client.GET('/templates/{templateId}', {
    params: { path: { templateId } },
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchTemplateFailed'), error, response)
  }

  return data
}

export const updateTemplate = async (
  templateId: number,
  payload: UpdateTemplatePayload,
  client: ApiClient = apiClient,
): Promise<TranslationTemplate> => {
  const { data, error, response } = await client.PUT('/templates/{templateId}', {
    params: { path: { templateId } },
    body: payload,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.updateTemplateFailed'), error, response)
  }

  return data
}

export const deleteTemplate = async (
  templateId: number,
  client: ApiClient = apiClient,
): Promise<void> => {
  const { error, response } = await client.DELETE('/templates/{templateId}', {
    params: { path: { templateId } },
  })

  if (response && !response.ok) {
    throw buildRequestFailureError(t('api.errors.deleteTemplateFailed'), error, response)
  }
}
