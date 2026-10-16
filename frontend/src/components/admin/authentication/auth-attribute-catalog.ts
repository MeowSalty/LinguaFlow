/**
 * Pure helpers for the admin attribute-mapping editor.
 *
 * Pointer canonicalisation and reserved `field_id`s are enforced by the server; this module
 * only keeps the draft self-consistent (unique ids, protocol-compatible sources, row limit)
 * so the editor can block saving obviously broken mappings.
 */
import type { ApiSchemas } from '@/api/client-core'

export type AuthAttributeField = ApiSchemas['AuthAttributeField']
export type AuthAttributeType = AuthAttributeField['type']
export type AuthAttributeSource = AuthAttributeField['source']
export type AuthConnectionKind = 'oidc' | 'generic_oauth2'

export const AUTH_ATTRIBUTE_MAX_FIELDS = 64

export const AUTH_ATTRIBUTE_TYPES: readonly AuthAttributeType[] = [
  'string',
  'boolean',
  'integer',
  'number',
  'datetime',
  'string_list',
]

/** Sources an administrator may pick per protocol. `derived` is never selectable. */
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
  SELECTABLE_SOURCES_BY_KIND[kind][0]

/** Derived fields are server-computed and read-only. */
export const isDerivedAttributeField = (field: AuthAttributeField): boolean =>
  field.source === 'derived'

export const isAttributeSourceAllowed = (
  kind: AuthConnectionKind,
  source: AuthAttributeSource,
): boolean => source === 'derived' || SELECTABLE_SOURCES_BY_KIND[kind].includes(source)

export const createAuthAttributeFieldDraft = (kind: AuthConnectionKind): AuthAttributeField => ({
  field_id: '',
  label: '',
  type: 'string',
  source: defaultAttributeSource(kind),
  pointer: '',
  required_scopes: [],
  optional: false,
  user_editable: false,
})

export type AuthAttributeFieldProblem =
  | 'fieldIdRequired'
  | 'fieldIdDuplicate'
  | 'labelRequired'
  | 'pointerRequired'
  | 'sourceNotAllowed'

export interface AuthAttributeFieldIssue {
  index: number
  code: AuthAttributeFieldProblem
}

export interface AuthAttributeMappingsIssues {
  valid: boolean
  count: number
  limitReached: boolean
  /** Problem scoped to a single row, keyed by row index. */
  rows: AuthAttributeFieldIssue[]
}

export const attributeFieldProblem = (
  field: AuthAttributeField,
  kind: AuthConnectionKind,
  seenFieldIds: ReadonlySet<string>,
): AuthAttributeFieldProblem | null => {
  const fieldId = field.field_id.trim()
  if (fieldId.length === 0) return 'fieldIdRequired'
  if (seenFieldIds.has(fieldId)) return 'fieldIdDuplicate'
  if (field.label.trim().length === 0) return 'labelRequired'
  if (field.pointer.trim().length === 0) return 'pointerRequired'
  if (!isAttributeSourceAllowed(kind, field.source)) return 'sourceNotAllowed'
  return null
}

/**
 * Validate the whole mapping list. Duplicate `field_id`s are reported on every later
 * occurrence so the editor can highlight each conflicting row.
 */
export const validateAuthAttributeMappings = (
  fields: readonly AuthAttributeField[],
  kind: AuthConnectionKind,
): AuthAttributeMappingsIssues => {
  const rows: AuthAttributeFieldIssue[] = []
  const seen = new Set<string>()
  fields.forEach((field, index) => {
    const fieldId = field.field_id.trim()
    const duplicate = fieldId.length > 0 && seen.has(fieldId)
    if (fieldId.length > 0) seen.add(fieldId)
    const code = duplicate ? 'fieldIdDuplicate' : attributeFieldProblem(field, kind, new Set())
    if (code) rows.push({ index, code })
  })
  const limitReached = fields.length > AUTH_ATTRIBUTE_MAX_FIELDS
  return {
    valid: rows.length === 0 && !limitReached,
    count: fields.length,
    limitReached,
    rows,
  }
}

/** Deep copy so edits never mutate the committed mapping array in place. */
export const cloneAuthAttributeFields = (
  fields: readonly AuthAttributeField[],
): AuthAttributeField[] =>
  fields.map((field) => ({
    ...field,
    ...(field.required_scopes ? { required_scopes: [...field.required_scopes] } : {}),
    ...(field.derived_from ? { derived_from: [...field.derived_from] } : {}),
  }))

/** Derived rows must stay untouched: the editor renders them read-only. */
export const derivedFieldDependencies = (field: AuthAttributeField): string[] =>
  field.derived_from ?? []
