import type { LocalAccount, LocalAccountPolicy } from '@/api/local-account'
import { stableJson } from './auth-connection-model'
import {
  unknownAuthRuleSetProblems,
  validateAuthRuleSet,
  type AuthRuleSetProblem,
} from './auth-rule-model'

export type LocalPolicyDependencyIssue = 'username-required' | 'password-login-required'

/**
 * Dependencies are explicit form problems, never silent switches. Signup and first-password
 * enrollment are independent policies; password login still covers both because they create
 * or maintain credentials.
 */
export function localPolicyDependencyIssues(
  policy: LocalAccountPolicy,
): LocalPolicyDependencyIssue[] {
  const issues: LocalPolicyDependencyIssue[] = []
  if (policy.password_login_enabled && !policy.username_login_enabled)
    issues.push('username-required')
  if (policy.password_enrollment_enabled && !policy.username_login_enabled)
    issues.push('username-required')
  if (policy.signup.enabled && !policy.username_login_enabled) issues.push('username-required')
  if (!policy.password_login_enabled && policy.password_enrollment_enabled)
    issues.push('password-login-required')
  if (!policy.password_login_enabled && policy.signup.enabled)
    issues.push('password-login-required')
  return [...new Set(issues)]
}

export const cloneLocalPolicy = <T>(value: T): T =>
  value === undefined ? value : (JSON.parse(JSON.stringify(value)) as T)

export const localPolicyEqual = (left: LocalAccountPolicy, right: LocalAccountPolicy) =>
  stableJson(left) === stableJson(right)

export const localPolicyDraft = (record: LocalAccount): LocalAccountPolicy =>
  cloneLocalPolicy(record.policy)

export function localSignupRuleProblems(
  policy: LocalAccountPolicy,
  fields: LocalAccountFields,
): AuthRuleSetProblem[] {
  const rules = policy.signup.rules
  return [
    ...unknownAuthRuleSetProblems(rules, fields),
    ...validateAuthRuleSet(rules, fields, {
      emailRequirement: policy.signup.email_requirement,
    }),
  ].filter(
    (problem, index, all) =>
      all.findIndex(
        (candidate) =>
          candidate.scope === problem.scope &&
          candidate.code === problem.code &&
          candidate.index === problem.index,
      ) === index,
  )
}

type LocalAccountFields = Parameters<typeof validateAuthRuleSet>[1]

export function localPolicyUpdate(revision: number, policy: LocalAccountPolicy) {
  return { expected_revision: revision, policy: cloneLocalPolicy(policy) }
}
