import { t } from '@/i18n'

import type { ApiClient, ApiSchemas } from './client'
import { apiClient } from './client'
import { buildRequestFailureError } from './utils'

type TranslationPromptTemplate = ApiSchemas['TranslationPromptTemplate']
type CreateTranslationPromptTemplateRequest = ApiSchemas['CreateTranslationPromptTemplateRequest']
type UpdateTranslationPromptTemplateRequest = ApiSchemas['UpdateTranslationPromptTemplateRequest']

export const fetchPromptTemplates = async (
  client: ApiClient = apiClient,
): Promise<ApiSchemas['TranslationPromptTemplateListResponse']> => {
  const { data, error, response } = await client.GET('/translation-prompt-templates')

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchPromptTemplatesFailed'), error, response)
  }

  return data
}

export const createPromptTemplate = async (
  payload: CreateTranslationPromptTemplateRequest,
  client: ApiClient = apiClient,
): Promise<TranslationPromptTemplate> => {
  const { data, error, response } = await client.POST('/translation-prompt-templates', {
    body: payload,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.createPromptTemplateFailed'), error, response)
  }

  return data
}

export const getPromptTemplate = async (
  translationPromptTemplateId: number,
  client: ApiClient = apiClient,
): Promise<TranslationPromptTemplate> => {
  const { data, error, response } = await client.GET(
    '/translation-prompt-templates/{translationPromptTemplateId}',
    {
      params: { path: { translationPromptTemplateId } },
    },
  )

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchPromptTemplateFailed'), error, response)
  }

  return data
}

export const updatePromptTemplate = async (
  translationPromptTemplateId: number,
  payload: UpdateTranslationPromptTemplateRequest,
  client: ApiClient = apiClient,
): Promise<TranslationPromptTemplate> => {
  const { data, error, response } = await client.PUT(
    '/translation-prompt-templates/{translationPromptTemplateId}',
    {
      params: { path: { translationPromptTemplateId } },
      body: payload,
    },
  )

  if (!data) {
    throw buildRequestFailureError(t('api.errors.updatePromptTemplateFailed'), error, response)
  }

  return data
}

export const deletePromptTemplate = async (
  translationPromptTemplateId: number,
  client: ApiClient = apiClient,
): Promise<void> => {
  const { error, response } = await client.DELETE(
    '/translation-prompt-templates/{translationPromptTemplateId}',
    {
      params: { path: { translationPromptTemplateId } },
    },
  )

  if (response && !response.ok) {
    throw buildRequestFailureError(t('api.errors.deletePromptTemplateFailed'), error, response)
  }
}
