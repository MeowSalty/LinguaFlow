import { apiClient, type ApiPaths, type ApiSchemas } from './client-core'
import { buildRequestFailureError } from './utils'
import { t } from '@/i18n'

export type Operation = ApiSchemas['OperationSummary']
export type OperationsSummary = ApiSchemas['OperationsSummaryResponse']
export type OperationsQuery = NonNullable<ApiPaths['/operations']['get']['parameters']['query']>
export type SummaryQuery = NonNullable<
  ApiPaths['/operations/summary']['get']['parameters']['query']
>
// Older servers omit this bucket; unknown summaries still remain null in the store.
const normalizeCounts = (counts: OperationsSummary['total']): OperationsSummary['total'] => ({
  ...counts,
  pausing: counts.pausing ?? 0,
})
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
  return {
    ...data,
    total: normalizeCounts(data.total),
    by_type: {
      ...data.by_type,
      translation: normalizeCounts(data.by_type.translation),
      glossary_sync: normalizeCounts(data.by_type.glossary_sync),
      storage: normalizeCounts(data.by_type.storage),
    },
  }
}
