import { describe, expect, it, vi } from 'vitest'
import type { ApiClient } from '../../src/api/client-core'
import {
  archiveAuthConnection,
  AuthConnectionError,
  checkAuthConnection,
  configureAuthConnection,
  createAuthConnection,
  disableAuthConnection,
  enableAuthConnection,
  fetchAuthConnectionAttributes,
  fetchAuthConnectionImpact,
  previewAuthConnectionAttributes,
  restoreAuthConnection,
  uploadAuthConnectionLogo,
  type AuthConnection,
  type AuthConnectionSnapshot,
} from '../../src/api/auth-connections'
import {
  clone,
  connectionChanges,
  connectionForm,
  connectionTestCandidate,
  connectionUpdate,
  hasUnsafeRuleNumbers,
  newConnectionSnapshot,
  parsePreviewSources,
  requiresSignupMaintenancePath,
} from '../../src/components/admin/authentication/auth-connection-model'

vi.mock('@/i18n', () => ({ t: (key: string) => key }))
vi.mock('@/api/client-core', () => ({ apiClient: {} }))

const rules = { mode: 'unrestricted' as const }
const record = (overrides: Partial<AuthConnection> = {}): AuthConnection => {
  const configuration: AuthConnectionSnapshot = {
    ...newConnectionSnapshot(),
    login_policy_revision: 4,
    display_name: 'Company SSO',
    description: 'Work identity',
    oidc: {
      issuer: 'https://identity.example.com',
      client_id: 'client-id',
      scopes: ['openid', 'profile'],
      userinfo_enabled: true,
      client_auth_method: 'client_secret_basic',
    },
    policy: {
      login_enabled: true,
      signup_enabled: false,
      link_enabled: false,
      login_rules: rules,
      signup_rules: rules,
      link_rules: rules,
      signup: { email_requirement: 'optional', password_requirement: 'none' },
    },
    has_client_secret: true,
  }
  return {
    id: 'company',
    kind: 'oidc',
    aggregate_revision: 9,
    state: 'configuring',
    archived: false,
    configuration,
    configuration_complete: true,
    capabilities: { can_login: false, can_signup: false, can_link: false, can_test: true },
    validation: { fresh_supported: false, tested_at: null },
    setup: {
      callback_url: 'https://app.example/auth/callback/company',
      completion_urls: ['https://app.example/auth/complete'],
      configuration_complete: true,
      issues: [],
      protocol: {
        required_fields: ['issuer', 'client_id'],
        client_auth_methods: ['client_secret_basic', 'client_secret_post'],
        scope_guidance: 'openid profile',
      },
    },
    ...overrides,
  }
}
const ok = <T>(data: T) => ({ data, response: new Response(null, { status: 200 }) })

