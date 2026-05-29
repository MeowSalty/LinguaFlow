import createClient, { type Client, type ClientOptions, type Middleware } from 'openapi-fetch'

import { t } from '@/i18n'

import type { components, paths } from './types'

export type ApiPaths = paths
export type ApiSchemas = components['schemas']
export type ApiClient = Client<ApiPaths>
export type AuthSession = ApiSchemas['AuthSession']
export type AuthTokens = Pick<AuthSession, 'access_token' | 'refresh_token'>

export interface TokenStorage {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
  removeItem(key: string): void
}

export interface ApiClientOptions extends Omit<ClientOptions, 'baseUrl'> {
  /** API 根地址;不传时默认使用 `/api/v1`。 */
  baseUrl?: string
  /** 自定义 Token 存储，默认使用 `window.localStorage`。 */
  tokenStorage?: TokenStorage
  /** 自定义 Access Token 读取逻辑；优先级高于 `tokenStorage`。 */
  getAccessToken?: () => string | null | undefined
}

const DEFAULT_API_BASE_URL = '/api/v1'
const ACCESS_TOKEN_STORAGE_KEY = 'linguaflow.access_token'
const REFRESH_TOKEN_STORAGE_KEY = 'linguaflow.refresh_token'
const API_BASE_URL_STORAGE_KEY = 'linguaflow.api_base_url'
const AUTH_TOKEN_SKIP_PATHS = new Set(['/ping', '/auth/register', '/auth/login', '/auth/refresh'])

const getDefaultTokenStorage = (): TokenStorage | undefined => {
  if (typeof window === 'undefined') {
    return undefined
  }

  return window.localStorage
}

const resolveApiBaseUrl = (baseUrl?: string): string => {
  const normalizedBaseUrl = baseUrl?.trim()

  return normalizedBaseUrl || DEFAULT_API_BASE_URL
}

const resolveAccessTokenReader = (
  tokenStorage?: TokenStorage,
  getAccessToken?: ApiClientOptions['getAccessToken'],
): (() => string | null | undefined) => {
  if (getAccessToken) {
    return getAccessToken
  }

  return () => tokenStorage?.getItem(ACCESS_TOKEN_STORAGE_KEY)
}

export const authTokenStorageKeys = {
  accessToken: ACCESS_TOKEN_STORAGE_KEY,
  refreshToken: REFRESH_TOKEN_STORAGE_KEY,
  apiBaseUrl: API_BASE_URL_STORAGE_KEY,
} as const

export const getAccessToken = (tokenStorage = getDefaultTokenStorage()): string | null => {
  return tokenStorage?.getItem(ACCESS_TOKEN_STORAGE_KEY) ?? null
}

export const getRefreshToken = (tokenStorage = getDefaultTokenStorage()): string | null => {
  return tokenStorage?.getItem(REFRESH_TOKEN_STORAGE_KEY) ?? null
}

export const getAuthTokens = (tokenStorage = getDefaultTokenStorage()): AuthTokens | null => {
  const accessToken = getAccessToken(tokenStorage)
  const refreshToken = getRefreshToken(tokenStorage)

  if (!accessToken || !refreshToken) {
    return null
  }

  return {
    access_token: accessToken,
    refresh_token: refreshToken,
  }
}

export const setAuthTokens = (
  tokens: AuthTokens,
  tokenStorage = getDefaultTokenStorage(),
): void => {
  tokenStorage?.setItem(ACCESS_TOKEN_STORAGE_KEY, tokens.access_token)
  tokenStorage?.setItem(REFRESH_TOKEN_STORAGE_KEY, tokens.refresh_token)
}

export const setAuthSession = (
  session: AuthSession,
  tokenStorage = getDefaultTokenStorage(),
): void => {
  setAuthTokens(session, tokenStorage)
}

export const clearAuthTokens = (tokenStorage = getDefaultTokenStorage()): void => {
  tokenStorage?.removeItem(ACCESS_TOKEN_STORAGE_KEY)
  tokenStorage?.removeItem(REFRESH_TOKEN_STORAGE_KEY)
}

export const readStoredApiBaseUrl = (tokenStorage = getDefaultTokenStorage()): string | null => {
  return tokenStorage?.getItem(API_BASE_URL_STORAGE_KEY) ?? null
}

export const writeStoredApiBaseUrl = (
  baseUrl: string,
  tokenStorage = getDefaultTokenStorage(),
): void => {
  tokenStorage?.setItem(API_BASE_URL_STORAGE_KEY, baseUrl)
}

export const clearStoredApiBaseUrl = (tokenStorage = getDefaultTokenStorage()): void => {
  tokenStorage?.removeItem(API_BASE_URL_STORAGE_KEY)
}

type UnauthorizedHandler = () => void
let _onUnauthorized: UnauthorizedHandler | null = null

