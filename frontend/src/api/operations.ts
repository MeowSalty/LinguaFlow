import { apiClient, type ApiPaths, type ApiSchemas } from './client'
import { buildRequestFailureError } from './utils'
import { t } from '@/i18n'

export type Operation = ApiSchemas['OperationSummary']
export type OperationsSummary = ApiSchemas['OperationsSummaryResponse']
export type OperationsQuery = NonNullable<ApiPaths['/operations']['get']['parameters']['query']>
export type SummaryQuery = NonNullable<
  ApiPaths['/operations/summary']['get']['parameters']['query']
>
export const listOperations = async (
  query: OperationsQuery = {},
  options?: { signal?: AbortSignal },
): Promise<ApiSchemas['OperationListResponse']> => {
  const { data, error, response } = await apiClient.GET('/operations', {
    params: { query },
    signal: options?.signal,
  })
  if (!data) throw buildRequestFailureError(t('operations.loadFailed'), error, response)
  return data
}
export const fetchOperationsSummary = async (
  query: SummaryQuery = {},
  options?: { signal?: AbortSignal },
): Promise<OperationsSummary> => {
  const { data, error, response } = await apiClient.GET('/operations/summary', {
    params: { query },
    signal: options?.signal,
  })
  if (!data) throw buildRequestFailureError(t('operations.loadFailed'), error, response)
  return data
}
