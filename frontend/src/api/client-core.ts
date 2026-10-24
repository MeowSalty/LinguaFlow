import createClient, { type Client, type ClientOptions, type Middleware } from 'openapi-fetch'
import { t } from '@/i18n-runtime'
import type { components, paths } from './types'
import {
  clearAuthTokens,
  clearRefreshAttempt,
  getAccessToken,
  getAuthEpoch,
  getDefaultTokenStorage,
  getRefreshToken,
  readRefreshAttempt,
  readStoredApiBaseUrl,
  readStoredAuthTokens,
  setAuthSession,
  setAuthTokensHidden,
  writeRefreshAttempt,
  type AuthTokens,
} from './token-storage'
import { ApiError, buildRequestFailureError } from './utils'
import {
  assertSessionCurrent,
  captureSession,
  changeSessionContext,
  isAuthSessionCurrent,
  normalizeApiBaseUrl,
  StaleSessionError,
  type SessionSnapshot,
} from './session-core'

export type ApiPaths = paths
export type ApiSchemas = components['schemas']
export type ApiClient = Client<ApiPaths>
export interface ApiClientOptions extends Omit<ClientOptions, 'baseUrl'> {
  baseUrl?: string
  tokenStorage?: import('./token-storage').TokenStorage
  getAccessToken?: () => string | null | undefined
}
// Anonymous endpoints never carry a bearer token and never trigger a rotation.
const AUTH_TOKEN_SKIP_PATHS = new Set([
  '/ping',
  '/mode',
  '/auth/register',
  '/auth/login',
  '/auth/refresh',
  '/auth/capabilities',
  '/auth/external/{connectionId}/start',
])
let _isLocalMode = false
export const isLocalMode = (): boolean => _isLocalMode
export const setLocalMode = (value: boolean): void => {
  _isLocalMode = value
}
let unauthorized: (() => void) | null = null
let unauthorizedGeneration = -1
export const setUnauthorizedHandler = (handler: (() => void) | null): void => {
  unauthorized = handler
}
let closing = false
let logoutContext: SessionSnapshot | null = null
let refreshFlight: { generation: number; promise: Promise<AuthTokens> } | null = null

const REFRESH_LOCK_NAME = 'linguaflow.auth.refresh'
/** Status codes that prove the refresh token itself was rejected. */
const DEFINITE_REFRESH_REJECTIONS = new Set([400, 401, 403])

const refreshSessionFailure = (status?: number): ApiError =>
  new ApiError(t('api.errors.refreshSessionFailed'), status)

const storedTokens = (): AuthTokens | null => {
  const accessToken = getAccessToken()
  const refreshToken = getRefreshToken()
  return accessToken && refreshToken
    ? { access_token: accessToken, refresh_token: refreshToken }
    : null
}

/**
 * Rotation needs one mutex per login context. Web Locks supplies that across
 * tabs; a runtime without `window` (unit tests, SSR) is a single context, where
 * the in-memory flight is sufficient; a browser without Web Locks cannot
 * coordinate at all, so it refuses the automatic rotation and asks the user to
 * sign in again instead of risking a second spend of the shared refresh token.
 */
export type RefreshStrategy = 'locked' | 'single-context' | 'unavailable'
export const refreshStrategy = (): RefreshStrategy => {
  const locks = globalThis.navigator?.locks
  if (locks && typeof locks.request === 'function') return 'locked'
  return typeof window === 'undefined' ? 'single-context' : 'unavailable'
}

const withRefreshLock = async <T>(run: () => Promise<T>): Promise<T> => {
  const locks = globalThis.navigator?.locks
  if (!locks || typeof locks.request !== 'function') return run()
  return locks.request(REFRESH_LOCK_NAME, run) as Promise<T>
}

const rawRefresh = async (base: string, refresh: string): Promise<ApiSchemas['AuthSession']> => {
  const response = await fetch(`${base}/auth/refresh`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ refresh_token: refresh }),
    signal: AbortSignal.timeout(15000),
  })
  const data = await response.json().catch(() => undefined)
  if (!response.ok || !data)
    throw buildRequestFailureError(t('api.errors.refreshSessionFailed'), data, response)
  return data as ApiSchemas['AuthSession']
}

