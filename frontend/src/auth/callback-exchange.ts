import { apiClient, setApiBaseUrl, setLocalMode, type ApiSchemas } from '../api/client-core'
import { resolveBootstrapService } from '../api/bootstrap-service'
import {
  captureSession,
  changeSessionContext,
  isSessionCurrent,
  normalizeApiBaseUrl,
} from '../api/session-core'
import {
  getAccessToken,
  getRefreshToken,
  getAuthEpoch,
  readStoredApiBaseUrl,
  setAuthSession,
} from '../api/token-storage'
import { setRuntimeTranslator, t } from '../i18n-runtime'
import { authenticationMessages } from '../locales/authentication'
import {
  activateExternalCallback,
  clearExternalFlow,
  clearExternalProof,
  exchangeExternalTicket,
  installExternalAuthLifecycle,
  prepareExternalCallback,
  readExternalFlow,
  readPreparedExternalCallback,
  saveExternalResult,
} from './external-browser'
import { markCallbackPrepared } from './callback-startup'
import { safeReturnPath } from './return-path'
import { authenticationFailure } from './errors'

const transportMessages: Record<string, string> = {
  'api.errors.refreshSessionFailed': '刷新会话失败',
  'api.errors.serverReturned': '服务器返回 {status}',
  'api.errors.requestNotSent': '请求未送达，请检查网络或服务器地址',
}
function installCallbackTranslator(): void {
  setRuntimeTranslator((key, named) => {
    const message = key.startsWith('authentication.')
      ? authenticationMessages[
          key.slice('authentication.'.length) as keyof typeof authenticationMessages
        ]
      : transportMessages[key]
    return (message ?? key).replace(/\{(\w+)\}/g, (match, name: string) =>
      named && name in named ? String(named[name]) : match,
    )
  })
}

class CallbackIdentityError extends Error {}
const invalid = () => new CallbackIdentityError(t('authentication.flowInvalid'))
export function callbackFailureMessage(
  error: unknown,
): 'flowInvalid' | 'flowExpired' | 'resultUnknown' {
  if (
    error instanceof CallbackIdentityError ||
    (error instanceof Error && error.message === t('authentication.flowInvalid'))
  )
    return 'flowInvalid'
  if (error instanceof Error && error.message === t('authentication.flowExpired'))
    return 'flowExpired'
  return authenticationFailure(error, 'external-complete') === 'flow-invalid'
    ? 'flowInvalid'
    : 'resultUnknown'
}
const fresh = (value: string) =>
  Number.isFinite(Date.parse(value)) && Date.parse(value) > Date.now()
const validUser = (value: unknown): value is ApiSchemas['User'] => {
  if (!value || typeof value !== 'object') return false
  const user = value as Partial<ApiSchemas['User']>
  return (
    Number.isSafeInteger(user.id) &&
    Number(user.id) > 0 &&
    user.active === true &&
    typeof user.username === 'string' &&
    !!user.username &&
    typeof user.role === 'string' &&
    (user.email === null || typeof user.email === 'string')
  )
}
const validSession = (value: unknown): value is ApiSchemas['AuthSession'] => {
  if (!value || typeof value !== 'object') return false
  const session = value as Partial<ApiSchemas['AuthSession']>
  return (
    validUser(session.user) &&
    typeof session.access_token === 'string' &&
    !!session.access_token &&
    typeof session.refresh_token === 'string' &&
    !!session.refresh_token &&
    session.token_type === 'Bearer' &&
    typeof session.expires_at === 'string' &&
    fresh(session.expires_at) &&
    typeof session.refresh_expires_at === 'string' &&
    fresh(session.refresh_expires_at)
  )
}

export function discardAuthCallback(): void {
  clearExternalFlow()
  clearExternalProof()
}

/** Restore the minimum identity, then consume the ticket before requesting the application. */
export async function completeAuthCallback(
  ticket: string,
  signal: AbortSignal,
): Promise<string | undefined> {
  installCallbackTranslator()
  installExternalAuthLifecycle()
  prepareExternalCallback(ticket)
  const candidate = readPreparedExternalCallback()
  const initial = captureSession()
  const assertCandidate = () => {
    signal.throwIfAborted()
    if (
      !candidate ||
      !isSessionCurrent(initial) ||
      !fresh(candidate.expiresAt) ||
      candidate.baseUrl !== initial.baseUrl ||
      candidate.epoch !== getAuthEpoch() ||
      normalizeApiBaseUrl(readStoredApiBaseUrl() ?? undefined) !== candidate.baseUrl
    )
      throw invalid()
  }
  try {
    assertCandidate()
    if (!candidate) throw invalid()
    // Starting an anonymous login cleared both tokens; unrelated credentials cannot be adopted here.
    if (candidate.userId === null && (getAccessToken() || getRefreshToken())) throw invalid()
    const service = await resolveBootstrapService(signal)
    assertCandidate()
    if (
      !service ||
      service.mode !== 'server' ||
      normalizeApiBaseUrl(service.baseUrl) !== candidate.baseUrl
    )
      throw invalid()
    setApiBaseUrl(service.baseUrl)
    setLocalMode(false)
    if (candidate.userId !== null) {
      if (!getAccessToken() || !getRefreshToken()) throw invalid()
      // This client retains the normal refresh mutex and unknown-refresh-result protection.
      const { data, response } = await apiClient.GET('/users/me', { signal })
      assertCandidate()
      if (!response.ok || !validUser(data) || data.id !== candidate.userId) throw invalid()
      changeSessionContext(candidate.baseUrl, data.id)
    }
    signal.throwIfAborted()
    activateExternalCallback()
    const flow = readExternalFlow()
    if (!flow) throw invalid()
    const context = captureSession()
    const result = await exchangeExternalTicket(ticket, signal)
    signal.throwIfAborted()
    if (!isSessionCurrent(context) || getAuthEpoch() !== flow.epoch) throw invalid()
    let expiresAt: string | undefined
    if (result.kind === 'session') {
      if (!validSession(result.session)) throw invalid()
      setAuthSession(result.session)
      changeSessionContext(flow.baseUrl, result.session.user.id)
      history.replaceState(history.state, '', safeReturnPath(flow.returnTo))
    } else if (result.kind === 'reauthentication_proof') {
      saveExternalResult(result)
      expiresAt = result.expires_at
      history.replaceState(history.state, '', '/settings/security')
    } else if (result.kind === 'signup_confirmation' || result.kind === 'link_confirmation') {
      // The confirmation token and verifier remain private in the existing runtime.
      expiresAt = readExternalFlow()?.expiresAt
    } else throw invalid()
    markCallbackPrepared(signal)
    return expiresAt
  } catch (error) {
    discardAuthCallback()
    throw error
  }
}
