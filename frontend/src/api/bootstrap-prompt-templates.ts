import { t } from '@/i18n'

import type { ApiClient, ApiSchemas } from './client'
import { apiClient } from './client'
import { buildRequestFailureError } from './utils'

type BootstrapPromptTemplate = ApiSchemas['BootstrapPromptTemplate']
type CreateBootstrapPromptTemplateRequest = ApiSchemas['CreateBootstrapPromptTemplateRequest']
type UpdateBootstrapPromptTemplateRequest = ApiSchemas['UpdateBootstrapPromptTemplateRequest']

export const fetchBootstrapPromptTemplates = async (
  client: ApiClient = apiClient,
): Promise<ApiSchemas['BootstrapPromptTemplateListResponse']> => {
  const { data, error, response } = await client.GET('/bootstrap-prompt-templates')

  if (!data) {
    throw buildRequestFailureError(
      t('api.errors.fetchBootstrapPromptTemplatesFailed'),
      error,
      response,
    )
  }

  return data
}

export const createBootstrapPromptTemplate = async (
  payload: CreateBootstrapPromptTemplateRequest,
  client: ApiClient = apiClient,
): Promise<BootstrapPromptTemplate> => {
  const { data, error, response } = await client.POST('/bootstrap-prompt-templates', {
    body: payload,
  })

  if (!data) {
    throw buildRequestFailureError(
      t('api.errors.createBootstrapPromptTemplateFailed'),
      error,
      response,
    )
  }

  return data
}

export const getBootstrapPromptTemplate = async (
  bootstrapPromptTemplateId: number,
  client: ApiClient = apiClient,
): Promise<BootstrapPromptTemplate> => {
  const { data, error, response } = await client.GET(
    '/bootstrap-prompt-templates/{bootstrapPromptTemplateId}',
    {
      params: { path: { bootstrapPromptTemplateId } },
    },
  )

  if (!data) {
    throw buildRequestFailureError(
      t('api.errors.fetchBootstrapPromptTemplateFailed'),
      error,
      response,
    )
  }

  return data
}

export const updateBootstrapPromptTemplate = async (
  bootstrapPromptTemplateId: number,
  payload: UpdateBootstrapPromptTemplateRequest,
  client: ApiClient = apiClient,
): Promise<BootstrapPromptTemplate> => {
  const { data, error, response } = await client.PUT(
    '/bootstrap-prompt-templates/{bootstrapPromptTemplateId}',
    {
      params: { path: { bootstrapPromptTemplateId } },
      body: payload,
    },
  )

  if (!data) {
    throw buildRequestFailureError(
      t('api.errors.updateBootstrapPromptTemplateFailed'),
      error,
      response,
    )
  }

  return data
}

export const deleteBootstrapPromptTemplate = async (
  bootstrapPromptTemplateId: number,
  client: ApiClient = apiClient,
): Promise<void> => {
  const { error, response } = await client.DELETE(
    '/bootstrap-prompt-templates/{bootstrapPromptTemplateId}',
    {
      params: { path: { bootstrapPromptTemplateId } },
    },
  )

  if (response && !response.ok) {
    throw buildRequestFailureError(
      t('api.errors.deleteBootstrapPromptTemplateFailed'),
      error,
      response,
    )
  }
}
