/**
 * Attribute catalog and mapping-input helpers.
 *
 * Catalog fields are read-only server results. Mapping drafts use the narrower
 * AuthAttributeMapping contract and never manufacture or retain server-owned metadata.
 */
import type { ApiSchemas } from '@/api/client-core'

export type AuthAttributeCatalog = ApiSchemas['AuthAttributeCatalog']
export type AuthAttributeField = ApiSchemas['AuthAttributeField']
export type AuthAttributeMapping = ApiSchemas['AuthAttributeMapping']
export type AuthAttributeType = AuthAttributeField['type']
export type AuthAttributeSource = AuthAttributeMapping['source']
export type AuthConnectionKind = 'oidc' | 'generic_oauth2'
export type AuthRulePurpose = AuthAttributeCatalog['purpose']

export const AUTH_ATTRIBUTE_MAX_MAPPINGS = 64

export const AUTH_ATTRIBUTE_TYPES: readonly AuthAttributeType[] = [
  'string',
  'boolean',
  'integer',
  'number',
  'datetime',
  'string_list',
]

/** Sources an administrator may select. Registration and derived values are server-owned. */
const SELECTABLE_SOURCES_BY_KIND: Record<
  AuthConnectionKind,
  readonly [AuthAttributeSource, ...AuthAttributeSource[]]
> = {
  oidc: ['oidc_id_token', 'oidc_userinfo'],
  generic_oauth2: ['oauth2_profile'],
}

export const attributeSourcesForKind = (kind: AuthConnectionKind): readonly AuthAttributeSource[] =>
  SELECTABLE_SOURCES_BY_KIND[kind]

export const defaultAttributeSource = (kind: AuthConnectionKind): AuthAttributeSource =>
  SELECTABLE_SOURCES_BY_KIND[kind][0]!

export const createAuthAttributeMappingDraft = (
  kind: AuthConnectionKind,
): AuthAttributeMapping => ({
  field_id: '',
  label: '',
  type: 'string',
  source: defaultAttributeSource(kind),
  pointer: '',
  required_scopes: [],
  optional: false,
  user_editable: false,
})

export type AuthAttributeMappingProblem =
  | 'fieldIdRequired'
  | 'fieldIdDuplicate'
  | 'labelRequired'
  | 'pointerRequired'
  | 'sourceNotAllowed'

export interface AuthAttributeMappingIssue {
  index: number
  code: AuthAttributeMappingProblem
}

export interface AuthAttributeMappingsIssues {
  valid: boolean
  count: number
  limitReached: boolean
  rows: AuthAttributeMappingIssue[]
}

export const attributeMappingProblem = (
  mapping: AuthAttributeMapping,
  kind: AuthConnectionKind,
  seenFieldIds: ReadonlySet<string>,
): AuthAttributeMappingProblem | null => {
  const fieldId = mapping.field_id.trim()
  if (!fieldId) return 'fieldIdRequired'
  if (seenFieldIds.has(fieldId)) return 'fieldIdDuplicate'
  if (!mapping.label.trim()) return 'labelRequired'
  if (!mapping.pointer.trim()) return 'pointerRequired'
  if (!SELECTABLE_SOURCES_BY_KIND[kind].includes(mapping.source)) return 'sourceNotAllowed'
  return null
}

export const validateAuthAttributeMappings = (
  mappings: readonly AuthAttributeMapping[],
  kind: AuthConnectionKind,
): AuthAttributeMappingsIssues => {
  const rows: AuthAttributeMappingIssue[] = []
  const seen = new Set<string>()
  mappings.forEach((mapping, index) => {
    const fieldId = mapping.field_id.trim()
    const duplicate = fieldId.length > 0 && seen.has(fieldId)
    if (fieldId) seen.add(fieldId)
    const code = duplicate ? 'fieldIdDuplicate' : attributeMappingProblem(mapping, kind, new Set())
    if (code) rows.push({ index, code })
  })
  const limitReached = mappings.length > AUTH_ATTRIBUTE_MAX_MAPPINGS
  return {
    valid: rows.length === 0 && !limitReached,
    count: mappings.length,
    limitReached,
    rows,
  }
}

/** Keep the mapping contract and explicitly drop any server-owned field metadata. */
export const cloneAuthAttributeMappings = (
  mappings: readonly AuthAttributeMapping[],
): AuthAttributeMapping[] =>
  mappings.map((mapping) => ({
    field_id: mapping.field_id,
    label: mapping.label,
    type: mapping.type,
    source: mapping.source,
    pointer: mapping.pointer,
    ...(mapping.required_scopes ? { required_scopes: [...mapping.required_scopes] } : {}),
    ...(mapping.optional === undefined ? {} : { optional: mapping.optional }),
    ...(mapping.user_editable === undefined ? {} : { user_editable: mapping.user_editable }),
  }))

