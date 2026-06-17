import { t } from '@/i18n'

import type { ApiClient, ApiSchemas } from './client'
import { apiClient } from './client'
import { buildRequestFailureError } from './utils'

type ExecutionPlanTemplate = ApiSchemas['ExecutionPlanTemplate']
type CreateExecutionPlanTemplateRequest = ApiSchemas['CreateExecutionPlanTemplateRequest']
type UpdateExecutionPlanTemplateRequest = ApiSchemas['UpdateExecutionPlanTemplateRequest']

export const fetchExecutionPlanTemplates = async (
  client: ApiClient = apiClient,
): Promise<ApiSchemas['ExecutionPlanTemplateListResponse']> => {
  const { data, error, response } = await client.GET('/execution-plan-templates')

  if (!data) {
    throw buildRequestFailureError(
      t('api.errors.fetchExecutionPlanTemplatesFailed'),
      error,
      response,
    )
  }

  return data
}

export const createExecutionPlanTemplate = async (
  payload: CreateExecutionPlanTemplateRequest,
  client: ApiClient = apiClient,
): Promise<ExecutionPlanTemplate> => {
  const { data, error, response } = await client.POST('/execution-plan-templates', {
    body: payload,
  })

  if (!data) {
    throw buildRequestFailureError(
      t('api.errors.createExecutionPlanTemplateFailed'),
      error,
      response,
    )
  }

  return data
}

export const getExecutionPlanTemplate = async (
  executionPlanTemplateId: number,
  client: ApiClient = apiClient,
): Promise<ExecutionPlanTemplate> => {
  const { data, error, response } = await client.GET(
    '/execution-plan-templates/{executionPlanTemplateId}',
    { params: { path: { executionPlanTemplateId } } },
  )

  if (!data) {
    throw buildRequestFailureError(
      t('api.errors.fetchExecutionPlanTemplateFailed'),
      error,
      response,
    )
  }

  return data
}

export const updateExecutionPlanTemplate = async (
  executionPlanTemplateId: number,
  payload: UpdateExecutionPlanTemplateRequest,
  client: ApiClient = apiClient,
): Promise<ExecutionPlanTemplate> => {
  const { data, error, response } = await client.PUT(
    '/execution-plan-templates/{executionPlanTemplateId}',
    { params: { path: { executionPlanTemplateId } }, body: payload },
  )

  if (!data) {
    throw buildRequestFailureError(
      t('api.errors.updateExecutionPlanTemplateFailed'),
      error,
      response,
    )
  }

  return data
}

export const deleteExecutionPlanTemplate = async (
  executionPlanTemplateId: number,
  client: ApiClient = apiClient,
): Promise<void> => {
  const { error, response } = await client.DELETE(
    '/execution-plan-templates/{executionPlanTemplateId}',
    { params: { path: { executionPlanTemplateId } } },
  )

  if (response && !response.ok) {
    throw buildRequestFailureError(
      t('api.errors.deleteExecutionPlanTemplateFailed'),
      error,
      response,
    )
  }
}
