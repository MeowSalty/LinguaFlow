import { afterEach, expect, it, vi } from 'vitest'
import {
  clearAuthTokens,
  getAccessToken,
  getRefreshToken,
  setAuthTokens,
  authTokenStorageKeys,
} from '@/api/token-storage'

afterEach(() => {
  clearAuthTokens()
  vi.unstubAllGlobals()
})

it('preserves in-memory login when storage is readable but rejects writes', () => {
  vi.stubGlobal('window', {
    localStorage: {
      getItem: () => null,
      setItem: () => {
        throw new Error('Quota exceeded')
      },
      removeItem: () => {},
    },
  })
  setAuthTokens({ access_token: 'memory-access', refresh_token: 'memory-refresh' })
  expect(getAccessToken()).toBe('memory-access')
  expect(getRefreshToken()).toBe('memory-refresh')
  clearAuthTokens()
  expect(getAccessToken()).toBeNull()
})

it('does not resurrect memory copies after another tab removes persistent credentials', () => {
  const values = new Map<string, string>()
  vi.stubGlobal('window', {
    localStorage: {
      getItem: (key: string) => values.get(key) ?? null,
      setItem: (key: string, value: string) => values.set(key, value),
      removeItem: (key: string) => values.delete(key),
    },
  })
  setAuthTokens({ access_token: 'persisted-access', refresh_token: 'persisted-refresh' })
  values.delete(authTokenStorageKeys.accessToken)
  values.delete(authTokenStorageKeys.refreshToken)
  expect(getAccessToken()).toBeNull()
  expect(getRefreshToken()).toBeNull()
})