export type AuthAttributeCatalogResource =
  | { kind: 'external'; id: string }
  | { kind: 'local-account' }

/** Every security/context dimension that separates one server catalog from another. */
export interface CatalogSourceKey {
  instance: string
  administratorSession: string
  resource: AuthAttributeCatalogResource
  revision: number
  purpose: AuthRulePurpose
  candidateFingerprint: string
}

const catalogIdentity = (source: Omit<CatalogSourceKey, 'candidateFingerprint'>): string =>
  JSON.stringify([
    source.instance,
    source.administratorSession,
    source.resource.kind,
    source.resource.kind === 'external' ? source.resource.id : '',
    source.revision,
    source.purpose,
  ])

export const authAttributeCatalogCacheKey = (source: CatalogSourceKey): string =>
  JSON.stringify([catalogIdentity(source), source.candidateFingerprint])

const canonicalJson = (value: unknown): string => {
  if (value === null || typeof value !== 'object') return JSON.stringify(value ?? null)
  if (Array.isArray(value)) return `[${value.map(canonicalJson).join(',')}]`
  const entries = Object.entries(value as Record<string, unknown>)
    .filter(([, item]) => item !== undefined)
    .sort(([left], [right]) => left.localeCompare(right))
  return `{${entries.map(([key, item]) => `${JSON.stringify(key)}:${canonicalJson(item)}`).join(',')}}`
}

/** Local GET has no candidate body, so this snapshot must enter the cache fingerprint. */
export const authSignupRequirementsFingerprint = (
  signupRequirements: readonly string[] = [],
): string => canonicalJson([...signupRequirements])

export interface AuthCatalogCandidateFingerprint {
  changes?: unknown
  signupRequirements?: readonly string[]
}

/** Candidate GET-equivalent identity includes only data outside the source-key tuple. */
export const authCatalogCandidateFingerprint = (
  candidate: AuthCatalogCandidateFingerprint = {},
): string =>
  canonicalJson({
    changes: candidate.changes ?? null,
    signupRequirements: candidate.signupRequirements ?? [],
  })

export const externalCatalogSourceKey = (input: {
  instance: string
  administratorSession: string
  connectionId: string
  revision: number
  purpose: AuthRulePurpose
  changes?: unknown
}): CatalogSourceKey => ({
  instance: input.instance,
  administratorSession: input.administratorSession,
  resource: { kind: 'external', id: input.connectionId },
  revision: input.revision,
  purpose: input.purpose,
  candidateFingerprint:
    input.changes === undefined
      ? authCatalogCandidateFingerprint()
      : authCatalogCandidateFingerprint({ changes: input.changes }),
})

export const localCatalogSourceKey = (input: {
  instance: string
  administratorSession: string
  revision: number
  signupRequirements?: readonly string[]
}): CatalogSourceKey => ({
  instance: input.instance,
  administratorSession: input.administratorSession,
  resource: { kind: 'local-account' },
  revision: input.revision,
  purpose: 'signup',
  candidateFingerprint: authSignupRequirementsFingerprint(input.signupRequirements),
})

/**
 * Small in-memory cache. A newly returned candidate invalidates older candidate
 * fingerprints for the same instance/session/resource/revision/purpose tuple.
 */
export class AuthAttributeCatalogCache {
  private readonly entries = new Map<string, AuthAttributeCatalog>()

  get(source: CatalogSourceKey): AuthAttributeCatalog | undefined {
    return this.entries.get(authAttributeCatalogCacheKey(source))
  }

  set(source: CatalogSourceKey, catalog: AuthAttributeCatalog): void {
    if (source.resource.kind === 'local-account' && source.purpose !== 'signup')
      throw new Error('Local-account attribute catalogs only support signup purpose')
    const identity = catalogIdentity(source)
    for (const key of this.entries.keys()) {
      const [entryIdentity] = JSON.parse(key) as [string]
      if (entryIdentity === identity) this.entries.delete(key)
    }
    this.entries.set(authAttributeCatalogCacheKey(source), catalog)
  }

  invalidate(source: Omit<CatalogSourceKey, 'candidateFingerprint'>): void {
    const identity = catalogIdentity(source)
    for (const key of this.entries.keys()) {
      const [entryIdentity] = JSON.parse(key) as [string]
      if (entryIdentity === identity) this.entries.delete(key)
    }
  }

  clear(): void {
    this.entries.clear()
  }
}

export const authCatalogFields = (catalog: AuthAttributeCatalog): readonly AuthAttributeField[] =>
  catalog.fields

export const isDerivedAttributeField = (field: AuthAttributeField): boolean =>
  field.source === 'derived'

export const derivedFieldDependencies = (field: AuthAttributeField): string[] =>
  field.derived_from ?? []
