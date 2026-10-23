import type { ApiSchemas } from '@/api/client-core'
import type { AuthConnectionPolicy } from '@/api/auth-connections'
import type { AuthConnection, AuthConnectionSnapshot } from '@/api/auth-connections'

export type SecretMode = 'keep' | 'replace' | 'clear'
export const clone = <T>(value: T): T =>
  value === undefined ? value : (JSON.parse(JSON.stringify(value)) as T)
/** JSON numeric parsing cannot preserve integers outside the safe range. Never write these back. */
export function hasUnsafeRuleNumbers(snapshot: AuthConnectionSnapshot | undefined | null): boolean {
  return Boolean(
    snapshot &&
    [snapshot.policy.login_rules, snapshot.policy.signup_rules, snapshot.policy.link_rules].some(
      (rules) =>
        rules.mode === 'conditions' &&
        rules.conditions.some(
          (condition) =>
            typeof condition.value === 'number' &&
            (!Number.isFinite(condition.value) ||
              (Number.isInteger(condition.value) && !Number.isSafeInteger(condition.value))),
        ),
    ),
  )
}
export function newConnectionSnapshot(): AuthConnectionSnapshot {
  return {
    display_name: '',
    description: '',
    display_order: 0,
    login_policy_revision: 1,
    signup_policy_revision: 1,
    link_policy_revision: 1,
    has_client_secret: false,
    policy: {
      login_enabled: false,
      signup_enabled: false,
      link_enabled: false,
      login_rules: { mode: 'unrestricted' },
      signup_rules: { mode: 'unrestricted' },
      link_rules: { mode: 'unrestricted' },
      signup: { email_requirement: 'optional', password_requirement: 'none' },
    },
    oidc: {
      issuer: '',
      client_id: '',
      scopes: ['openid', 'profile'],
      userinfo_enabled: true,
      client_auth_method: 'client_secret_basic',
    },
    oauth2: {
      authorization_endpoint: '',
      token_endpoint: '',
      profile_endpoint: '',
      client_id: '',
      scopes: [],
      client_auth_method: 'client_secret_basic',
      subject_mapping: { pointer: '/id', type: 'string' },
    },
    attribute_mappings: [],
    evidence_max_age_seconds: 3600,
  }
}

/** Fill editable empty protocol fields without changing the saved record. */
export function connectionForm(record: AuthConnection): AuthConnectionSnapshot {
  const defaults = newConnectionSnapshot()
  return {
    ...defaults,
    ...clone(record.configuration),
    ...(record.kind === 'oidc'
      ? { oidc: { ...defaults.oidc, ...record.configuration.oidc }, oauth2: undefined }
      : record.kind === 'generic_oauth2'
        ? {
            oidc: undefined,
            oauth2: {
              ...defaults.oauth2,
              ...record.configuration.oauth2,
              subject_mapping: {
                ...defaults.oauth2?.subject_mapping,
                ...record.configuration.oauth2?.subject_mapping,
              },
            },
          }
        : { oidc: undefined, oauth2: undefined }),
  }
}

/** B5 protocol policies require explicit registration requirements in every readable record. */
export function connectionSignupPolicyValid(policy: AuthConnectionPolicy): boolean {
  const signup = policy.signup as
    | { email_requirement?: unknown; password_requirement?: unknown }
    | undefined
  return Boolean(
    signup &&
    ['optional', 'required'].includes(String(signup.email_requirement)) &&
    ['none', 'optional', 'required'].includes(String(signup.password_requirement)),
  )
}

/** A legacy/missing signup object is never silently defaulted for an existing record. */
export const connectionSignupRequired = (policy: AuthConnectionPolicy): boolean =>
  !connectionSignupPolicyValid(policy)

export type ConnectionSaveGroup = 'presentation' | 'configuration' | 'all'
/** Compare JSON content independently of object property insertion order. */
export function stableJson(value: unknown): string {
  const normalize = (input: unknown): unknown => {
    if (Array.isArray(input)) return input.map(normalize)
    if (input && typeof input === 'object')
      return Object.fromEntries(
        Object.entries(input)
          .sort(([a], [b]) => a.localeCompare(b))
          .map(([key, item]) => [key, normalize(item)]),
      )
    return input
  }
  return JSON.stringify(normalize(value))
}
const same = (a: unknown, b: unknown): boolean => stableJson(a) === stableJson(b)