/** A login context is only reusable while its account, service and epoch hold. */
const assertRefreshContext = (context: SessionSnapshot, epoch: string): void => {
  if (
    closing ||
    !isAuthSessionCurrent(context) ||
    captureSession().baseUrl !== context.baseUrl ||
    getAuthEpoch() !== epoch
  )
    throw new StaleSessionError()
}

const ownedAttempt = (token: string, epoch: string, baseUrl: string): boolean => {
  const attempt = readRefreshAttempt()
  return attempt?.token === token && attempt.epoch === epoch && attempt.baseUrl === baseUrl
}

const rotateStoredToken = async (
  baseUrl: string,
  token: string,
  epoch: string,
): Promise<ApiSchemas['AuthSession']> => {
  if (ownedAttempt(token, epoch, baseUrl)) throw refreshSessionFailure(401)
  writeRefreshAttempt({ token, epoch, baseUrl })
  try {
    return await rawRefresh(baseUrl, token)
  } catch (error) {
    if (error instanceof ApiError && DEFINITE_REFRESH_REJECTIONS.has(error.status ?? 0)) {
      if (ownedAttempt(token, epoch, baseUrl)) clearRefreshAttempt()
    }
    throw error
  }
}

const performRefresh = async (
  context: SessionSnapshot,
  planned: string,
  epoch: string,
): Promise<AuthTokens> => {
  assertRefreshContext(context, epoch)
  const token = getRefreshToken()
  // Another tab already replaced the shared token: reuse what it produced
  // instead of spending the one-use refresh a second time.
  if (token !== planned) {
    const tokens = storedTokens()
    if (tokens) return tokens
    throw refreshSessionFailure(401)
  }
  if (!token) throw refreshSessionFailure(401)
  const storedBase = readStoredApiBaseUrl()
  // An earlier attempt with this token ended without a definite result; the
  // token may already be consumed, so never replay it automatically.
  const session = await rotateStoredToken(context.baseUrl, token, epoch)
  const tokens: AuthTokens = {
    access_token: session.access_token,
    refresh_token: session.refresh_token,
  }
  if (
    (isAuthSessionCurrent(context) ||
      (logoutContext &&
        isAuthSessionCurrent(logoutContext) &&
        refreshFlight?.generation === context.authGeneration)) &&
    getAuthEpoch() === epoch &&
    readStoredApiBaseUrl() === storedBase &&
    readStoredAuthTokens()?.refresh_token === token
  ) {
    // During local logout the realm mask still prevents UI/request reuse. Publish
    // under the held lock so queued tabs cannot spend the old refresh token.
    setAuthSession(session)
    if (ownedAttempt(token, epoch, context.baseUrl)) clearRefreshAttempt()
  }
  return tokens
}

/**
 * Single coordinator for token rotation: one in-tab flight, cross-tab Web Lock
 * coordination with a token re-read, and reuse of an access token that was
 * rotated after the request which produced a late 401.
 */
export const refreshTokenOnce = async (
  accessTokenUsed?: string | null,
  epochUsed?: string,
): Promise<AuthTokens> => {
  const context = captureSession()
  if (closing) throw new StaleSessionError()
  const epoch = getAuthEpoch()
  // The failed request belonged to a previous login context: never rotate the
  // current account with its credentials.
  if (epochUsed && epochUsed !== epoch) throw new StaleSessionError()
  if (refreshFlight?.generation === context.authGeneration) return refreshFlight.promise
  // A late 401 for an already-rotated access token reuses its replacement.
  const rotated = storedTokens()
  if (rotated && accessTokenUsed && rotated.access_token !== accessTokenUsed) return rotated
  const planned = getRefreshToken()
  if (!planned) throw refreshSessionFailure(401)
  if (refreshStrategy() === 'unavailable') throw refreshSessionFailure(401)
  const promise = withRefreshLock(() => performRefresh(context, planned, epoch)).finally(() => {
    if (refreshFlight?.promise === promise) refreshFlight = null
  })
  refreshFlight = { generation: context.authGeneration, promise }
  return promise
}

const notifyUnauthorized = (context: SessionSnapshot): void => {
  assertSessionCurrent(context)
  if (!unauthorized || unauthorizedGeneration === context.authGeneration) return
  unauthorizedGeneration = context.authGeneration
  unauthorized()
}

