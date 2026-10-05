import createClient, { type Client, type ClientOptions, type Middleware } from 'openapi-fetch'
import { t } from '@/i18n'
import type { components, paths } from './types'
import {
  getDefaultTokenStorage,
  getRefreshToken,
  getAccessToken,
  setAuthSession,
  clearAuthTokens,
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
} from './session-context'

export type ApiPaths = paths
export type ApiSchemas = components['schemas']
export type ApiClient = Client<ApiPaths>
export interface ApiClientOptions extends Omit<ClientOptions, 'baseUrl'> {
  baseUrl?: string
  tokenStorage?: import('./token-storage').TokenStorage
  getAccessToken?: () => string | null | undefined
}
const AUTH_TOKEN_SKIP_PATHS = new Set([
  '/ping',
  '/mode',
  '/auth/register',
  '/auth/login',
  '/auth/refresh',
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
let refreshFlight: { generation: number; promise: Promise<ApiSchemas['AuthSession']> } | null = null

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
export const refreshTokenOnce = async (): Promise<ApiSchemas['AuthSession']> => {
  const context = captureSession()
  if (closing) throw new StaleSessionError()
  if (refreshFlight?.generation === context.authGeneration) return refreshFlight.promise
  const token = getRefreshToken()
  if (!token) throw new ApiError(t('api.errors.refreshSessionFailed'), 401)
  const promise = rawRefresh(context.baseUrl, token)
    .then((session) => {
      // Logout may still consume this result, but it must never restore the UI session.
      if (isAuthSessionCurrent(context) && !closing && getRefreshToken() === token)
        setAuthSession(session)
      return session
    })
    .finally(() => {
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

/** Shared fetch/XHR recovery: one token rotation and at most one transport replay. */
export const recoverUnauthorizedResponse = async <T extends { status: number }>(
  response: T,
  context: SessionSnapshot,
  replay: (accessToken: string) => Promise<T>,
): Promise<T> => {
  assertSessionCurrent(context)
  if (response.status !== 401) return response
  if (closing) throw new StaleSessionError()
  if (_isLocalMode) {
    notifyUnauthorized(context)
    return response
  }
  let session: ApiSchemas['AuthSession']
  try {
    session = await refreshTokenOnce()
  } catch (error) {
    assertSessionCurrent(context)
    if (error instanceof ApiError && (error.status === 400 || error.status === 401))
      notifyUnauthorized(context)
    throw error
  }
  assertSessionCurrent(context)
  const retried = await replay(session.access_token)
  assertSessionCurrent(context)
  if (retried.status === 401) notifyUnauthorized(context)
  return retried
}

export const logoutCurrentSession = async (): Promise<void> => {
  if (closing) return
  const context = captureSession()
  const inFlight =
    refreshFlight?.generation === context.authGeneration ? refreshFlight.promise : null
  let access = getAccessToken()
  let refresh = getRefreshToken()
  closing = true
  changeSessionContext(context.baseUrl, null, true)
  clearAuthTokens()
  try {
    if (_isLocalMode || !refresh) return
    let refreshed = false
    if (inFlight) {
      const session = await inFlight
      access = session.access_token
      refresh = session.refresh_token
      refreshed = true
    }
    const revoke = () =>
      fetch(`${context.baseUrl}/auth/logout`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${access}` },
        body: JSON.stringify({ refresh_token: refresh }),
        signal: AbortSignal.timeout(15000),
      })
    let response = await revoke()
    if (response.status === 401 && !refreshed) {
      const session = await rawRefresh(context.baseUrl, refresh)
      access = session.access_token
      refresh = session.refresh_token
      response = await revoke()
    }
    if (!response.ok)
      throw buildRequestFailureError(
        t('operations.logoutUnconfirmed'),
        await response.json().catch(() => undefined),
        response,
      )
  } finally {
    closing = false
  }
}

export const createAuthMiddleware = (
  readAccessToken: () => string | null | undefined = getAccessToken,
): Middleware => {
  const contexts = new WeakMap<Request, SessionSnapshot>()
  const retryRequests = new WeakMap<Request, Request>()
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
        if (access && !_isLocalMode && !bound.headers.has('Authorization'))
          bound.headers.set('Authorization', `Bearer ${access}`)
      }
      retryRequests.set(bound, bound.clone())
      return bound
    },
    async onResponse({ response, request, schemaPath }) {
      const context = contexts.get(request) ?? captureSession()
      assertSessionCurrent(context)
      if (closing && schemaPath !== '/ping' && schemaPath !== '/mode') throw new StaleSessionError()
      if (response.status !== 401 || AUTH_TOKEN_SKIP_PATHS.has(schemaPath)) return response
      return recoverUnauthorizedResponse(response, context, (accessToken) => {
        const headers = new Headers(request.headers)
        headers.set('Authorization', `Bearer ${accessToken}`)
        return fetch(new Request(retryRequests.get(request) ?? request, { headers }))
      })
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
  client.use(
    createAuthMiddleware(reader ?? (() => tokenStorage.getItem('linguaflow.access_token'))),
  )
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
