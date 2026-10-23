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
const AUTH_EPOCH_STORAGE_KEY = 'linguaflow.auth_epoch'
const REFRESH_ATTEMPT_STORAGE_KEY = 'linguaflow.refresh_attempt'
export const authTokenStorageKeys = {
  accessToken: ACCESS_TOKEN_STORAGE_KEY,
  refreshToken: REFRESH_TOKEN_STORAGE_KEY,
  apiBaseUrl: API_BASE_URL_STORAGE_KEY,
  authEpoch: AUTH_EPOCH_STORAGE_KEY,
  refreshAttempt: REFRESH_ATTEMPT_STORAGE_KEY,
} as const

const memory = new Map<string, string>()
const volatileKeys = new Map<string, Storage>()
const safeStorage: TokenStorage = {
  getItem(key) {
    try {
      const storage = window.localStorage
      if (volatileKeys.get(key) === storage) return memory.get(key) ?? null
      volatileKeys.delete(key)
      // A readable storage is authoritative: a null here means the value is gone
      // (for example another tab logged out), never "resurrect the memory copy".
      return storage.getItem(key)
    } catch {
      return memory.get(key) ?? null
    }
  },
  setItem(key, value) {
    memory.set(key, value)
    let storage: Storage | undefined
    try {
      storage = window.localStorage
      storage.setItem(key, value)
      volatileKeys.delete(key)
    } catch {
      if (storage) volatileKeys.set(key, storage)
      else volatileKeys.delete(key)
    }
  },
  removeItem(key) {
    memory.delete(key)
    let storage: Storage | undefined
    try {
      storage = window.localStorage
      storage.removeItem(key)
      volatileKeys.delete(key)
    } catch {
      if (storage) volatileKeys.set(key, storage)
      else volatileKeys.delete(key)
    }
  },
}
export const getDefaultTokenStorage = (): TokenStorage => safeStorage

// Logout hides credentials in this realm while another tab finishes rotating
// the shared pair. The coordinator alone reads through this mask under its lock.
let authTokensHidden = false
export const setAuthTokensHidden = (hidden: boolean): void => {
  authTokensHidden = hidden
}

export const readStoredAuthTokens = (
  tokenStorage = getDefaultTokenStorage(),
): AuthTokens | null => {
  const access_token = tokenStorage.getItem(ACCESS_TOKEN_STORAGE_KEY)
  const refresh_token = tokenStorage.getItem(REFRESH_TOKEN_STORAGE_KEY)
  return access_token && refresh_token ? { access_token, refresh_token } : null
}

const createAuthEpoch = (): string => {
  const cryptoApi = globalThis.crypto
  if (cryptoApi && typeof cryptoApi.randomUUID === 'function') return cryptoApi.randomUUID()
  const bytes = new Uint8Array(16)
  if (cryptoApi && typeof cryptoApi.getRandomValues === 'function') {
    cryptoApi.getRandomValues(bytes)
  } else {
    for (let index = 0; index < bytes.length; index += 1)
      bytes[index] = Math.floor(Math.random() * 256)
  }
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, '0')).join('')
}

let cachedAuthEpoch: string | null = null

/**
 * Identity of the current login context. It is shared across tabs and survives
 * reloads (the external-browser flow compares it across a full-page redirect),
 * rotates only when credentials are cleared, and stays unchanged when an
 * ordinary refresh rotates the tokens.
 */
export const getAuthEpoch = (): string => {
  const stored = safeStorage.getItem(AUTH_EPOCH_STORAGE_KEY)
  if (stored) {
    cachedAuthEpoch = stored
    return stored
  }
  const epoch = cachedAuthEpoch ?? createAuthEpoch()
  cachedAuthEpoch = epoch
  safeStorage.setItem(AUTH_EPOCH_STORAGE_KEY, epoch)
  return epoch
}

/** Starts a new login context. Used when credentials are cleared, not on rotation. */
export const rotateAuthEpoch = (): string => {
  const epoch = createAuthEpoch()
  cachedAuthEpoch = epoch
  safeStorage.setItem(AUTH_EPOCH_STORAGE_KEY, epoch)
  return epoch
}

export const getAccessToken = (tokenStorage = getDefaultTokenStorage()): string | null => {
  if (authTokensHidden) return null
  return tokenStorage?.getItem(ACCESS_TOKEN_STORAGE_KEY) ?? null
}

export const getRefreshToken = (tokenStorage = getDefaultTokenStorage()): string | null => {
  if (authTokensHidden) return null
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
  tokenStorage?.removeItem(REFRESH_ATTEMPT_STORAGE_KEY)
  // Clearing credentials ends the login context so stale external flows and the
  // other tabs' assumptions are invalidated.
  rotateAuthEpoch()
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

/**
 * Durable record of a refresh token that was sent to the server but whose
 * outcome is unknown. It is shared across tabs and survives reloads so a one-use
 * token is never automatically consumed twice.
 */
export interface RefreshAttempt {
  token: string
  epoch: string
  baseUrl: string
}

export const readRefreshAttempt = (
  tokenStorage = getDefaultTokenStorage(),
): RefreshAttempt | null => {
  const raw = tokenStorage?.getItem(REFRESH_ATTEMPT_STORAGE_KEY)
  if (!raw) return null
  try {
    const parsed: unknown = JSON.parse(raw)
    if (!parsed || typeof parsed !== 'object') return null
    const record = parsed as Partial<RefreshAttempt>
    return typeof record.token === 'string' &&
      typeof record.epoch === 'string' &&
      typeof record.baseUrl === 'string'
      ? { token: record.token, epoch: record.epoch, baseUrl: record.baseUrl }
      : null
  } catch {
    return null
  }
}

export const writeRefreshAttempt = (
  attempt: RefreshAttempt,
  tokenStorage = getDefaultTokenStorage(),
): void => {
  tokenStorage?.setItem(REFRESH_ATTEMPT_STORAGE_KEY, JSON.stringify(attempt))
}

export const clearRefreshAttempt = (tokenStorage = getDefaultTokenStorage()): void => {
  tokenStorage?.removeItem(REFRESH_ATTEMPT_STORAGE_KEY)
}
