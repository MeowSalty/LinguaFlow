import { apiClient, type ApiClient, type ApiSchemas } from './client-core'
import { buildRequestFailureError } from './utils'
import { t } from '@/i18n'

export async function fetchAuthCapabilities(
  client: ApiClient = apiClient,
): Promise<ApiSchemas['AuthCapabilities']> {
  const { data, error, response } = await client.GET('/auth/capabilities')
  if (!data || !data.password || !Array.isArray(data.connections))
    throw buildRequestFailureError(t('authentication.capabilitiesFailed'), error, response)
  return data
}
