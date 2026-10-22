import { describe, expect, it } from 'vitest'
import type { ApiSchemas } from '../../src/api/client-core'
import type { LocalAccount, LocalAccountPolicy } from '../../src/api/local-account'
import {
  cloneLocalPolicy,
  localPolicyDependencyIssues,
  localPolicyDraft,
  localPolicyEqual,
  localPolicyUpdate,
  localSignupRuleProblems,
} from '../../src/components/admin/authentication/local-account-model'

const accountPolicy = (overrides: Partial<LocalAccountPolicy> = {}): LocalAccountPolicy => ({
  password_login_enabled: true,
  username_login_enabled: true,
  email_login_enabled: false,
  password_enrollment_enabled: true,
  signup: {
    enabled: true,
    email_requirement: 'optional',
    rules: { mode: 'unrestricted' },
  },
  ...overrides,
})

const record = (overrides: Partial<LocalAccount> = {}): LocalAccount => ({
  revision: 7,
  policy: accountPolicy(),
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
  ...overrides,
})

const domainField = (): ApiSchemas['AuthAttributeField'] => ({
  field_id: 'registration.email_domain',
  label: 'Registration email domain',
  type: 'string' as const,
  source: 'derived' as const,
  pointer: '/email_domain',
  user_editable: false,
  allowed_operators: ['eq', 'ne', 'in', 'not_in'] as const,
  semantic_format: 'email_domain' as const,
  normalization_version: 'idna-v1',
  derived_from: ['registration.email'],
})

describe('local policy dependencies', () => {
  it('requires username whenever password login, enrollment or signup is enabled', () => {
    expect(localPolicyDependencyIssues(accountPolicy({ username_login_enabled: false }))).toEqual([
      'username-required',
    ])
    expect(
      localPolicyDependencyIssues(
        accountPolicy({
          username_login_enabled: false,
          signup: { ...accountPolicy().signup, enabled: false },
        }),
      ),
    ).toEqual(['username-required'])
  })

  it('blocks closing password login while enrollment or signup stays on, without inventing other links', () => {
    expect(localPolicyDependencyIssues(accountPolicy({ password_login_enabled: false }))).toEqual([
      'password-login-required',
    ])
    expect(
      localPolicyDependencyIssues(
        accountPolicy({
          password_login_enabled: false,
          password_enrollment_enabled: false,
          signup: { ...accountPolicy().signup, enabled: false },
        }),
      ),
    ).toEqual([])
    expect(
      localPolicyDependencyIssues(
        accountPolicy({ password_enrollment_enabled: false, password_login_enabled: false }),
      ),
    ).toEqual(['password-login-required'])
  })

  it('keeps signup and first-password enrollment independent', () => {
    expect(
      localPolicyDependencyIssues(accountPolicy({ password_enrollment_enabled: false })),
    ).toEqual([])
    expect(
      localPolicyDependencyIssues(
        accountPolicy({
          password_login_enabled: false,
          password_enrollment_enabled: true,
          signup: { ...accountPolicy().signup, enabled: false },
        }),
      ),
    ).toEqual(['password-login-required'])
  })
})

describe('local signup rules', () => {
  it('preserves unknown fields and blocks writes', () => {
    const value = accountPolicy({
      signup: {
        enabled: true,
        email_requirement: 'required',
        rules: {
          mode: 'conditions',
          match: 'all',
          conditions: [{ field_id: 'future.field', operator: 'eq', value: 'kept' }],
        },
      },
    })
    expect(localSignupRuleProblems(value, [domainField()])).toEqual([
      { scope: 'condition', code: 'unknownField', index: 0 },
    ])
  })

  it('rejects IP-literal and non-string domain values without crashing', () => {
    const base = accountPolicy()
    const rules = (value: unknown) => ({
      ...base,
      signup: {
        ...base.signup,
        email_requirement: 'required' as const,
        rules: {
          mode: 'conditions' as const,
          match: 'all' as const,
          conditions: [{ field_id: 'registration.email_domain', operator: 'eq' as const, value }],
        },
      },
    })
    expect(localSignupRuleProblems(rules('192.168.0.1'), [domainField()])).toEqual([
      { scope: 'condition', code: 'invalidDomainValue', index: 0 },
    ])
    expect(localSignupRuleProblems(rules(42), [domainField()])).toEqual([
      { scope: 'condition', code: 'invalidValueShape', index: 0 },
    ])
  })

  it('keeps Unicode labels structurally valid without client canonicalization', () => {
    const value = accountPolicy({
      signup: {
        enabled: true,
        email_requirement: 'required',
        rules: {
          mode: 'conditions',
          match: 'all',
          conditions: [
            { field_id: 'registration.email_domain', operator: 'in', value: ['Bücher.example'] },
          ],
        },
      },
    })
    expect(localSignupRuleProblems(value, [domainField()])).toEqual([])
  })

  it('requires signup email only for domain rules and only from the outer policy', () => {
    const rules = {
      mode: 'conditions' as const,
      match: 'all' as const,
      conditions: [
        { field_id: 'registration.email_domain', operator: 'eq' as const, value: 'example.com' },
      ],
    }
    expect(
      localSignupRuleProblems(
        accountPolicy({
          signup: { ...accountPolicy().signup, email_requirement: 'required', rules },
        }),
        [domainField()],
      ),
    ).toEqual([])
    expect(
      localSignupRuleProblems(
        accountPolicy({
          signup: { ...accountPolicy().signup, email_requirement: 'optional', rules },
        }),
        [domainField()],
      ),
    ).toEqual([{ scope: 'rule', code: 'emailDomainRequired' }])
  })
})

describe('local policy write contract', () => {
  it('sends the complete policy with expected revision and never a partial changes shape', () => {
    const current = record()
    const body = localPolicyUpdate(current.revision, current.policy)
    expect(body).toEqual({ expected_revision: 7, policy: current.policy })
    expect(body).not.toHaveProperty('changes')
  })

  it('clones the payload so later draft mutations cannot alias the saved policy', () => {
    const current = record()
    const body = localPolicyUpdate(current.revision, current.policy)
    body.policy.password_login_enabled = false
    expect(current.policy.password_login_enabled).toBe(true)
    expect(cloneLocalPolicy(body.policy)).toEqual(body.policy)
  })

  it('detects no-change drafts and preserves unknown policy keys through cloning', () => {
    const current = record()
    const draft = localPolicyDraft(current)
    expect(localPolicyEqual(draft, current.policy)).toBe(true)
    const unknown = record({
      policy: { ...current.policy, future_group: { enabled: true } } as LocalAccountPolicy,
    })
    expect(localPolicyEqual(localPolicyDraft(unknown), unknown.policy)).toBe(true)
    expect(localPolicyDraft(unknown).future_group).toEqual({ enabled: true })
  })
})