/** Partial writes keep presentation saves independent of unfinished protocol edits. */
export function connectionChanges(
  record: AuthConnection,
  form: AuthConnectionSnapshot,
  mode: SecretMode,
  secret: string,
  group: ConnectionSaveGroup = 'all',
): ApiSchemas['AuthConnectionChanges'] {
  const saved = connectionForm(record)
  const body: ApiSchemas['AuthConnectionChanges'] = {}
  if (group !== 'configuration') {
    if (form.display_name.trim() !== saved.display_name)
      body.display_name = form.display_name.trim()
    if (form.description !== saved.description) body.description = form.description
    if (form.display_order !== saved.display_order) body.display_order = form.display_order
  }
  if (group === 'presentation') return body
  if (hasUnsafeRuleNumbers(record.configuration) || hasUnsafeRuleNumbers(form))
    throw new Error('Unsafe rule number')
  if (connectionSignupRequired(form.policy)) throw new Error('Missing signup policy')
  if (!same(form.policy, saved.policy)) body.policy = clone(form.policy)
  if (!same(form.attribute_mappings, saved.attribute_mappings))
    body.attribute_mappings = clone(form.attribute_mappings)
  if (form.evidence_max_age_seconds !== saved.evidence_max_age_seconds)
    body.evidence_max_age_seconds = form.evidence_max_age_seconds
  if (record.kind === 'oidc' && !same(form.oidc, saved.oidc)) body.oidc = clone(form.oidc)
  if (record.kind === 'generic_oauth2' && !same(form.oauth2, saved.oauth2))
    body.oauth2 = clone(form.oauth2)
  if (mode === 'clear') body.client_secret = null
  if (mode === 'replace') {
    if (!secret.length) throw new Error('Empty replacement secret')
    body.client_secret = secret
  }
  return body
}

export const connectionProtocolChanged = (changes: ApiSchemas['AuthConnectionChanges']): boolean =>
  changes.oidc !== undefined || changes.oauth2 !== undefined || 'client_secret' in changes

/** Shared candidate identity for diagnostics, evidence and purpose-scoped catalogs. */
export function connectionCandidateFingerprint(
  record: AuthConnection,
  changes: ApiSchemas['AuthConnectionChanges'],
): string {
  return stableJson(connectionTestCandidate(record, changes))
}

/** The candidate diagnostic identity excludes presentation and lifecycle fields. Memory only. */
export function connectionTestCandidate(
  record: AuthConnection,
  changes: ApiSchemas['AuthConnectionChanges'],
) {
  const effective = { ...connectionForm(record), ...clone(changes) }
  return {
    protocol: record.kind === 'oidc' ? effective.oidc : effective.oauth2,
    mappings: effective.attribute_mappings,
    evidence_max_age_seconds: effective.evidence_max_age_seconds,
    policy: effective.policy,
    secret:
      'client_secret' in changes ? changes.client_secret : { current: effective.has_client_secret },
  }
}

export function connectionUpdate(
  record: AuthConnection,
  draft: AuthConnectionSnapshot,
  mode: SecretMode,
  secret: string,
  group: ConnectionSaveGroup = 'all',
): ApiSchemas['UpdateAuthConnectionRequest'] {
  return {
    expected_revision: record.aggregate_revision,
    ...connectionChanges(record, draft, mode, secret, group),
  }
}

export function connectionFormValid(
  kind: AuthConnection['kind'],
  draft: AuthConnectionSnapshot,
  complete = true,
): boolean {
  const url = (value: string | undefined) => {
    if (!value) return !complete
    try {
      return ['http:', 'https:'].includes(new URL(value).protocol)
    } catch {
      return false
    }
  }
  if (!draft.display_name.trim() || !Number.isSafeInteger(draft.display_order)) return false
  if (kind === 'oidc')
    return Boolean((!complete || draft.oidc?.client_id?.trim()) && url(draft.oidc?.issuer))
  const cfg = draft.oauth2
  return Boolean(
    cfg &&
    (!complete || cfg.client_id?.trim()) &&
    [cfg.authorization_endpoint, cfg.token_endpoint, cfg.profile_endpoint].every(url) &&
    ((!complete && !cfg.subject_mapping?.pointer) ||
      /^\/(?:[^~]|~[01])*$/.test(cfg.subject_mapping?.pointer ?? '')),
  )
}

export function parsePreviewSources(
  input: string,
): ApiSchemas['AuthRulesPreviewRequest']['sources'] {
  const data: unknown = JSON.parse(input)
  if (!data || typeof data !== 'object' || Array.isArray(data)) throw new Error('Invalid sources')
  for (const [key, value] of Object.entries(data)) {
    if (
      !['oidc_id_token', 'oidc_userinfo'].includes(key) ||
      !value ||
      typeof value !== 'object' ||
      Array.isArray(value)
    )
      throw new Error('Invalid source')
  }
  return data as ApiSchemas['AuthRulesPreviewRequest']['sources']
}
