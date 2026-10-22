import { describe, expect, it, vi } from 'vitest'
import type { ApiClient, ApiSchemas } from '../../src/api/client-core'
import { AuthConnectionError } from '../../src/api/auth-connections'
import {
  fetchLocalAccount,
  updateLocalAccount,
  fetchLocalAccountAttributes,
  previewLocalAccount,
  fetchLocalAccountImpact,
  type LocalAccount,
} from '../../src/api/local-account'

vi.mock('@/i18n', () => ({ t: (key: string) => key }))
vi.mock('@/api/client-core', () => ({ apiClient: {} }))

const record = (): LocalAccount => ({
  revision: 7,
  policy: {
    password_login_enabled: true,
    username_login_enabled: true,
    email_login_enabled: false,
    password_enrollment_enabled: true,
    signup: { enabled: true, email_requirement: 'optional', rules: { mode: 'unrestricted' } },
  },
  capabilities: {
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
  issues: [],
})
const catalog = (purpose = 'signup'): ApiSchemas['AuthAttributeCatalog'] => ({
  revision: 7,
  purpose: purpose as 'signup',
  operators: { string: ['eq', 'in'] },
  fields: [
    {
      field_id: 'registration.email_domain',
      label: 'Registration email domain',
      type: 'string',
      source: 'registration',
      pointer: '/email_domain',
      user_editable: false,
      allowed_operators: ['eq', 'ne', 'in', 'not_in'],
      semantic_format: 'email_domain',
      normalization_version: 'idna-v1',
      derived_from: ['registration.email'],
    },
  ],
})
const ok = <T>(data: T) => ({ data, response: new Response(null) })

describe('fixed local account API', () => {
  it('reads the fixed resource and sends the entire policy with the local revision', async () => {
    const value = record()
    const GET = vi.fn().mockResolvedValue(ok(value))
    const PATCH = vi.fn().mockResolvedValue(ok(value))
    const client = { GET, PATCH } as unknown as ApiClient
    await expect(fetchLocalAccount(client)).resolves.toEqual(value)
    const body = { expected_revision: 7, policy: value.policy }
    await updateLocalAccount(body, client)
    expect(GET).toHaveBeenCalledWith('/admin/local-account')
    expect(PATCH).toHaveBeenCalledWith('/admin/local-account', { body })
    expect(PATCH.mock.calls[0]![1].body).not.toHaveProperty('changes')
  })

  it('requires a signup catalogue at the requested local revision and preserves readonly metadata', async () => {
    const GET = vi.fn().mockResolvedValue(ok(catalog()))
    const client = { GET } as unknown as ApiClient
    await expect(fetchLocalAccountAttributes(7, client)).resolves.toEqual(catalog())
    expect(GET.mock.calls[0]![1].params.query).toEqual({ expected_revision: 7 })
    for (const value of [
      catalog('login'),
      { ...catalog(), revision: 8 },
      { ...catalog(), fields: [{ ...catalog().fields[0], allowed_operators: undefined }] },
    ]) {
      GET.mockResolvedValueOnce(ok(value))
      await expect(fetchLocalAccountAttributes(7, client)).rejects.toBeInstanceOf(
        AuthConnectionError,
      )
    }
  })

  it('sends registration preview and complete candidate impact without connection fields', async () => {
    const POST = vi
      .fn()
      .mockResolvedValueOnce(
        ok({ revision: 7, simulated: true, attributes: {}, signup: { state: 'pass' } }),
      )
      .mockResolvedValueOnce(
        ok({ affected_users: [], blocked_users: [], affected_sessions: 0, can_save: true }),
      )
    const client = { POST } as unknown as ApiClient
    const preview = {
      expected_revision: 7,
      policy: record().policy,
      registration: { email: 'person@example.com' },
    }
    await previewLocalAccount(preview, client)
    const candidate = { expected_revision: 7, policy: record().policy }
    await fetchLocalAccountImpact(candidate, client)
    expect(POST.mock.calls.map(([path, options]) => [path, options.body])).toEqual([
      ['/admin/local-account/preview', preview],
      ['/admin/local-account/impact', candidate],
    ])
  })

  it('preserves dependency conflicts and unknown stable problem identifiers', async () => {
    const problem = {
      type: 'urn:linguaflow:auth-connection-signup-dependency-conflict',
      title: 'Dependency conflict',
      issues: [
        {
          code: 'maintenance-path-required',
          field: 'policy.signup.password_requirement',
          message: 'Keep verified maintenance.',
        },
      ],
      impact: { can_save: false, blocked_users: [3], affected_users: [3], affected_sessions: 1 },
    }
    const PATCH = vi
      .fn()
      .mockResolvedValue({ error: problem, response: new Response(null, { status: 409 }) })
    const failure = await updateLocalAccount({ expected_revision: 7, policy: record().policy }, {
      PATCH,
    } as unknown as ApiClient).catch((error: unknown) => error)
    expect(failure).toBeInstanceOf(AuthConnectionError)
    expect(failure).toMatchObject({ status: 409, code: 'signup-dependency-conflict', problem })
    expect(PATCH).toHaveBeenCalledTimes(1)
  })

  it('rejects a missing policy or capability instead of filling defaults', async () => {
    const GET = vi
      .fn()
      .mockResolvedValueOnce(ok({ ...record(), capabilities: undefined }))
      .mockResolvedValueOnce(ok({ ...record(), policy: { ...record().policy, signup: undefined } }))
    for (let attempt = 0; attempt < 2; attempt++)
      await expect(fetchLocalAccount({ GET } as unknown as ApiClient)).rejects.toBeInstanceOf(
        AuthConnectionError,
      )
  })
})
