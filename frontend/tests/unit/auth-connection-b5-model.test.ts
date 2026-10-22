import { describe, expect, it, vi } from 'vitest'
import type { AuthConnection } from '../../src/api/auth-connections'
import {
  clone,
  connectionCandidateFingerprint,
  connectionChanges,
  connectionForm,
  connectionSignupRequired,
  newConnectionSnapshot,
  type SecretMode,
} from '../../src/components/admin/authentication/auth-connection-model'

vi.mock('@/i18n', () => ({ t: (key: string) => key }))

const rules = { mode: 'unrestricted' as const }
const signup = { email_requirement: 'optional' as const, password_requirement: 'none' as const }
const record = (): AuthConnection =>
  ({
    id: 'company',
    kind: 'oidc',
    aggregate_revision: 9,
    state: 'configuring',
    archived: false,
    configuration: {
      ...newConnectionSnapshot(),
      login_policy_revision: 4,
      signup_policy_revision: 5,
      link_policy_revision: 6,
      display_name: 'Company SSO',
      description: '',
      display_order: 1,
      oidc: {
        issuer: 'https://identity.example.com',
        client_id: 'client-id',
        scopes: ['openid'],
        userinfo_enabled: true,
        client_auth_method: 'client_secret_basic',
      },
      oauth2: undefined,
      attribute_mappings: [],
      evidence_max_age_seconds: 3600,
      policy: {
        login_enabled: true,
        signup_enabled: false,
        link_enabled: false,
        login_rules: rules,
        signup_rules: rules,
        link_rules: rules,
        signup,
      },
      has_client_secret: true,
      logo_url: null,
    },
    configuration_complete: true,
    capabilities: { can_login: false, can_signup: false, can_link: false, can_test: true },
    validation: { fresh_supported: false, tested_at: null },
    setup: {
      callback_url: null,
      completion_urls: [],
      configuration_complete: true,
      issues: [],
      protocol: {
        required_fields: [],
        client_auth_methods: ['client_secret_basic'],
        scope_guidance: '',
      },
    },
  }) as AuthConnection

describe('B5 external connection model', () => {
  it('initializes all three policy revisions with explicit signup requirements', () => {
    const snapshot = newConnectionSnapshot()
    expect(snapshot.login_policy_revision).toBe(1)
    expect(snapshot.signup_policy_revision).toBe(1)
    expect(snapshot.link_policy_revision).toBe(1)
    expect(snapshot.policy.signup).toEqual({
      email_requirement: 'optional',
      password_requirement: 'none',
    })
  })

  it('preserves saved revisions and signup requirements when reading a record', () => {
    const form = connectionForm(record())
    expect(form.login_policy_revision).toBe(4)
    expect(form.signup_policy_revision).toBe(5)
    expect(form.link_policy_revision).toBe(6)
    expect(form.policy.signup).toEqual(signup)
  })

  it('blocks saving a record whose signup requirements are missing or invalid', () => {
    const saved = record()
    const form = connectionForm(saved)
    expect(connectionSignupRequired(form.policy)).toBe(false)
    delete (form.policy as { signup?: unknown }).signup
    expect(connectionSignupRequired(form.policy)).toBe(true)
    expect(() => connectionChanges(saved, form, 'keep' as SecretMode, '', 'configuration')).toThrow(
      'Missing signup policy',
    )
  })

  it('includes signup requirements in the candidate fingerprint', () => {
    const saved = record()
    const base = connectionCandidateFingerprint(saved, {})
    const changes = connectionChanges(saved, connectionForm(saved), 'keep', '', 'configuration')
    expect(changes.policy).toBeUndefined()
    expect(connectionCandidateFingerprint(saved, changes)).toBe(base)
    const changed = clone(changes)
    changed.policy = {
      ...clone(saved.configuration.policy),
      signup: { email_requirement: 'required', password_requirement: 'none' },
    }
    expect(connectionCandidateFingerprint(saved, changed)).not.toBe(base)
  })
})
