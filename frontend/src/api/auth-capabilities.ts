import { apiClient, type ApiClient, type ApiSchemas } from './client-core'
import { buildRequestFailureError } from './utils'
import { t } from '@/i18n'
import { isAuthCapabilities } from './auth-contract'

export async function fetchAuthCapabilities(
  client: ApiClient = apiClient,
): Promise<ApiSchemas['AuthCapabilities']> {
  const { data, error, response } = await client.GET('/auth/capabilities')
  if (!response.ok || error || !isAuthCapabilities(data))
    throw buildRequestFailureError(t('authentication.capabilitiesFailed'), error, response)
  return data
}
