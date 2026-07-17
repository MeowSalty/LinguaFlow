import { t } from '@/i18n'

import type { ApiClient, ApiSchemas } from './client'
import { apiClient } from './client'
import { buildRequestFailureError } from './utils'

type ExecutionProfile = ApiSchemas['ExecutionProfile']
type CreateExecutionProfileRequest = ApiSchemas['CreateExecutionProfileRequest']
type UpdateExecutionProfileRequest = ApiSchemas['UpdateExecutionProfileRequest']

export const fetchExecutionProfiles = async (
  client: ApiClient = apiClient,
): Promise<ApiSchemas['ExecutionProfileListResponse']> => {
  const { data, error, response } = await client.GET('/execution-profiles')

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchExecutionProfilesFailed'), error, response)
  }

  return data
}

export const createExecutionProfile = async (
  payload: CreateExecutionProfileRequest,
  client: ApiClient = apiClient,
): Promise<ExecutionProfile> => {
  const { data, error, response } = await client.POST('/execution-profiles', {
    body: payload,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.createExecutionProfileFailed'), error, response)
  }

  return data
}

export const getExecutionProfile = async (
  executionProfileId: number,
  client: ApiClient = apiClient,
): Promise<ExecutionProfile> => {
  const { data, error, response } = await client.GET('/execution-profiles/{executionProfileId}', {
    params: { path: { executionProfileId } },
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchExecutionProfileFailed'), error, response)
  }

  return data
}

export const updateExecutionProfile = async (
  executionProfileId: number,
  payload: UpdateExecutionProfileRequest,
  client: ApiClient = apiClient,
): Promise<ExecutionProfile> => {
  const { data, error, response } = await client.PUT('/execution-profiles/{executionProfileId}', {
    params: { path: { executionProfileId } },
    body: payload,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.updateExecutionProfileFailed'), error, response)
  }

  return data
}

export const deleteExecutionProfile = async (
  executionProfileId: number,
  client: ApiClient = apiClient,
): Promise<void> => {
  const { error, response } = await client.DELETE('/execution-profiles/{executionProfileId}', {
    params: { path: { executionProfileId } },
  })

  if (response && !response.ok) {
    throw buildRequestFailureError(t('api.errors.deleteExecutionProfileFailed'), error, response)
  }
}
