import { ApiError } from '@/api/utils'

export type AuthenticationFailure =
  | 'invalid-input'
  | 'username-conflict'
  | 'flow-invalid'
  | 'rule-denied'
  | 'reauthentication-required'
  | 'authentication-conflict'
  | 'forbidden'
  | 'unauthorized'
  | 'unavailable'
  | 'result-unknown'
  | 'generic'

export type AuthenticationOperation =
  | 'login'
  | 'register'
  | 'external-complete'
  | 'security'
  | 'admin'

/** Stable identifiers and the originating operation determine recovery, never detail text. */
export function authenticationFailure(
  error: unknown,
  operation: AuthenticationOperation,
): AuthenticationFailure {
  if (!(error instanceof ApiError))
    return operation === 'external-complete' ? 'result-unknown' : 'generic'
  const type = error.problem?.type
  const title = error.problem?.title
  const matches = (code: string, legacyTitle = code.replaceAll('-', '_')) =>
    type === `urn:linguaflow:${code}` || (!type && title === legacyTitle)
  if (error.status === 400 && matches('external-flow-invalid')) return 'flow-invalid'
  if (error.status === 403 && matches('authentication-rule-denied')) return 'rule-denied'
  if (error.status === 403 && matches('reauthentication-required'))
    return 'reauthentication-required'
  if (error.status === 409) {
    if (
      (operation === 'register' || operation === 'external-complete') &&
      type === 'urn:linguaflow:conflict' &&
      title === 'conflict'
    )
      return 'username-conflict'
    return 'authentication-conflict'
  }
  if (error.status === 400 && matches('invalid-input')) return 'invalid-input'
  if (error.status === 401) return 'unauthorized'
  if (error.status === 403) return 'forbidden'
  if (operation === 'external-complete' && (error.status === undefined || error.status >= 500))
    return 'result-unknown'
  if (error.status === 503) return 'unavailable'
  return 'generic'
}

/** The B5 server leaves these two explicit validation failures unconsumed. */
export const canRetryExternalConfirmation = (error: unknown): boolean =>
  ['invalid-input', 'username-conflict'].includes(authenticationFailure(error, 'external-complete'))