/** Legacy deployments may use 401 for a definite storage refusal, not an expired session. */
const isStorageDeploymentRefusal = async (response: { status: number }): Promise<boolean> => {
  try {
    // Keep the original Fetch body readable for the storage error boundary.
    const problem: unknown =
      response instanceof Response
        ? await response.clone().json()
        : 'text' in response && typeof response.text === 'string'
          ? JSON.parse(response.text)
          : undefined
    if (!problem || typeof problem !== 'object' || Array.isArray(problem)) return false
    const code = 'error_code' in problem ? problem.error_code : undefined
    return code === 'storage_deployment_disabled' || code === 'byos_disabled'
  } catch {
    return false
  }
}

/**
 * Shared fetch/XHR recovery: one token rotation and at most one transport replay.
 * `accessTokenUsed`/`epochUsed` let a late 401 reuse an access token that another
 * request already rotated to, and stop a previous login context from consuming
 * the current one.
 */
export const recoverUnauthorizedResponse = async <T extends { status: number }>(
  response: T,
  context: SessionSnapshot,
  replay: (accessToken: string) => Promise<T>,
  accessTokenUsed?: string | null,
  epochUsed?: string,
): Promise<T> => {
  const epoch = epochUsed ?? getAuthEpoch()
  const storedBase = readStoredApiBaseUrl()
  const assertRecoveryCurrent = () => {
    assertSessionCurrent(context)
    if (closing || getAuthEpoch() !== epoch || readStoredApiBaseUrl() !== storedBase)
      throw new StaleSessionError()
  }
  assertRecoveryCurrent()
  if (response.status !== 401) return response
  const deploymentRefusal = await isStorageDeploymentRefusal(response)
  assertRecoveryCurrent()
  if (deploymentRefusal) return response
  if (_isLocalMode) {
    notifyUnauthorized(context)
    return response
  }
  let session: AuthTokens
  try {
    session = await refreshTokenOnce(accessTokenUsed, epoch)
  } catch (error) {
    assertRecoveryCurrent()
    if (error instanceof ApiError && (error.status === 400 || error.status === 401))
      notifyUnauthorized(context)
    throw error
  }
  assertRecoveryCurrent()
  const retried = await replay(session.access_token)
  assertRecoveryCurrent()
  if (retried.status === 401) {
    const refused = await isStorageDeploymentRefusal(retried)
    assertRecoveryCurrent()
    if (!refused) notifyUnauthorized(context)
  }
  return retried
}

export const logoutCurrentSession = async (): Promise<void> => {
  if (closing) return
  const context = captureSession()
  const epoch = getAuthEpoch()
  const storedBase = readStoredApiBaseUrl()
  const planned = readStoredAuthTokens()?.refresh_token
  const inFlight =
    refreshFlight?.generation === context.authGeneration ? refreshFlight.promise : null
  closing = true
  setAuthTokensHidden(true)
  changeSessionContext(context.baseUrl, null, true)
  logoutContext = captureSession()
  const ownsContext = () =>
    logoutContext !== null &&
    isAuthSessionCurrent(logoutContext) &&
    getAuthEpoch() === epoch &&
    readStoredApiBaseUrl() === storedBase
  try {
    if (_isLocalMode) {
      if (ownsContext()) clearAuthTokens()
      return
    }
    // A queued local flight needs the same Web Lock: never await it while
    // holding that lock. Failure still permits best-effort revocation.
    if (inFlight) await inFlight.catch(() => undefined)
    // Serialize with any cross-tab rotation so revocation always uses the latest
    // credentials and a late rotation cannot resurrect the session.
    await withRefreshLock(async () => {
      if (!ownsContext()) return
      try {
        let tokens = readStoredAuthTokens()
        if (!tokens) return
        const revoke = () =>
          fetch(`${context.baseUrl}/auth/logout`, {
            method: 'POST',
            headers: {
              'Content-Type': 'application/json',
              Authorization: `Bearer ${tokens!.access_token}`,
            },
            body: JSON.stringify({ refresh_token: tokens!.refresh_token }),
            signal: AbortSignal.timeout(15000),
          })
        let response = await revoke()
        if (!ownsContext()) return
        if (
          response.status === 401 &&
          !inFlight &&
          tokens.refresh_token === planned &&
          refreshStrategy() !== 'unavailable'
        ) {
          tokens = await rotateStoredToken(context.baseUrl, tokens.refresh_token, epoch)
          if (!ownsContext()) return
          response = await revoke()
        }
        if (!response.ok)
          throw buildRequestFailureError(
            t('operations.logoutUnconfirmed'),
            await response.json().catch(() => undefined),
            response,
          )
      } finally {
        // Login/service changes do not take the refresh lock. Never clear their
        // new credentials after an awaited network response from the old context.
        if (ownsContext()) clearAuthTokens()
      }
    })
  } finally {
    logoutContext = null
    setAuthTokensHidden(false)
    closing = false
  }
}

