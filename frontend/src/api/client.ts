import createClient, { type Client, type ClientOptions, type Middleware } from 'openapi-fetch'

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
  /** API 根地址；不传时默认使用 `/api`。 */
  baseUrl?: string
  /** 自定义 Token 存储，默认使用 `window.localStorage`。 */
  tokenStorage?: TokenStorage
  /** 自定义 Access Token 读取逻辑；优先级高于 `tokenStorage`。 */
  getAccessToken?: () => string | null | undefined
}

const DEFAULT_API_BASE_URL = '/api'
const ACCESS_TOKEN_STORAGE_KEY = 'linguaflow.access_token'
const REFRESH_TOKEN_STORAGE_KEY = 'linguaflow.refresh_token'
const AUTH_TOKEN_SKIP_PATHS = new Set(['/auth/register', '/auth/login', '/auth/refresh'])

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
})

export const createApiClient = (options: ApiClientOptions = {}): ApiClient => {
  const { baseUrl, tokenStorage = getDefaultTokenStorage(), getAccessToken, ...clientOptions } = options
  const readAccessToken = resolveAccessTokenReader(tokenStorage, getAccessToken)
  const client = createClient<ApiPaths>({
    ...clientOptions,
    baseUrl: resolveApiBaseUrl(baseUrl),
  })

  client.use(createAuthMiddleware(readAccessToken))

  return client
}

export const apiClient = createApiClient()

export const loginWithPassword = async (
  credentials: ApiSchemas['LoginRequest'],
  client = apiClient,
): Promise<AuthSession> => {
  const { data, error } = await client.POST('/auth/login', {
    body: credentials,
  })

  if (error) {
    throw error
  }

  setAuthSession(data)

  return data
}

export const registerAndLogin = async (
  payload: ApiSchemas['RegisterRequest'],
  client = apiClient,
): Promise<AuthSession> => {
  const { data, error } = await client.POST('/auth/register', {
    body: payload,
  })

  if (error) {
    throw error
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

  const { data, error } = await client.POST('/auth/refresh', {
    body: {
      refresh_token: refreshToken,
    },
  })

  if (error) {
    throw error
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
