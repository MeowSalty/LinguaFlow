import { describe, expect, it } from 'vitest'
import {
  assertSessionCurrent,
  captureSession,
  changeSessionContext,
  getSessionScope,
  isSessionCurrent,
  normalizeApiBaseUrl,
  onSessionChange,
  StaleSessionError,
} from '../session-context'

describe('session identity isolation', () => {
  it('normalizes equivalent service addresses without resetting the session', () => {
    changeSessionContext('https://example.com/api/v1/', 42, true)
    const before = captureSession()
    changeSessionContext('https://example.com/api/v1', 42)
    expect(isSessionCurrent(before)).toBe(true)
    expect(getSessionScope()).toBe('https://example.com/api/v1|42')
    expect(normalizeApiBaseUrl(' https://example.com/api/v1/// ')).toBe(
      'https://example.com/api/v1',
    )
  })

  it.each([
    ['account', 'https://one.example/api/v1', 2],
    ['service', 'https://two.example/api/v1', 1],
  ] as const)('invalidates in-flight requests when the %s changes', (_kind, base, id) => {
    changeSessionContext('https://one.example/api/v1', 1, true)
    const before = captureSession()
    let observedCurrent = true
    const remove = onSessionChange(() => {
      observedCurrent = isSessionCurrent(before)
    })
    changeSessionContext(base, id)
    expect(before.signal.aborted).toBe(true)
    expect(observedCurrent).toBe(false)
    expect(() => assertSessionCurrent(before)).toThrow(StaleSessionError)
    expect(isSessionCurrent(captureSession())).toBe(true)
    remove()
  })

  it('forces logout invalidation even when no user was established', () => {
    changeSessionContext('/api/v1', null, true)
    const before = captureSession()
    changeSessionContext('/api/v1', null, true)
    expect(isSessionCurrent(before)).toBe(false)
    expect(getSessionScope()).toBeNull()
  })
})
