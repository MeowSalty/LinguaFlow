import { t } from '@/i18n'
import { apiClient, type ApiClient, type ApiSchemas } from './client-core'
import { buildRequestFailureError } from './utils'

export type RuntimeSummary = ApiSchemas['RuntimeSummary']
export type RuntimeRunner = ApiSchemas['RuntimeRunner']
export type RuntimeExternalRequests = ApiSchemas['RuntimeExternalRequests']

export async function fetchRuntimeSummary(
  signal?: AbortSignal,
  client: ApiClient = apiClient,
): Promise<RuntimeSummary> {
  const { data, error, response } = await client.GET('/admin/runtime/summary', { signal })
  if (!data) throw buildRequestFailureError(t('runtime.fetchFailed'), error, response)
  return data
}
