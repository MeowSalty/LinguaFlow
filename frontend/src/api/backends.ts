import { t } from '@/i18n'

import type { ApiClient, ApiSchemas } from './client'
import { apiClient } from './client'
import { buildRequestFailureError } from './utils'

type Backend = ApiSchemas['Backend']
type CreateBackendPayload = ApiSchemas['CreateBackendRequest']
type UpdateBackendPayload = ApiSchemas['UpdateBackendRequest']
type ListBackendModelsPayload = ApiSchemas['ListBackendModelsRequest']

export const fetchBackends = async (
  client: ApiClient = apiClient,
  orgId?: number,
  signal?: AbortSignal,
): Promise<ApiSchemas['BackendListResponse']> => {
  const { data, error, response } =
    orgId === undefined
      ? await client.GET('/backends', { signal })
      : await client.GET('/orgs/{orgId}/backends', { params: { path: { orgId } }, signal })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchBackendsFailed'), error, response)
  }

  return data
}

export const listBackendModels = async (
  payload: ListBackendModelsPayload,
  client: ApiClient = apiClient,
): Promise<ApiSchemas['BackendModelListResponse']> => {
  const { data, error, response } = await client.POST('/backends/models', {
    body: payload,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.listBackendModelsFailed'), error, response)
  }

  return data
}

export const createBackend = async (
  payload: CreateBackendPayload,
  client: ApiClient = apiClient,
  orgId?: number,
): Promise<Backend> => {
  const { data, error, response } =
    orgId === undefined
      ? await client.POST('/backends', {
          body: payload,
        })
      : await client.POST('/orgs/{orgId}/backends', { params: { path: { orgId } }, body: payload })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.createBackendFailed'), error, response)
  }

  return data
}

export const updateBackend = async (
  backendId: number,
  payload: UpdateBackendPayload,
  client: ApiClient = apiClient,
  orgId?: number,
): Promise<Backend> => {
  const { data, error, response } =
    orgId === undefined
      ? await client.PUT('/backends/{backendId}', {
          params: { path: { backendId } },
          body: payload,
        })
      : await client.PUT('/orgs/{orgId}/backends/{backendId}', {
          params: { path: { orgId, backendId } },
          body: payload,
        })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.updateBackendFailed'), error, response)
  }

  return data
}

export const deleteBackend = async (
  backendId: number,
  client: ApiClient = apiClient,
  orgId?: number,
): Promise<void> => {
  const { error, response } =
    orgId === undefined
      ? await client.DELETE('/backends/{backendId}', {
          params: { path: { backendId } },
        })
      : await client.DELETE('/orgs/{orgId}/backends/{backendId}', {
          params: { path: { orgId, backendId } },
        })

  if (response && !response.ok) {
    throw buildRequestFailureError(t('api.errors.deleteBackendFailed'), error, response)
  }
}
