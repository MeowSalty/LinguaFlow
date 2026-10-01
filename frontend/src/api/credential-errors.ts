import { ApiError } from './utils'
import { t } from '@/i18n'

/** Never preserve upstream messages, response bodies or submitted secrets in errors. */
export function safeCredentialError(cause?: unknown, response?: Response): Error {
  if (cause instanceof Error && ['AbortError', 'StaleSessionError'].includes(cause.name))
    return cause
  const status = response?.status ?? (cause instanceof ApiError ? cause.status : undefined)
  const key =
    status === 401 || status === 403 || status === 404
      ? 'configurationCredentials.denied'
      : status === 400 || status === 409 || status === 422
        ? 'configurationCredentials.bindingFailure'
        : 'configurationCredentials.failure'
  return new ApiError(t(key), status)
}

export const isUnknownCredentialWrite = (cause: unknown): boolean =>
  !(cause instanceof ApiError && cause.status !== undefined) &&
  !(cause instanceof Error && cause.name === 'StaleSessionError')

export const isCredentialAccessDenied = (cause: unknown): boolean =>
  cause instanceof ApiError && [401, 403, 404].includes(cause.status ?? 0)