describe('authentication connection API contract', () => {
  it('preserves absent protocol groups when cloning saved configurations and pending edits', () => {
    const saved = record()
    delete saved.configuration.oauth2
    const pending = connectionForm(saved)
    expect(clone(pending.oauth2)).toBeUndefined()
    expect(clone(null)).toBeNull()
    const copy = clone(pending)
    copy.oidc!.issuer = 'https://changed.example'
    expect(pending.oidc!.issuer).toBe('https://identity.example.com')
  })
  it('creates a minimally configured connection with one configuration and setup shape', async () => {
    const created = record({ configuration_complete: false, state: 'configuring' })
    const POST = vi.fn().mockResolvedValue(ok(created))
    const body = { kind: 'oidc' as const, display_name: 'Company' }

    await expect(createAuthConnection(body, { POST } as unknown as ApiClient)).resolves.toEqual(
      created,
    )

    expect(POST).toHaveBeenCalledWith('/admin/auth-connections', { body })
    expect(created).toHaveProperty('configuration')
    expect(created).toHaveProperty('state', 'configuring')
    expect(created).toHaveProperty('setup.callback_url')
    expect(created).not.toHaveProperty('draft')
    expect(created).not.toHaveProperty('published')
  })

  it('uses aggregate revision for attribute reads and logo writes', async () => {
    const GET = vi
      .fn()
      .mockResolvedValue(ok({ revision: 9, purpose: 'login', fields: [], operators: {} }))
    const POST = vi.fn().mockResolvedValue(ok(record()))
    const client = { GET, POST } as unknown as ApiClient
    await fetchAuthConnectionAttributes('company', 9, 'login', client)
    const file = new File([new Uint8Array([137, 80, 78, 71])], 'logo.png', { type: 'image/png' })
    await uploadAuthConnectionLogo('company', 9, file, client)

    expect(GET.mock.calls[0]?.[1].params.query).toEqual({ expected_revision: 9, purpose: 'login' })
    expect(POST.mock.calls[0]?.[1].params.query).toEqual({ expected_revision: 9 })
    expect(POST.mock.calls[0]?.[1].bodySerializer()).toBe(file)
  })

  it('rejects unsupported, empty and oversized logos before sending', async () => {
    const POST = vi.fn()
    const client = { POST } as unknown as ApiClient
    for (const file of [
      new File(['<svg />'], 'logo.svg', { type: 'image/svg+xml' }),
      new File([], 'logo.png', { type: 'image/png' }),
      new File([new Uint8Array(256 * 1024 + 1)], 'logo.png', { type: 'image/png' }),
    ])
      await expect(uploadAuthConnectionLogo('company', 9, file, client)).rejects.toThrow()
    expect(POST).not.toHaveBeenCalled()
  })

  it('preserves a partial presentation save while protocol edits remain unsaved', () => {
    const saved = record()
    const form = connectionForm(saved)
    form.display_name = '  Company SSO renamed  '
    form.description = 'Updated description'
    form.oidc!.issuer = 'not-yet-valid'
    form.policy.login_enabled = false

    expect(connectionChanges(saved, form, 'keep', 'ignored', 'presentation')).toEqual({
      display_name: 'Company SSO renamed',
      description: 'Updated description',
    })
    expect(connectionUpdate(saved, form, 'keep', '', 'presentation')).toEqual({
      expected_revision: 9,
      display_name: 'Company SSO renamed',
      description: 'Updated description',
    })
  })

  it('builds candidate diagnostics from configuration changes without presentation fields', () => {
    const candidate = connectionTestCandidate(record(), {
      display_name: 'Renamed',
      oidc: { issuer: 'https://candidate.example', client_id: 'candidate' },
      client_secret: 'candidate-secret',
    })
    expect(candidate.protocol).toEqual({
      issuer: 'https://candidate.example',
      client_id: 'candidate',
    })
    expect(candidate.secret).toBe('candidate-secret')
    expect(candidate).not.toHaveProperty('display_name')
  })

  it('sends candidate, check and attribute-preview requests in the final contract shape', async () => {
    const body = {
      expected_revision: 9,
      changes: { oidc: { issuer: 'https://candidate.example' } },
    }
    const attributesBody = { ...body, purpose: 'signup' as const }
    const POST = vi
      .fn()
      .mockResolvedValue(ok({ revision: 9, purpose: 'signup', fields: [], operators: {} }))
    const client = { POST } as unknown as ApiClient
    await previewAuthConnectionAttributes('company', attributesBody, client)
    POST.mockResolvedValueOnce(
      ok({
        revision: 9,
        rules: { login: {}, signup: {}, link: {} },
        valid: true,
        fresh_supported: true,
        issues: [],
      }),
    )
    await checkAuthConnection('company', body, client)
    POST.mockResolvedValueOnce(
      ok({
        affected_users: [],
        blocked_users: [],
        affected_sessions: 0,
        can_save: true,
      }),
    )
    await fetchAuthConnectionImpact('company', body, client)

    expect(
      POST.mock.calls.map(([path, options]) => [path, options.params.path, options.body]),
    ).toEqual([
      [
        '/admin/auth-connections/{connectionId}/attributes',
        { connectionId: 'company' },
        attributesBody,
      ],
      ['/admin/auth-connections/{connectionId}/check', { connectionId: 'company' }, body],
      ['/admin/auth-connections/{connectionId}/impact', { connectionId: 'company' }, body],
    ])
  })

  it('uses configure, enable and disable actions with the same revision-bound evidence', async () => {
    const body = { expected_revision: 9, test_id: 'test-1', verifier: 'memory-only-verifier' }
    const POST = vi.fn().mockResolvedValue(ok(record()))
    const client = { POST } as unknown as ApiClient
    await configureAuthConnection('company', body, client)
    await enableAuthConnection('company', body, client)
    await disableAuthConnection('company', { expected_revision: 10 }, client)

    expect(POST.mock.calls.map(([path, options]) => [path, options.body])).toEqual([
      ['/admin/auth-connections/{connectionId}/configure', body],
      ['/admin/auth-connections/{connectionId}/enable', body],
      ['/admin/auth-connections/{connectionId}/disable', { expected_revision: 10 }],
    ])
  })

  it('archives and restores by aggregate revision without a publication endpoint', async () => {
    const POST = vi.fn().mockResolvedValue(ok(record()))
    const client = { POST } as unknown as ApiClient
    await archiveAuthConnection('company', 9, client)
    await restoreAuthConnection('company', 10, client)
    expect(POST.mock.calls.map(([path, options]) => [path, options.body])).toEqual([
      ['/admin/auth-connections/{connectionId}/archive', { expected_revision: 9 }],
      ['/admin/auth-connections/{connectionId}/restore', { expected_revision: 10 }],
    ])
    expect(POST.mock.calls.some(([path]) => String(path).endsWith('/publish'))).toBe(false)
  })

  it('retains structured conflict code, issues and impact without retrying', async () => {
    const problem = {
      type: 'urn:linguaflow:auth-connection-fallback-required',
      title: 'Fallback required',
      status: 409,
      detail: 'Another administrator sign-in must remain available.',
      issues: [
        { code: 'fallback_missing', field: 'policy.login_enabled', message: 'Keep a fallback.' },
      ],
      impact: {
        affected_users: [3],
        blocked_users: [3],
        affected_sessions: 2,
        can_save: false,
        remediation: 'Enable password login first.',
      },
    }
    const POST = vi.fn().mockResolvedValue({
      error: problem,
      response: new Response(null, { status: 409 }),
    })
    const failure = await configureAuthConnection('company', { expected_revision: 9 }, {
      POST,
    } as unknown as ApiClient).catch((cause) => cause)

    expect(failure).toBeInstanceOf(AuthConnectionError)
    expect(failure).toMatchObject({
      status: 409,
      code: 'fallback-required',
      problem,
      message: problem.detail,
    })
    expect(POST).toHaveBeenCalledTimes(1)
  })

  it('maps a maintenance-path issue to the actionable signup message', async () => {
    const problem = {
      type: 'urn:linguaflow:auth-connection-configuration-incomplete',
      title: 'auth_connection_configuration-incomplete',
      status: 409,
      issues: [
        {
          code: 'maintenance-path-required',
          field: 'policy.signup.password_requirement',
          message: 'Registration requires a maintenance path.',
        },
      ],
    }
    const POST = vi.fn().mockResolvedValue({
      error: problem,
      response: new Response(null, { status: 409 }),
    })

    const failure = await configureAuthConnection('company', { expected_revision: 9 }, {
      POST,
    } as unknown as ApiClient).catch((cause) => cause)

    expect(failure).toBeInstanceOf(AuthConnectionError)
    expect(failure).toMatchObject({
      code: 'configuration-incomplete',
      message: 'authAdmin.signupPasswordMaintenanceRequired',
      problem,
    })
  })
})

