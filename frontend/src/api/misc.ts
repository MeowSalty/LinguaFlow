import { t } from '@/i18n'

import type { ApiClient, ApiSchemas } from './client'
import { apiClient } from './client'
import { buildRequestFailureError } from './utils'

export const fetchOrganizations = async (
  client: ApiClient = apiClient,
): Promise<ApiSchemas['OrganizationListResponse']> => {
  const { data, error, response } = await client.GET('/orgs')

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchOrganizationsFailed'), error, response)
  }

  return data
}

export const fetchActivity = async (
  params?: { cursor?: string; limit?: number },
  client: ApiClient = apiClient,
): Promise<ApiSchemas['ActivityListResponse']> => {
  const { data, error, response } = await client.GET('/activity', {
    params: { query: params },
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchActivityFailed'), error, response)
  }

  return data
}
