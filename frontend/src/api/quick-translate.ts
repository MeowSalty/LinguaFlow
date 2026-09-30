import { t } from '@/i18n'

import type { ApiClient, ApiSchemas } from './client-core'
import { apiClient } from './client-core'
import { buildRequestFailureError } from './utils'

export const quickTranslate = async (
  payload: ApiSchemas['QuickTranslateRequest'],
  client: ApiClient = apiClient,
): Promise<ApiSchemas['QuickTranslateResponse']> => {
  const { data, error, response } = await client.POST('/quick-translate', { body: payload })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.quickTranslateFailed'), error, response)
  }

  return data
}
