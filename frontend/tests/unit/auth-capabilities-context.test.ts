import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope } from 'vue'
import type { ApiSchemas } from '../../src/api/client-core'
import { captureSession, changeSessionContext } from '../../src/api/session-context'
import { useAuthCapabilities } from '../../src/composables/useAuthCapabilities'

const request = vi.hoisted(() => vi.fn())
vi.mock('@/api/auth-capabilities', () => ({ fetchAuthCapabilities: request }))
vi.mock('@/i18n', () => ({ t: (key: string) => key }))

const capabilities = (): ApiSchemas['AuthCapabilities'] => ({
  mode: 'server',
  local_account: {
    can_login: false,
    can_signup: false,
    can_enroll_password: false,
    password_login_enabled: false,
    password_enrollment_enabled: false,
    username_login_enabled: true,
    email_login_enabled: false,
    signup: { email_requirement: 'optional', password_requirement: 'required' },
    signup_requirements: ['local_password'],
  },
  connections: [
    {
      connection_id: 'login-only',
      display_name: 'Login',
      description: '',
      display_order: 2,
      can_login: true,
      can_signup: false,
      signup: { email_requirement: 'optional', password_requirement: 'none' },
      signup_requirements: [],
    },
    {
      connection_id: 'signup-only',
      display_name: 'Signup',
      description: '',
      display_order: 1,
      can_login: false,
      can_signup: true,
      signup: { email_requirement: 'optional', password_requirement: 'none' },
      signup_requirements: [],
    },
  ],
  email_verification_enabled: false,
  password_reset_enabled: false,
})
const deferred = <T>() => {
  let resolve!: (value: T) => void
  let reject!: (error: Error) => void
  const promise = new Promise<T>((res, rej) => {
    resolve = res
    reject = rej
  })
  return { promise, resolve, reject }
}
const settle = async () => {
  await Promise.resolve()
  await Promise.resolve()
}
let scope: ReturnType<typeof effectScope>
beforeEach(() => {
  request.mockReset()
  changeSessionContext('/api/v1', null, true)
  scope = effectScope()
})
afterEach(() => scope.stop())

describe('public capability context isolation', () => {
  it('filters and sorts login and signup independently without mutating server order', async () => {
    const value = capabilities()
    request.mockResolvedValue(value)
    const state = scope.run(() => useAuthCapabilities())!
    await settle()
    expect(state.loginConnections.value.map((connection) => connection.connection_id)).toEqual([
      'login-only',
    ])
    expect(state.signupConnections.value.map((connection) => connection.connection_id)).toEqual([
      'signup-only',
    ])
    expect(state.canSignup.value).toBe(true)
    expect(value.connections[0]!.connection_id).toBe('login-only')
  })

  it.each(['service', 'account', 'session'] as const)(
    'clears previous capabilities immediately on %s changes and discards their late response',
    async (kind) => {
      const first = deferred<ApiSchemas['AuthCapabilities']>()
      const second = deferred<ApiSchemas['AuthCapabilities']>()
      request.mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)
      const state = scope.run(() => useAuthCapabilities())!
      const old = captureSession()
      changeSessionContext(
        kind === 'service' ? 'https://second.example/api/v1' : old.baseUrl,
        kind === 'account' ? 7 : old.userId,
        true,
      )
      expect(state.capabilities.value).toBeNull()
      expect(state.canSignup.value).toBe(false)
      first.resolve(capabilities())
      await settle()
      expect(state.capabilities.value).toBeNull()
      const current = { ...capabilities(), connections: [] }
      second.resolve(current)
      await settle()
      expect(state.capabilities.value).toEqual(current)
      expect(state.loading.value).toBe(false)
    },
  )

  it('keeps failed discovery disabled and supports explicit retry', async () => {
    request
      .mockRejectedValueOnce(new Error('Incomplete contract'))
      .mockResolvedValueOnce(capabilities())
    const state = scope.run(() => useAuthCapabilities())!
    await settle()
    expect(state.error.value).toBe('Incomplete contract')
    expect(state.canSignup.value).toBe(false)
    await state.reload()
    expect(state.error.value).toBe('')
    expect(state.canSignup.value).toBe(true)
  })

  it('ignores late responses and unregisters session listeners after the view is disposed', async () => {
    const pending = deferred<ApiSchemas['AuthCapabilities']>()
    request.mockReturnValueOnce(pending.promise)
    const state = scope.run(() => useAuthCapabilities())!
    scope.stop()
    pending.resolve(capabilities())
    await settle()
    expect(state.capabilities.value).toBeNull()
    changeSessionContext('/api/v1', null, true)
    expect(request).toHaveBeenCalledTimes(1)
  })
})