export const setUnauthorizedHandler = (handler: UnauthorizedHandler | null): void => {
  _onUnauthorized = handler
}

export const createAuthMiddleware = (
  readAccessToken = resolveAccessTokenReader(getDefaultTokenStorage()),
): Middleware => ({
  onRequest({ request, schemaPath }) {
    if (AUTH_TOKEN_SKIP_PATHS.has(schemaPath)) {
      return undefined
    }

    const accessToken = readAccessToken()

    if (!accessToken || request.headers.has('Authorization')) {
      return undefined
    }

    const headers = new Headers(request.headers)
    headers.set('Authorization', `Bearer ${accessToken}`)

    return new Request(request, { headers })
  },
  onResponse({ response, schemaPath }) {
    if (response.status === 401 && !AUTH_TOKEN_SKIP_PATHS.has(schemaPath)) {
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

const buildRequestFailureError = (
  fallbackMessage: string,
  error?: unknown,
  response?: Response,
): Error => {
  if (error) {
    return error as Error
  }
  const status = response?.status
  const reason = status
    ? t('api.errors.serverReturned', { status })
    : t('api.errors.requestNotSent')
  return new Error(`${fallbackMessage}（${reason}）`)
}

export const loginWithPassword = async (
  credentials: ApiSchemas['LoginRequest'],
  client = apiClient,
): Promise<AuthSession> => {
  const { data, error, response } = await client.POST('/auth/login', {
    body: credentials,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.loginFailed'), error, response)
  }

  setAuthSession(data)

  return data
}

export const registerAndLogin = async (
  payload: ApiSchemas['RegisterRequest'],
  client = apiClient,
): Promise<AuthSession> => {
  const { data, error, response } = await client.POST('/auth/register', {
    body: payload,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.registerFailed'), error, response)
  }

  setAuthSession(data)

  return data
}

export const refreshAuthSession = async (
  refreshToken = getRefreshToken(),
  client = apiClient,
): Promise<AuthSession> => {
  if (!refreshToken) {
    throw new Error('Refresh token is missing.')
  }

  const { data, error, response } = await client.POST('/auth/refresh', {
    body: {
      refresh_token: refreshToken,
    },
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.refreshSessionFailed'), error, response)
  }

  setAuthSession(data)

  return data
}

export const logout = async (
  refreshToken = getRefreshToken(),
  client = apiClient,
): Promise<void> => {
  try {
    if (refreshToken) {
      const { error } = await client.POST('/auth/logout', {
        body: {
          refresh_token: refreshToken,
        },
      })

      if (error) {
        throw error
      }
    }
  } finally {
    clearAuthTokens()
  }
}

export const fetchCurrentUser = async (client = apiClient): Promise<ApiSchemas['User']> => {
  const { data, error, response } = await client.GET('/users/me')

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchCurrentUserFailed'), error, response)
  }

  return data
}

export const fetchStatsSummary = async (client = apiClient): Promise<ApiSchemas['UsageStats']> => {
  const { data, error, response } = await client.GET('/stats/summary')

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchStatsFailed'), error, response)
  }

  return data
}

export const fetchProjects = async (
  client = apiClient,
): Promise<ApiSchemas['ProjectListResponse']> => {
  const { data, error, response } = await client.GET('/projects')

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchProjectsFailed'), error, response)
  }

  return data
}

export const createProject = async (
  payload: ApiSchemas['CreateProjectRequest'],
  client = apiClient,
): Promise<ApiSchemas['Project']> => {
  const { data, error, response } = await client.POST('/projects', {
    body: payload,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.createProjectFailed'), error, response)
  }

  return data
}

export const updateProject = async (
  projectId: number,
  payload: ApiSchemas['UpdateProjectRequest'],
  client = apiClient,
): Promise<ApiSchemas['Project']> => {
  const { data, error, response } = await client.PUT('/projects/{projectId}', {
    params: { path: { projectId } },
    body: payload,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.updateProjectFailed'), error, response)
  }

  return data
}

export const deleteProject = async (projectId: number, client = apiClient): Promise<void> => {
  const { error, response } = await client.DELETE('/projects/{projectId}', {
    params: { path: { projectId } },
  })

  if (error || response.status !== 204) {
    throw buildRequestFailureError(t('api.errors.deleteProjectFailed'), error, response)
  }
}

export const fetchOrganizations = async (
  client = apiClient,
): Promise<ApiSchemas['OrganizationListResponse']> => {
  const { data, error, response } = await client.GET('/orgs')

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchOrganizationsFailed'), error, response)
  }

  return data
}

export const fetchActivity = async (
  params?: { cursor?: string; limit?: number },
  client = apiClient,
): Promise<ApiSchemas['ActivityListResponse']> => {
  const { data, error, response } = await client.GET('/activity', {
    params: { query: params },
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchActivityFailed'), error, response)
  }

  return data
}
