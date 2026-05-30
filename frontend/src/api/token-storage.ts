import type { components } from './types'

export type AuthSession = components['schemas']['AuthSession']
export type AuthTokens = Pick<AuthSession, 'access_token' | 'refresh_token'>

export interface TokenStorage {
  getItem(key: string): string | null
  setItem(key: string, value: string): void
  removeItem(key: string): void
}

const ACCESS_TOKEN_STORAGE_KEY = 'linguaflow.access_token'
const REFRESH_TOKEN_STORAGE_KEY = 'linguaflow.refresh_token'
const API_BASE_URL_STORAGE_KEY = 'linguaflow.api_base_url'

export const authTokenStorageKeys = {
  accessToken: ACCESS_TOKEN_STORAGE_KEY,
  refreshToken: REFRESH_TOKEN_STORAGE_KEY,
  apiBaseUrl: API_BASE_URL_STORAGE_KEY,
} as const

export const getDefaultTokenStorage = (): TokenStorage | undefined => {
  if (typeof window === 'undefined') {
    return undefined
  }

  return window.localStorage
}

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
