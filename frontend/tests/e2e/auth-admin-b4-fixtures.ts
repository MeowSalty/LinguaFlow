import { expect, type Page, type Route } from '@playwright/test'
import type { AuthConnection } from '../../src/api/auth-connections'
import { json, mockApp } from './fixtures'

export const authDiagnostics = {
  login: { status: 'pass', retryable: false, conditions: [] },
  signup: { status: 'pass', retryable: false, conditions: [] },
  link: { status: 'pass', retryable: false, conditions: [] },
}

export type B4ConnectionCalls = {
  save: number
  start: number
  exchange: number
  cancel: number
  enable: number
  configure: number
}

export type B4ConnectionHandler = {
  start?: (body: Record<string, unknown>, record: AuthConnection) => Promise<unknown> | unknown
  enable?: (body: Record<string, unknown>) => Promise<unknown> | unknown
}

export function oidcConnection(patch: Partial<AuthConnection> = {}): AuthConnection {
  const configuration = {
    display_name: 'Company SSO',
    description: 'Production identity provider',
    display_order: 0,
    login_policy_revision: 1,
    has_client_secret: true,
    policy: {
      login_enabled: true,
      signup_enabled: false,
      link_enabled: false,
      login_rules: { mode: 'unrestricted' as const },
      signup_rules: { mode: 'unrestricted' as const },
      link_rules: { mode: 'unrestricted' as const },
    },
    oidc: {
      issuer: 'https://identity.example.com',
      client_id: 'client-id',
      scopes: ['openid', 'profile'],
      userinfo_enabled: true,
      client_auth_method: 'client_secret_basic' as const,
    },
    oauth2: undefined,
    attribute_mappings: [],
    evidence_max_age_seconds: 3600,
  }
  return {
    id: 'company',
    kind: 'oidc',
    aggregate_revision: 7,
    state: 'configuring',
    archived: false,
    configuration,
    configuration_complete: true,
    capabilities: { can_login: true, can_signup: false, can_link: false, can_test: true },
    validation: { fresh_supported: true, tested_at: null },
    setup: {
      callback_url: 'https://frontend.example/auth/callback/company',
      completion_urls: ['https://frontend.example/auth/complete'],
      configuration_complete: true,
      issues: [],
      protocol: {
        required_fields: [],
        client_auth_methods: ['client_secret_basic', 'client_secret_post'],
        scope_guidance: 'openid profile',
      },
    },
    ...patch,
  }
}

export function b4ConnectionCalls(): B4ConnectionCalls {
  return { save: 0, start: 0, exchange: 0, cancel: 0, enable: 0, configure: 0 }
}

export async function mockB4Connection(
  page: Page,
  recordInput: AuthConnection | (() => AuthConnection),
  calls: B4ConnectionCalls = b4ConnectionCalls(),
  handler: B4ConnectionHandler = {},
) {
  const getRecord = () => (typeof recordInput === 'function' ? recordInput() : recordInput)
  await mockApp(page)
  await page.route('**/api/v1/admin/auth-connections', (route) =>
    json(route, { items: [getRecord()] }),
  )
  await page.route('**/api/v1/admin/auth-connections?*', (route) =>
    json(route, { items: [getRecord()] }),
  )
  await page.route('**/api/v1/admin/auth-connections/company', (route) => json(route, getRecord()))
  await page.route('**/api/v1/admin/auth-connections/company/attributes', (route) =>
    json(route, { revision: getRecord().aggregate_revision, fields: [], operators: {} }),
  )
  await page.route('**/api/v1/admin/auth-connections/company/check', (route) =>
    json(route, {
      valid: true,
      revision: getRecord().aggregate_revision,
      fresh_supported: true,
      issues: [],
      rules: authDiagnostics,
    }),
  )
  await page.route('**/api/v1/admin/auth-connections/company/test', async (route: Route) => {
    calls.start += 1
    const cascade = {
      transaction_id: 'transaction-e2e',
      authorization_url: 'https://identity.example.com/authorize',
      expires_at: new Date(Date.now() + 120_000).toISOString(),
    }
    const override = await Promise.resolve(
      handler.start?.(route.request().postDataJSON(), getRecord()),
    )
    return json(route, override ?? cascade)
  })
  await page.route('**/api/v1/auth/external/exchange', (route) => {
    calls.exchange += 1
    expect(route.request().postDataJSON()).toEqual({
      ticket: 'ticket-e2e',
      verifier: expect.stringMatching(/^[\w-]{43}$/),
    })
    return json(route, {
      kind: 'test_result',
      connection_id: getRecord().id,
      revision: getRecord().aggregate_revision,
      test_id: 'transaction-e2e',
      fresh_supported: true,
      attributes: {
        email: {
          value: 'person@example.com',
          status: 'pass',
          fingerprint: 'fingerprint-email',
        },
      },
      rules: authDiagnostics,
      expires_at: new Date(Date.now() + 120_000).toISOString(),
    })
  })
  await page.route('**/api/v1/auth/external/cancel', (route) => {
    calls.cancel += 1
    expect(route.request().postDataJSON()).toEqual({
      transaction_id: 'transaction-e2e',
      verifier: expect.stringMatching(/^[\w-]{43}$/),
    })
    return json(route, null)
  })
  await page.route('**/api/v1/admin/auth-connections/company/enable', async (route) => {
    calls.enable += 1
    const body = route.request().postDataJSON()
    expect(body.expected_revision).toBe(getRecord().aggregate_revision)
    const override = await Promise.resolve(handler.enable?.(body))
    return json(route, override ?? { ...getRecord(), state: 'enabled' })
  })
  await page.route('**/api/v1/admin/auth-connections/company/configure', (route) => {
    calls.configure += 1
    return json(route, getRecord())
  })
  return calls
}
