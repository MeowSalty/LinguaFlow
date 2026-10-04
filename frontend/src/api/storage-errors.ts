import { t } from '@/i18n'
import { ApiError } from './utils'
import { StaleSessionError } from './session-context'

export interface StorageProblem {
  error_code?: string
  task_id?: number
  operation_id?: string
  check_id?: number
}

/** Only contract-defined identifiers; provider messages and locations are never retained. */
export const safeStorageProblem = (input: unknown): StorageProblem => {
  if (typeof input === 'string') {
    try {
      input = JSON.parse(input)
    } catch {
      return {}
    }
  }
  if (!input || typeof input !== 'object') return {}
  const value = input as Record<string, unknown>
  const result: StorageProblem = {}
  if (typeof value.error_code === 'string' && /^[a-z][a-z0-9_]{0,95}$/.test(value.error_code))
    result.error_code = value.error_code
  for (const key of ['task_id', 'check_id'] as const)
    if (typeof value[key] === 'number' && Number.isSafeInteger(value[key]) && value[key] > 0)
      result[key] = value[key]
  if (typeof value.operation_id === 'string' && /^[a-zA-Z0-9_-]{1,200}$/.test(value.operation_id))
    result.operation_id = value.operation_id
  return result
}

const codeMessages = {
  source_missing: 'sourceMissing',
  source_corrupt: 'sourceCorrupt',
  storage_auth_required: 'authRequired',
  storage_permission_denied: 'permissionDenied',
  storage_crypto_unavailable: 'cryptoUnavailable',
  storage_unavailable: 'unavailable',
  storage_quota_exceeded: 'quotaExceeded',
  repair_content_mismatch: 'contentMismatch',
  source_revision_conflict: 'sourceConflict',
  storage_generation_conflict: 'generationConflict',
  storage_operation_in_progress: 'operationInProgress',
  storage_intent_expired: 'intentExpired',
  storage_idempotency_conflict: 'idempotencyConflict',
  storage_maintenance: 'maintenance',
  storage_payload_too_large: 'tooLarge',
} as const

export const storageTaskErrorMessage = (code?: string): string => {
  const key = codeMessages[code as keyof typeof codeMessages]
  return key ? t(`storageErrors.${key}`) : t('storageErrors.requestFailed')
}

export class StorageApiError extends ApiError implements StorageProblem {
  readonly error_code?: string
  readonly task_id?: number
  readonly operation_id?: string
  readonly check_id?: number
  constructor(message: string, status: number | undefined, problem: StorageProblem) {
    super(message, status)
    this.name = 'StorageApiError'
    this.error_code = problem.error_code
    this.task_id = problem.task_id
    this.operation_id = problem.operation_id
    this.check_id = problem.check_id
  }
}

export const storageRequestError = (
  response?: Pick<Response, 'status'>,
  body?: unknown,
): StorageApiError => {
  const problem = safeStorageProblem(body)
  const messages = {
    400: 'invalidContent',
    401: 'unauthenticated',
    403: 'forbidden',
    404: 'notFound',
    409: 'revisionConflict',
    411: 'lengthRequired',
    413: 'tooLarge',
    415: 'invalidContent',
    422: 'invalidContent',
    429: 'tooManyRequests',
    503: 'unavailable',
    504: 'resultUnknown',
  } as const
  const key = messages[response?.status as keyof typeof messages]
  const message = problem.error_code
    ? storageTaskErrorMessage(problem.error_code)
    : key
      ? t(`storageErrors.${key}`)
      : t(response ? 'storageErrors.requestFailed' : 'storageErrors.resultUnknown')
  return new StorageApiError(message, response?.status, problem)
}

export const storageErrorMessage = (error: unknown): string =>
  error instanceof StorageApiError
    ? error.message
    : error instanceof ApiError
      ? storageRequestError({ status: error.status ?? 0 }, error.problem).message
      : t('storageErrors.resultUnknown')

export const storageTransportFailure = (error: unknown): Error => {
  if (
    error instanceof StorageApiError ||
    error instanceof StaleSessionError ||
    (error instanceof Error && error.name === 'AbortError')
  )
    return error
  return storageRequestError(
    error instanceof ApiError && error.status !== undefined ? { status: error.status } : undefined,
    error instanceof ApiError ? error.problem : undefined,
  )
}

export const storageResultUnknown = (error: unknown): boolean => {
  if (error instanceof StorageApiError && error.error_code === 'storage_operation_in_progress')
    return true
  return (
    !(error instanceof ApiError) ||
    error.status === undefined ||
    error.status === 408 ||
    error.status >= 500
  )
}
