import { describe, expect, it, vi } from 'vitest'
import type { ApiClient, ApiSchemas } from '../../src/api/client-core'
import { fetchAuthCapabilities } from '../../src/api/auth-capabilities'
import {
  isAuthCapabilities,
  isExternalConfirmation,
  isLoginMethods,
  isReauthenticationProof,
} from '../../src/api/auth-contract'
import { ApiError } from '../../src/api/utils'
import { authenticationFailure, canRetryExternalConfirmation } from '../../src/auth/errors'
import { normalizeEmail, validateEmail } from '../../src/utils/email'

vi.mock('@/i18n', () => ({ t: (key: string) => key }))
vi.mock('@/api/client-core', () => ({ apiClient: {} }))

export const b5Capabilities = (): ApiSchemas['AuthCapabilities'] => ({
  mode: 'server',
  local_account: {
    can_login: true,
    can_signup: true,
    can_enroll_password: true,
    password_login_enabled: true,
    username_login_enabled: true,
    email_login_enabled: false,
    password_enrollment_enabled: true,
    signup: { email_requirement: 'optional', password_requirement: 'required' },
    signup_requirements: ['local_password'],
  },
  connections: [
    {
      connection_id: 'company',
      display_name: 'Company',
      description: '',
      display_order: 0,
      can_login: false,
      can_signup: true,
      signup: { email_requirement: 'required', password_requirement: 'none' },
      signup_requirements: ['email'],
    },
  ],
  email_verification_enabled: false,
  password_reset_enabled: false,
})

const confirmation = () => ({
  kind: 'signup_confirmation',
  confirmation_token: 'short-lived',
  transaction_id: 'tx',
  expires_at: new Date(Date.now() + 60_000).toISOString(),
  signup: { email_requirement: 'required', password_requirement: 'optional' },
  signup_requirements: ['email'],
  profile: { connection_id: 'company', connection_name: 'Company', external_display: 'Person' },
})

describe('B5 runtime authentication contract', () => {
  it('accepts effective login and signup capabilities independently', () => {
    expect(isAuthCapabilities(b5Capabilities())).toBe(true)
    const capabilities = b5Capabilities()
    capabilities.local_account.can_signup = false
    expect(isAuthCapabilities(capabilities)).toBe(true)
    expect(capabilities.connections[0]!.can_signup).toBe(true)
  })

  it.each(['local_account', 'connections', 'email_verification_enabled', 'password_reset_enabled'])(
    'fails closed if %s is absent',
    (key) => {
      const value: Record<string, unknown> = { ...b5Capabilities() }
      delete value[key]
      expect(isAuthCapabilities(value)).toBe(false)
    },
  )

  it('does not reconstruct missing requirements or accept unknown and inconsistent steps', () => {
    const value = b5Capabilities()
    const connection = value.connections[0]!
    expect(
      isAuthCapabilities({ ...value, connections: [{ ...connection, signup: undefined }] }),
    ).toBe(false)
    expect(
      isAuthCapabilities({
        ...value,
        connections: [{ ...connection, signup_requirements: ['local_password'] }],
      }),
    ).toBe(false)
    expect(
      isAuthCapabilities({
        ...value,
        connections: [{ ...connection, signup_requirements: ['future'] }],
      }),
    ).toBe(false)
    expect(
      isAuthCapabilities({
        ...value,
        local_account: { ...value.local_account, can_signup: 'true' },
      }),
    ).toBe(false)
  })

  it('rejects successful HTTP responses with an incomplete capability contract', async () => {
    const GET = vi
      .fn()
      .mockResolvedValue({ data: { mode: 'server' }, response: new Response(null) })
    await expect(fetchAuthCapabilities({ GET } as unknown as ApiClient)).rejects.toBeInstanceOf(
      ApiError,
    )
    GET.mockResolvedValue({ data: b5Capabilities(), response: new Response(null) })
    await expect(fetchAuthCapabilities({ GET } as unknown as ApiClient)).resolves.toEqual(
      b5Capabilities(),
    )
  })

  it('requires the signup snapshot while linking needs no registration requirements', () => {
    const value = confirmation()
    expect(isExternalConfirmation(value)).toBe(true)
    expect(isExternalConfirmation({ ...value, signup: undefined })).toBe(false)
    expect(
      isExternalConfirmation({
        ...value,
        signup: { email_requirement: 'required', password_requirement: 'future' },
      }),
    ).toBe(false)
    expect(
      isExternalConfirmation({ ...value, signup_requirements: ['email', 'local_password'] }),
    ).toBe(false)
    expect(
      isExternalConfirmation({
        ...value,
        kind: 'link_confirmation',
        signup: undefined,
        signup_requirements: [],
      }),
    ).toBe(true)
    expect(isExternalConfirmation({ ...value, kind: 'link_confirmation' })).toBe(false)
  })

  it('does not invent account-level password or binding capabilities', () => {
    const value: ApiSchemas['LoginMethodsResponse'] = {
      password: {
        is_set: true,
        can_login: false,
        can_set: false,
        can_delete: false,
        verified: false,
      },
      email: { address: null, verified: false, can_recover: false },
      bindings: [],
      available_connections: [],
    }
    expect(isLoginMethods(value)).toBe(true)
    expect(isLoginMethods({ ...value, password: { is_set: true } })).toBe(false)
    expect(
      isLoginMethods({ ...value, bindings: [{ binding_id: 2, can_reauthenticate: true }] }),
    ).toBe(false)
    expect(
      isLoginMethods({
        ...value,
        available_connections: [{ connection_id: 'id', display_name: 'Name' }],
      }),
    ).toBe(false)
  })

  it('rejects empty or incomplete password reauthentication proofs', () => {
    expect(
      isReauthenticationProof({
        reauthentication_proof: 'proof',
        expires_at: '2099-01-01T00:00:00Z',
      }),
    ).toBe(true)
    expect(
      isReauthenticationProof({ reauthentication_proof: '', expires_at: '2099-01-01T00:00:00Z' }),
    ).toBe(false)
    expect(isReauthenticationProof({ reauthentication_proof: 'proof' })).toBe(false)
  })
})

