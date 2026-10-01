import { t } from '@/i18n'

import type { ApiClient, ApiPaths, ApiSchemas } from './client-core'
import { apiClient } from './client-core'
import { buildRequestFailureError } from './utils'

export const fetchActivity = async (
  params?: NonNullable<ApiPaths['/activity']['get']['parameters']['query']>,
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
