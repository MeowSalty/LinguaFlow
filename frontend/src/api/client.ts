import createClient, { type Client, type ClientOptions, type Middleware } from 'openapi-fetch'

import { t } from '@/i18n'

import type { components, paths } from './types'
import { getDefaultTokenStorage, getRefreshToken, setAuthSession } from './token-storage'
import { buildRequestFailureError } from './utils'

export type ApiPaths = paths
export type ApiSchemas = components['schemas']
export type ApiClient = Client<ApiPaths>

export interface ApiClientOptions extends Omit<ClientOptions, 'baseUrl'> {
  /** API 根地址;不传时默认使用 `/api/v1`。 */
  baseUrl?: string
  /** 自定义 Token 存储，默认使用 `window.localStorage`。 */
  tokenStorage?: import('./token-storage').TokenStorage
  /** 自定义 Access Token 读取逻辑；优先级高于 `tokenStorage`。 */
  getAccessToken?: () => string | null | undefined
}

const DEFAULT_API_BASE_URL = '/api/v1'
const AUTH_TOKEN_SKIP_PATHS = new Set([
  '/ping',
  '/mode',
  '/auth/register',
  '/auth/login',
  '/auth/refresh',
])

const resolveApiBaseUrl = (baseUrl?: string): string => {
  const normalizedBaseUrl = baseUrl?.trim()

  return normalizedBaseUrl || DEFAULT_API_BASE_URL
}

const resolveAccessTokenReader = (
  tokenStorage?: import('./token-storage').TokenStorage,
  getAccessToken?: ApiClientOptions['getAccessToken'],
): (() => string | null | undefined) => {
  if (getAccessToken) {
    return getAccessToken
  }

  return () => tokenStorage?.getItem('linguaflow.access_token')
}

const readStoredApiBaseUrl = (): string | null => {
  return getDefaultTokenStorage()?.getItem('linguaflow.api_base_url') ?? null
}

const writeStoredApiBaseUrl = (baseUrl: string): void => {
  getDefaultTokenStorage()?.setItem('linguaflow.api_base_url', baseUrl)
}

let _isLocalMode = false

export const setLocalMode = (isLocal: boolean): void => {
  _isLocalMode = isLocal
}

type UnauthorizedHandler = () => void
let _onUnauthorized: UnauthorizedHandler | null = null

export const setUnauthorizedHandler = (handler: UnauthorizedHandler | null): void => {
  _onUnauthorized = handler
}

let _refreshPromise: Promise<string | null> | null = null

const tryRefreshToken = async (): Promise<string | null> => {
  const refreshToken = getRefreshToken()
  if (!refreshToken) return null

  try {
    const { data } = await _client.POST('/auth/refresh', {
      body: { refresh_token: refreshToken },
    })

    if (data) {
      setAuthSession(data)
      return data.access_token
    }
    return null
  } catch {
    return null
  }
}

const refreshTokenOnce = (): Promise<string | null> => {
  if (!_refreshPromise) {
    _refreshPromise = tryRefreshToken().finally(() => {
      _refreshPromise = null
    })
  }
  return _refreshPromise
}

export const createAuthMiddleware = (
  readAccessToken = resolveAccessTokenReader(getDefaultTokenStorage()),
): Middleware => ({
  onRequest({ request, schemaPath }) {
    if (AUTH_TOKEN_SKIP_PATHS.has(schemaPath)) {
      return undefined
    }

    const accessToken = readAccessToken()

    if (request.headers.has('Authorization')) {
      return undefined
    }

    if (!accessToken && !_isLocalMode) {
      return new Response(null, { status: 401 })
    }

    if (!accessToken) {
      return undefined
    }

    const headers = new Headers(request.headers)
    headers.set('Authorization', `Bearer ${accessToken}`)

    return new Request(request, { headers })
  },
  async onResponse({ response, request, schemaPath }) {
    if (response.status === 401 && !AUTH_TOKEN_SKIP_PATHS.has(schemaPath)) {
      try {
        const newToken = await refreshTokenOnce()
        if (newToken) {
          const headers = new Headers(request.headers)
          headers.set('Authorization', `Bearer ${newToken}`)
          return fetch(new Request(request, { headers }))
        }
      } catch {
        // refresh failed
      }

      _onUnauthorized?.()
    }

    return undefined
  },
})

export const createApiClient = (options: ApiClientOptions = {}): ApiClient => {
  const {
    baseUrl,
    tokenStorage = getDefaultTokenStorage(),
    getAccessToken,
    ...clientOptions
  } = options
  const readAccessToken = resolveAccessTokenReader(tokenStorage, getAccessToken)
  const client = createClient<ApiPaths>({
    ...clientOptions,
    baseUrl: resolveApiBaseUrl(baseUrl),
  })

  client.use(createAuthMiddleware(readAccessToken))

  return client
}

let _client: ApiClient = createApiClient({
  baseUrl: readStoredApiBaseUrl() ?? undefined,
})

/**
 * 运行时切换 API 根地址。会重建内部 client，并把新地址持久化到 localStorage。
 * 通过 Proxy 暴露的 `apiClient` 会自动指向新实例，调用方无需重新获取。
 */
export const setApiBaseUrl = (baseUrl: string): void => {
  const normalized = resolveApiBaseUrl(baseUrl)
  _client = createApiClient({ baseUrl: normalized })
  writeStoredApiBaseUrl(normalized)
}

export const pingService = async (baseUrl: string): Promise<ApiSchemas['HealthResponse']> => {
  const client = createApiClient({
    baseUrl,
    getAccessToken: () => null,
  })
  const { data, error, response } = await client.GET('/ping')

  if (!data) {
    throw buildRequestFailureError(t('api.errors.pingFailed'), error, response)
  }

  return data
}

export const apiClient = new Proxy({} as ApiClient, {
  get: (_target, prop) => Reflect.get(_client as object, prop),
  has: (_target, prop) => Reflect.has(_client as object, prop),
}) as ApiClient

// Re-export 所有子模块以保持向后兼容
export * from './token-storage'
export * from './utils'
export * from './auth'
export * from './backends'
export * from './projects'
export * from './jobs'
export * from './misc'
export * from './glossary'
export * from './prompt-templates'
export * from './bootstrap-prompt-templates'
export * from './prune-prompt-templates'
export * from './execution-profiles'
export * from './execution-plan-templates'
export * from './system'
export * from './admin'
