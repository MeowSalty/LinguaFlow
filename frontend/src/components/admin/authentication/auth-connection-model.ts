import type { ApiSchemas } from '@/api/client-core'
import type {
  AuthConnection,
  AuthConnectionSnapshot,
  AuthAttributeField,
} from '@/api/auth-connections'

export type SecretMode = 'keep' | 'replace' | 'clear'
export const clone = <T>(value: T): T => JSON.parse(JSON.stringify(value)) as T
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
    revision: 0,
    has_client_secret: false,
    policy: {
      enabled: false,
      login_enabled: false,
      signup_enabled: false,
      link_enabled: false,
      login_rules: { mode: 'unrestricted' },
      signup_rules: { mode: 'unrestricted' },
      link_rules: { mode: 'unrestricted' },
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

export function connectionUpdate(
  record: AuthConnection,
  draft: AuthConnectionSnapshot,
  mode: SecretMode,
  secret: string,
): ApiSchemas['UpdateAuthConnectionRequest'] {
  if (hasUnsafeRuleNumbers(record.draft ?? record.published) || hasUnsafeRuleNumbers(draft))
    throw new Error('Unsafe rule number')
  const policy = clone(draft.policy)
  if (record.kind === 'password') {
    if (
      [policy.login_rules, policy.signup_rules, policy.link_rules].some(
        (rules) => rules.mode !== 'unrestricted',
      )
    )
      throw new Error('Password rules must be unrestricted')
  }
  const body: ApiSchemas['UpdateAuthConnectionRequest'] = {
    expected_revision: record.aggregate_revision,
    display_name: draft.display_name.trim(),
    description: draft.description,
    display_order: draft.display_order,
    policy,
  }
  if (record.kind !== 'password') {
    body.attribute_mappings = clone(draft.attribute_mappings ?? [])
    body.evidence_max_age_seconds = draft.evidence_max_age_seconds ?? 3600
    if (record.kind === 'oidc') body.oidc = clone(draft.oidc)
    else body.oauth2 = clone(draft.oauth2)
    if (mode === 'clear') body.client_secret = null
    if (mode === 'replace') {
      if (!secret.length) throw new Error('Empty replacement secret')
      body.client_secret = secret
    }
  }
  return body
}

export function connectionFormValid(
  kind: AuthConnection['kind'],
  draft: AuthConnectionSnapshot,
): boolean {
  const url = (value: string) => {
    try {
      return ['http:', 'https:'].includes(new URL(value).protocol)
    } catch {
      return false
    }
  }
  if (!draft.display_name.trim() || !Number.isSafeInteger(draft.display_order)) return false
  if (kind === 'password')
    return [draft.policy.login_rules, draft.policy.signup_rules, draft.policy.link_rules].every(
      (rules) => rules.mode === 'unrestricted',
    )
  if (kind === 'oidc') return Boolean(draft.oidc?.client_id.trim() && url(draft.oidc.issuer))
  const cfg = draft.oauth2
  return Boolean(
    cfg &&
    cfg.client_id.trim() &&
    [cfg.authorization_endpoint, cfg.token_endpoint, cfg.profile_endpoint].every(url) &&
    /^\/(?:[^~]|~[01])*$/.test(cfg.subject_mapping.pointer),
  )
}

/** Preserve built-in fields but remove deleted custom mappings from the live rule catalogue. */
export function mergeDraftAttributes(
  catalog: AuthAttributeField[],
  saved: AuthAttributeField[],
  draft: AuthAttributeField[],
) {
  const savedIds = new Set(saved.map((field) => field.field_id))
  const merged = new Map(
    catalog
      .filter((field) => !savedIds.has(field.field_id) || field.source === 'derived')
      .map((field) => [field.field_id, field]),
  )
  for (const field of draft)
    if (field.field_id && field.source !== 'derived') merged.set(field.field_id, field)
  return [...merged.values()]
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