describe('authentication connection model', () => {
  it('uses one configuration snapshot and protocol-specific blank defaults', () => {
    const defaults = newConnectionSnapshot()
    expect(defaults.login_policy_revision).toBe(1)
    expect(defaults.policy).not.toHaveProperty('enabled')
    expect(defaults.attribute_mappings).toEqual([])
    expect(connectionForm(record()).display_name).toBe('Company SSO')
    expect(defaults.signup_policy_revision).toBe(1)
    expect(defaults.link_policy_revision).toBe(1)
    expect(connectionForm(record({ kind: 'generic_oauth2' })).oidc).toBeUndefined()
  })

  it('preserves safe rule integers and refuses lossy JSON numbers', () => {
    const saved = record()
    const form = clone(saved.configuration)
    form.policy.login_rules = {
      mode: 'conditions',
      match: 'all',
      conditions: [
        { field_id: 'id', operator: 'eq', value: Number.MAX_SAFE_INTEGER },
        { field_id: 'score', operator: 'gt', value: 0.125 },
      ],
    }
    expect(hasUnsafeRuleNumbers(form)).toBe(false)
    expect(connectionUpdate(saved, form, 'keep', '').policy).toEqual(form.policy)
    form.policy.login_rules.conditions[0]!.value = Number.MAX_SAFE_INTEGER + 1
    expect(hasUnsafeRuleNumbers(form)).toBe(true)
    expect(() => connectionUpdate(saved, form, 'keep', '')).toThrow('Unsafe rule number')
  })

  it('keeps secret operations explicit and never adds protocol data to presentation saves', () => {
    const saved = record()
    const form = connectionForm(saved)
    expect(connectionUpdate(saved, form, 'keep', '')).not.toHaveProperty('client_secret')
    expect(connectionUpdate(saved, form, 'clear', '')).toHaveProperty('client_secret', null)
    expect(connectionUpdate(saved, form, 'replace', 'secret')).toHaveProperty(
      'client_secret',
      'secret',
    )
    expect(() => connectionUpdate(saved, form, 'replace', '')).toThrow('Empty replacement secret')
  })

  it('accepts only supported rule preview sources', () => {
    expect(parsePreviewSources('{"oidc_id_token":{"team":"infra"}}')).toEqual({
      oidc_id_token: { team: 'infra' },
    })
    for (const input of ['null', '[]', '{"oauth2_profile":{}}', '{"oidc_userinfo":[]}'])
      expect(() => parsePreviewSources(input)).toThrow()
  })

  it('requires a maintenance path only for active non-required signup passwords', () => {
    expect(requiresSignupMaintenancePath('generic_oauth2', true, 'none', false)).toBe(true)
    expect(requiresSignupMaintenancePath('generic_oauth2', true, 'optional', true)).toBe(true)
    expect(requiresSignupMaintenancePath('oidc', true, 'none', false)).toBe(true)
    expect(requiresSignupMaintenancePath('oidc', true, 'optional', true)).toBe(false)
    expect(requiresSignupMaintenancePath('oidc', false, 'none', false)).toBe(false)
    expect(requiresSignupMaintenancePath('oidc', true, 'required', false)).toBe(false)
  })
})