export const createAuthMiddleware = (
  readAccessToken: () => string | null | undefined = getAccessToken,
): Middleware => {
  const contexts = new WeakMap<Request, SessionSnapshot>()
  const retryRequests = new WeakMap<Request, Request>()
  const epochs = new WeakMap<Request, string>()
  return {
    onRequest({ request, schemaPath }) {
      const context = captureSession()
      if (closing && schemaPath !== '/ping' && schemaPath !== '/mode') throw new StaleSessionError()
      const bound = new Request(request, {
        signal: AbortSignal.any([request.signal, context.signal]),
      })
      contexts.set(bound, context)
      if (!AUTH_TOKEN_SKIP_PATHS.has(schemaPath)) {
        const access = readAccessToken()
        if (access && !_isLocalMode && !bound.headers.has('Authorization')) {
          bound.headers.set('Authorization', `Bearer ${access}`)
          epochs.set(bound, getAuthEpoch())
        }
      }
      retryRequests.set(bound, bound.clone())
      return bound
    },
    async onResponse({ response, request, schemaPath }) {
      const context = contexts.get(request) ?? captureSession()
      assertSessionCurrent(context)
      if (closing && schemaPath !== '/ping' && schemaPath !== '/mode') throw new StaleSessionError()
      if (response.status !== 401 || AUTH_TOKEN_SKIP_PATHS.has(schemaPath)) return response
      // Anonymous requests (dual-purpose external exchange/complete/cancel) must
      // not rotate or drop the session; local mode keeps its logout semantics.
      const authorization = request.headers.get('Authorization')
      if (!authorization && !_isLocalMode) return response
      return recoverUnauthorizedResponse(
        response,
        context,
        (accessToken) => {
          const headers = new Headers(request.headers)
          headers.set('Authorization', `Bearer ${accessToken}`)
          return fetch(new Request(retryRequests.get(request) ?? request, { headers }))
        },
        authorization?.replace(/^Bearer\s+/i, ''),
        epochs.get(request),
      )
    },
  }
}
export const createApiClient = (options: ApiClientOptions = {}): ApiClient => {
  const {
    baseUrl,
    tokenStorage = getDefaultTokenStorage(),
    getAccessToken: reader,
    ...rest
  } = options
  const client = createClient<ApiPaths>({ ...rest, baseUrl: normalizeApiBaseUrl(baseUrl) })
  client.use(createAuthMiddleware(reader ?? (() => getAccessToken(tokenStorage))))
  return client
}
const storedBase = getDefaultTokenStorage().getItem('linguaflow.api_base_url')
changeSessionContext(storedBase ?? '/api/v1', null)
let _client = createApiClient({ baseUrl: storedBase ?? undefined })
export const setApiBaseUrl = (baseUrl: string): void => {
  const normalized = normalizeApiBaseUrl(baseUrl)
  if (captureSession().baseUrl !== normalized) {
    clearAuthTokens()
    changeSessionContext(normalized, null, true)
  }
  _client = createApiClient({ baseUrl: normalized })
  getDefaultTokenStorage().setItem('linguaflow.api_base_url', normalized)
}
export const pingService = async (baseUrl: string): Promise<ApiSchemas['HealthResponse']> => {
  const { data, error, response } = await createApiClient({
    baseUrl,
    getAccessToken: () => null,
  }).GET('/ping')
  if (!data) throw buildRequestFailureError(t('api.errors.pingFailed'), error, response)
  return data
}
export const apiClient = new Proxy({} as ApiClient, {
  get: (_target, prop) => Reflect.get(_client as object, prop),
  has: (_target, prop) => Reflect.has(_client as object, prop),
}) as ApiClient
