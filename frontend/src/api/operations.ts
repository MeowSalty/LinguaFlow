import { apiClient, type ApiPaths, type ApiSchemas } from './client-core'
import { buildRequestFailureError } from './utils'
import { t } from '@/i18n'

export type OperationSummary = ApiSchemas['OperationSummary']
export type Operation = OperationSummary
export type SupportedOperationType = 'translation' | 'glossary_sync' | 'storage'
export type SupportedOperation = Extract<Operation, { task_type: SupportedOperationType }>
export type OperationsSummary = ApiSchemas['OperationsSummaryResponse']
export type OperationsQuery = NonNullable<ApiPaths['/operations']['get']['parameters']['query']>
export type SummaryQuery = NonNullable<
  ApiPaths['/operations/summary']['get']['parameters']['query']
>
export type OperationLocator = {
  task_type: SupportedOperationType
  task_id: string
  project_id?: number
}
// Older servers omit the retirement bucket; treat it as an empty bucket until the server is updated.
const normalizeCounts = (counts: OperationsSummary['total']): OperationsSummary['total'] => ({
  ...counts,
  pausing: counts.pausing ?? 0,
})
const emptyCounts = (): OperationsSummary['total'] => ({
  pending: 0,
  running: 0,
  pausing: 0,
  paused: 0,
  recent_failed: 0,
  waiting_retry: 0,
  needs_action: 0,
})
export const isSupportedOperation = (operation: Operation): operation is SupportedOperation =>
  operation.task_type === 'translation' ||
  operation.task_type === 'glossary_sync' ||
  operation.task_type === 'storage'
export const listOperations = async (
  query: OperationsQuery = {},
  options?: { signal?: AbortSignal },
): Promise<ApiSchemas['OperationListResponse']> => {
  const { data, error, response } = await apiClient.GET('/operations', {
    params: { query },
    signal: options?.signal,
  })
  if (!data) throw buildRequestFailureError(t('operations.loadFailed'), error, response)
  return { ...data, items: data.items.filter(isSupportedOperation) }
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
      storage_retirement: normalizeCounts(data.by_type.storage_retirement ?? emptyCounts()),
    },
  }
}