describe('shared registration email validation', () => {
  it('normalizes nonempty profile email and distinguishes optional from required blanks', () => {
    expect(normalizeEmail('  Person+Tag@EXAMPLE.COM  ')).toBe('person+tag@example.com')
    expect(validateEmail('  ')).toBeNull()
    expect(validateEmail('  ', true)).toBe('required')
    expect(validateEmail('  Person+Tag@EXAMPLE.COM  ', true)).toBeNull()
  })

  it.each([
    'Person <person@example.com>',
    'a@example.com,b@example.com',
    '"person"@example.com',
    'a@@example.com',
    'person',
    'a b@example.com',
  ])('rejects an unsupported address form: %s', (email) =>
    expect(validateEmail(email)).toBe('invalid'),
  )
})

describe('authentication failure recovery', () => {
  const problem = (status: number, code: string, title = code.replaceAll('-', '_')) =>
    new ApiError('untrusted detail', status, { type: `urn:linguaflow:${code}`, title })

  it('allows only explicitly unconsumed confirmation validation failures to retry', () => {
    expect(canRetryExternalConfirmation(problem(400, 'invalid-input'))).toBe(true)
    expect(canRetryExternalConfirmation(problem(409, 'conflict', 'conflict'))).toBe(true)
    for (const failure of [
      problem(409, 'authentication-conflict'),
      problem(409, 'future-conflict'),
      problem(400, 'external-flow-invalid'),
      problem(403, 'forbidden'),
      problem(503, 'unavailable'),
      new TypeError('Network failed'),
    ])
      expect(canRetryExternalConfirmation(failure)).toBe(false)
  })

  it('keeps username collision recovery limited to account creation actions', () => {
    const failure = problem(409, 'conflict', 'conflict')
    expect(authenticationFailure(failure, 'register')).toBe('username-conflict')
    expect(authenticationFailure(failure, 'security')).toBe('authentication-conflict')
    expect(authenticationFailure(problem(409, 'conflict', 'different'), 'external-complete')).toBe(
      'authentication-conflict',
    )
  })

  it('separates expired proofs, rule denials, authorization, and uncertain completion', () => {
    expect(authenticationFailure(problem(403, 'reauthentication-required'), 'security')).toBe(
      'reauthentication-required',
    )
    expect(
      authenticationFailure(problem(403, 'authentication-rule-denied'), 'external-complete'),
    ).toBe('rule-denied')
    expect(authenticationFailure(problem(403, 'future'), 'security')).toBe('forbidden')
    expect(authenticationFailure(problem(401, 'unauthorized'), 'security')).toBe('unauthorized')
    expect(authenticationFailure(problem(503, 'unavailable'), 'external-complete')).toBe(
      'result-unknown',
    )
    expect(authenticationFailure(problem(503, 'unavailable'), 'login')).toBe('unavailable')
    expect(authenticationFailure(new TypeError('Network failed'), 'external-complete')).toBe(
      'result-unknown',
    )
  })
})
