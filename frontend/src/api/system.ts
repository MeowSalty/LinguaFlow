import { t } from '@/i18n'

import type { ApiClient, ApiSchemas } from './client'
import { apiClient } from './client'
import { buildRequestFailureError } from './utils'

export const fetchMode = async (
  client: ApiClient = apiClient,
): Promise<ApiSchemas['ModeResponse']> => {
  const { data, error, response } = await client.GET('/mode')

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchModeFailed'), error, response)
  }

  return data
}
