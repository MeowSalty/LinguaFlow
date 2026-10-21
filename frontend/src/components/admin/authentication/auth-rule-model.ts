/**
 * Pure helpers for the admin authentication rule editor.
 *
 * Drafts are lossless: unknown fields/operators are retained and marked invalid, while
 * valid values are written without coercion, rounding or server-metadata inference.
 */
import type { ApiSchemas } from '@/api/client-core'

export type AuthRuleSet = ApiSchemas['AuthRuleSet']
export type AuthRuleCondition = ApiSchemas['AuthRuleCondition']
export type AuthRuleOperator = AuthRuleCondition['operator']
export type AuthRuleValue = AuthRuleCondition['value']
export type AuthAttributeField = ApiSchemas['AuthAttributeField']
export type AuthAttributeType = AuthAttributeField['type']
export type AuthRuleMatch = 'all' | 'any'
export type AuthRuleMode = AuthRuleSet['mode']

export const AUTH_RULE_MAX_CONDITIONS = 64
export const AUTH_RULE_MIN_CONDITIONS = 1
export const AUTH_DOMAIN_SEMANTIC_FORMAT = 'email_domain'
export const AUTH_DOMAIN_MAX_VALUES = 100

export type AuthRuleValueShape = 'string' | 'boolean' | 'integer' | 'number' | 'datetime' | 'list'

export type AuthRuleValueError =
  | 'invalidPrecision'
  | 'invalidNumberFormat'
  | 'invalidNonFinite'
  | 'invalidUnsafeInteger'
  | 'invalidNotInteger'
  | 'invalidDatetime'
  | 'invalidListEmpty'
  | 'invalidListEntry'
  | 'invalidValueShape'
  | 'invalidEmptyText'

export type AuthRuleConditionProblem =
  | 'unknownField'
  | 'unknownOperator'
  | 'operatorNotAllowed'
  | 'domainOperator'
  | 'invalidDomainValue'
  | 'domainLimitReached'
  | AuthRuleValueError

const OPERATORS_BY_TYPE: Record<
  AuthAttributeType,
  readonly [AuthRuleOperator, ...AuthRuleOperator[]]
> = {
  string: [
    'eq',
    'ne',
    'in',
    'not_in',
    'starts_with',
    'not_starts_with',
    'ends_with',
    'not_ends_with',
    'contains',
    'not_contains',
  ],
  boolean: ['eq'],
  integer: ['eq', 'ne', 'in', 'not_in', 'gt', 'gte', 'lt', 'lte'],
  number: ['eq', 'ne', 'in', 'not_in', 'gt', 'gte', 'lt', 'lte'],
  datetime: ['gt', 'gte', 'lt', 'lte'],
  string_list: ['contains', 'not_contains', 'contains_any', 'contains_all'],
}

export const DNS_DOMAIN_OPERATORS: readonly AuthRuleOperator[] = ['eq', 'ne', 'in', 'not_in']

const ALL_OPERATORS: readonly AuthRuleOperator[] = [
  'eq',
  'ne',
  'in',
  'not_in',
  'gt',
  'gte',
  'lt',
  'lte',
  'contains',
  'not_contains',
  'starts_with',
  'not_starts_with',
  'ends_with',
  'not_ends_with',
  'contains_any',
  'contains_all',
]

const MULTI_VALUE_OPERATORS: readonly AuthRuleOperator[] = [
  'in',
  'not_in',
  'contains_any',
  'contains_all',
]

const NON_EMPTY_TEXT_OPERATORS: readonly AuthRuleOperator[] = [
  'starts_with',
  'not_starts_with',
  'ends_with',
  'not_ends_with',
  'contains',
  'not_contains',
]

const INTEGER_PATTERN = /^[+-]?[0-9]+$/
const NUMBER_PATTERN = /^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$/

/** Compare decimal values as written on the wire, without binary floating-point rounding. */
const decimalValue = (value: string): string => {
  const [mantissa = '', exponent = '0'] = value.toLowerCase().split('e')
  const negative = mantissa.startsWith('-')
  const unsigned = mantissa.replace(/^[+-]/, '')
  const [whole = '', fraction = ''] = unsigned.split('.')
  const digits = (whole + fraction).replace(/^0+/, '')
  if (!digits) return '0'
  const significant = digits.replace(/0+$/, '')
  const scale = Number(exponent) - fraction.length + digits.length - significant.length
  return `${negative ? '-' : ''}${significant}e${scale}`
}

const RFC3339_PATTERN =
  /^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]+)?(?:Z|[+-][0-9]{2}:[0-9]{2})$/

export const isAuthRuleOperator = (value: unknown): value is AuthRuleOperator =>
  typeof value === 'string' && (ALL_OPERATORS as readonly string[]).includes(value)

export const authRuleOperatorsForType = (type: AuthAttributeType): AuthRuleOperator[] => [
  ...OPERATORS_BY_TYPE[type],
]

export const isOperatorAllowedForType = (
  type: AuthAttributeType,
  operator: AuthRuleOperator,
): boolean => OPERATORS_BY_TYPE[type].includes(operator)

/**
 * The server field list is authoritative. Client support only narrows it; no type
 * table can resurrect an operator the field omitted.
 */
export const authRuleOperatorsForField = (field: AuthAttributeField): AuthRuleOperator[] => {
  const allowed = isDnsDomainAttributeField(field)
    ? DNS_DOMAIN_OPERATORS
    : authRuleOperatorsForType(field.type)
  return allowed.filter(
    (operator) =>
      isOperatorAllowedForType(field.type, operator) && field.allowed_operators.includes(operator),
  )
}

export const isOperatorAllowedForField = (
  field: AuthAttributeField,
  operator: AuthRuleOperator,
): boolean => authRuleOperatorsForField(field).includes(operator)

export const isDnsDomainAttributeField = (field: AuthAttributeField): boolean =>
  field.semantic_format === AUTH_DOMAIN_SEMANTIC_FORMAT

/** Typed value a (field type, operator) pair expects; drives the editor control. */
export const authRuleValueShape = (
  type: AuthAttributeType,
  operator: AuthRuleOperator,
): AuthRuleValueShape => {
  if (MULTI_VALUE_OPERATORS.includes(operator)) return 'list'
  switch (type) {
    case 'boolean':
      return 'boolean'
    case 'integer':
      return 'integer'
    case 'number':
      return 'number'
    case 'datetime':
      return 'datetime'
    default:
      return 'string'
  }
}

export const authRuleValueShapeFor = (
  field: AuthAttributeField,
  operator: AuthRuleOperator,
): AuthRuleValueShape | null =>
  isOperatorAllowedForField(field, operator) ? authRuleValueShape(field.type, operator) : null

export type AuthRuleValueCheck =
  | { ok: true; value: AuthRuleValue }
  | { ok: false; error: AuthRuleValueError }

export const checkAuthRuleValue = (
  value: unknown,
  shape: AuthRuleValueShape,
): AuthRuleValueCheck => {
  switch (shape) {
    case 'string':
      return typeof value === 'string'
        ? { ok: true, value }
        : { ok: false, error: 'invalidValueShape' }
    case 'boolean':
      return typeof value === 'boolean'
        ? { ok: true, value }
        : { ok: false, error: 'invalidValueShape' }
    case 'integer': {
      if (typeof value !== 'number') return { ok: false, error: 'invalidValueShape' }
      if (!Number.isFinite(value)) return { ok: false, error: 'invalidNonFinite' }
      if (!Number.isInteger(value)) return { ok: false, error: 'invalidNotInteger' }
      if (!Number.isSafeInteger(value)) return { ok: false, error: 'invalidUnsafeInteger' }
      return { ok: true, value }
    }
    case 'number': {
      if (typeof value !== 'number') return { ok: false, error: 'invalidValueShape' }
      if (!Number.isFinite(value)) return { ok: false, error: 'invalidNonFinite' }
      if (Number.isInteger(value) && !Number.isSafeInteger(value))
        return { ok: false, error: 'invalidUnsafeInteger' }
      return { ok: true, value }
    }
    case 'datetime':
      return typeof value === 'string' && isRfc3339WithTimezone(value)
        ? { ok: true, value }
        : { ok: false, error: 'invalidDatetime' }
    case 'list': {
      if (!Array.isArray(value)) return { ok: false, error: 'invalidValueShape' }
      if (value.length === 0) return { ok: false, error: 'invalidListEmpty' }
      if (value.some((item) => typeof item !== 'string'))
        return { ok: false, error: 'invalidValueShape' }
      if (value.some((item) => item.trim().length === 0))
        return { ok: false, error: 'invalidListEntry' }
      return { ok: true, value: [...(value as string[])] }
    }
  }
}

export const parseAuthRuleText = (text: string, shape: AuthRuleValueShape): AuthRuleValueCheck => {
  switch (shape) {
    case 'string':
      return { ok: true, value: text }
    case 'boolean':
    case 'list':
      return { ok: false, error: 'invalidValueShape' }
    case 'integer': {
      const trimmed = text.trim()
      if (!INTEGER_PATTERN.test(trimmed)) return { ok: false, error: 'invalidNumberFormat' }
      const parsed = Number(trimmed)
      if (!Number.isFinite(parsed)) return { ok: false, error: 'invalidNonFinite' }
      const check = checkAuthRuleValue(parsed, 'integer')
      return check.ok ? check : { ok: false, error: check.error }
    }
    case 'number': {
      const trimmed = text.trim()
      if (!NUMBER_PATTERN.test(trimmed)) return { ok: false, error: 'invalidNumberFormat' }
      const parsed = Number(trimmed)
      if (!Number.isFinite(parsed)) return { ok: false, error: 'invalidNonFinite' }
      const check = checkAuthRuleValue(parsed, 'number')
      if (!check.ok) return check
      if (decimalValue(trimmed) !== decimalValue(JSON.stringify(parsed)))
        return { ok: false, error: 'invalidPrecision' }
      return check
    }
    case 'datetime': {
      const trimmed = text.trim()
      return isRfc3339WithTimezone(trimmed)
        ? { ok: true, value: trimmed }
        : { ok: false, error: 'invalidDatetime' }
    }
  }
}

export const isRfc3339WithTimezone = (value: string): boolean => {
  if (!RFC3339_PATTERN.test(value)) return false
  const year = Number(value.slice(0, 4))
  const month = Number(value.slice(5, 7))
  const day = Number(value.slice(8, 10))
  const leapYear = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0)
  const days = [31, leapYear ? 29 : 28, 31, 30, 31, 31, 30, 31, 30, 31, 30, 31]
  if (month < 1 || month > 12 || day < 1 || day > (days[month - 1] ?? 0)) return false
  if (
    Number(value.slice(11, 13)) > 23 ||
    Number(value.slice(14, 16)) > 59 ||
    Number(value.slice(17, 19)) > 59
  )
    return false
  const time = Date.parse(value)
  if (Number.isNaN(time)) return false
  const offset = /(?:[Zz]|[+-][0-9]{2}:[0-9]{2})$/.exec(value)?.[0] ?? ''
  if (offset.length === 6) {
    const hours = Number(offset.slice(1, 3))
    const minutes = Number(offset.slice(4, 6))
    return hours <= 23 && minutes <= 59
  }
  return true
}

export const findAuthAttributeField = (
  fields: readonly AuthAttributeField[],
  fieldId: string,
): AuthAttributeField | undefined => fields.find((field) => field.field_id === fieldId)

export const isKnownAuthAttributeField = (
  fields: readonly AuthAttributeField[],
  fieldId: string,
): boolean => findAuthAttributeField(fields, fieldId) !== undefined

/**
 * Structural domain checks only. IDNA normalization, canonicalization and deduping
 * remain server decisions; the client never applies URL or locale-specific rewriting.
 */
export const isPlausibleDnsDomain = (value: unknown): value is string => {
  if (typeof value !== 'string' || value !== value.trim()) return false
  if (!value || value.length > 253 || value.endsWith('.')) return false
  if (value.includes('@') || value.includes('*') || value.includes(' ')) return false
  if (/[/\\:?[\]<>]/.test(value)) return false
  const labels = value.split('.')
  if (labels.length < 2) return false
  return (
    labels.every((label) => {
      if (!label || label.length > 63 || label.startsWith('-') || label.endsWith('-')) return false
      return /^[\p{L}\p{N}][\p{L}\p{N}-]*$/u.test(label)
    }) && !labels.every((label) => /^[0-9]+$/.test(label))
  )
}

export const domainValuesFromCondition = (value: AuthRuleValue): string[] =>
  Array.isArray(value) ? value : [value].filter((item): item is string => typeof item === 'string')

const validateDomainValues = (value: AuthRuleValue): AuthRuleConditionProblem | null => {
  const values = domainValuesFromCondition(value)
  if (values.length > AUTH_DOMAIN_MAX_VALUES) return 'domainLimitReached'
  return values.every((item) => isPlausibleDnsDomain(item)) ? null : 'invalidDomainValue'
}

const validateOperatorText = (
  operator: AuthRuleOperator,
  value: AuthRuleValue,
): AuthRuleConditionProblem | null => {
  if (!NON_EMPTY_TEXT_OPERATORS.includes(operator)) return null
  return typeof value === 'string' && value.trim().length > 0 ? null : 'invalidEmptyText'
}

export const defaultAuthRuleValue = (
  shape: AuthRuleValueShape,
  now: Date = new Date(),
): AuthRuleValue => {
  switch (shape) {
    case 'boolean':
      return true
    case 'integer':
    case 'number':
      return 0
    case 'datetime':
      return formatRfc3339WithTimezone(now)
    case 'list':
      return []
    default:
      return ''
  }
}

export const formatRfc3339WithTimezone = (date: Date): string => {
  const pad = (value: number, size = 2): string => String(value).padStart(size, '0')
  const offsetMinutes = -date.getTimezoneOffset()
  const sign = offsetMinutes < 0 ? '-' : '+'
  const absOffset = Math.abs(offsetMinutes)
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}${sign}${pad(Math.floor(absOffset / 60))}:${pad(absOffset % 60)}`
}

const draftText = (value: AuthRuleValue, shape: AuthRuleValueShape): string =>
  shape === 'boolean' || shape === 'list' ? '' : String(value)

export const defaultOperatorForType = (type: AuthAttributeType): AuthRuleOperator =>
  OPERATORS_BY_TYPE[type][0]!

export const defaultOperatorForField = (field: AuthAttributeField): AuthRuleOperator | null =>
  authRuleOperatorsForField(field)[0] ?? null

export interface AuthRuleConditionDraft {
  field_id: string
  operator: AuthRuleOperator
  text: string
  value: AuthRuleValue
  typedValue: boolean
}

export const createAuthRuleConditionDraft = (
  field: AuthAttributeField,
  now: Date = new Date(),
): AuthRuleConditionDraft | null => {
  const operator = defaultOperatorForField(field)
  if (!operator) return null
  const shape = authRuleValueShape(field.type, operator)
  const value =
    shape === 'string' && NON_EMPTY_TEXT_OPERATORS.includes(operator)
      ? 'value'
      : defaultAuthRuleValue(shape, now)
  return {
    field_id: field.field_id,
    operator,
    text: draftText(value, shape),
    value,
    typedValue: shape === 'boolean' || shape === 'list',
  }
}

export const retargetAuthRuleConditionDraft = (
  draft: AuthRuleConditionDraft,
  field: AuthAttributeField,
  now: Date = new Date(),
): AuthRuleConditionDraft => {
  const keepOperator = isOperatorAllowedForField(field, draft.operator)
  const operator = keepOperator ? draft.operator : defaultOperatorForField(field)
  if (!operator) return { ...draft, field_id: field.field_id }
  const shape = authRuleValueShape(field.type, operator)
  if (keepOperator && draft.typedValue && (shape === 'boolean' || shape === 'list')) {
    const check = checkAuthRuleValue(draft.value, shape)
    const value = check.ok ? check.value : defaultAuthRuleValue(shape, now)
    return { ...draft, field_id: field.field_id, value, typedValue: true }
  }
  if (keepOperator && !draft.typedValue && shape !== 'boolean' && shape !== 'list') {
    const check = parseAuthRuleText(draft.text, shape)
    return {
      ...draft,
      field_id: field.field_id,
      value: check.ok ? check.value : defaultAuthRuleValue(shape, now),
      typedValue: false,
    }
  }
  const value = defaultAuthRuleValue(shape, now)
  return {
    field_id: field.field_id,
    operator,
    text: draftText(value, shape),
    value,
    typedValue: shape === 'boolean' || shape === 'list',
  }
}

export const authRuleConditionFromDraft = (draft: AuthRuleConditionDraft): AuthRuleCondition => ({
  field_id: draft.field_id,
  operator: draft.operator,
  value: normalizeAuthRuleValue(draft.value),
})

export const authRuleConditionToDraft = (condition: AuthRuleCondition): AuthRuleConditionDraft => {
  const typedValue = typeof condition.value === 'boolean' || Array.isArray(condition.value)
  return {
    field_id: condition.field_id,
    operator: condition.operator,
    text: typedValue ? '' : String(condition.value),
    value: normalizeAuthRuleValue(condition.value),
    typedValue,
  }
}

export const normalizeAuthRuleValue = (value: AuthRuleValue): AuthRuleValue =>
  Array.isArray(value) ? [...value] : value

export const authRuleConditionProblem = (
  draft: AuthRuleConditionDraft,
  fields: readonly AuthAttributeField[],
): AuthRuleConditionProblem | null => {
  const field = findAuthAttributeField(fields, draft.field_id)
  if (!field) return 'unknownField'
  if (!isAuthRuleOperator(draft.operator)) return 'unknownOperator'
  if (isDnsDomainAttributeField(field) && !DNS_DOMAIN_OPERATORS.includes(draft.operator))
    return 'domainOperator'
  if (!isOperatorAllowedForField(field, draft.operator))
    return isDnsDomainAttributeField(field) ? 'domainOperator' : 'operatorNotAllowed'
  const shape = authRuleValueShape(field.type, draft.operator)
  const check = draft.typedValue
    ? checkAuthRuleValue(draft.value, shape)
    : parseAuthRuleText(draft.text, shape)
  if (!check.ok) return check.error
  const typedCheck = checkAuthRuleValue(draft.value, shape)
  if (!typedCheck.ok) return typedCheck.error
  if (isDnsDomainAttributeField(field)) return validateDomainValues(draft.value)
  return validateOperatorText(draft.operator, draft.value)
}

export interface AuthRuleDraftState {
  mode: AuthRuleMode
  match: AuthRuleMatch
  conditions: AuthRuleConditionDraft[]
}

export interface AuthRuleSetProblem {
  scope: 'conditions' | 'condition' | 'rule'
  code: string
  index?: number
}

export interface AuthRuleValidationOptions {
  emailRequirement?: 'optional' | 'required'
}

export const authRuleSetFromDrafts = (state: AuthRuleDraftState): AuthRuleSet =>
  state.mode === 'unrestricted'
    ? { mode: 'unrestricted' }
    : {
        mode: 'conditions',
        match: state.match,
        conditions: state.conditions.map(authRuleConditionFromDraft),
      }

export const authRuleSetsEqual = (left: AuthRuleSet, right: AuthRuleSet): boolean => {
  if (left.mode === 'unrestricted' || right.mode === 'unrestricted') return left.mode === right.mode
  return (
    left.match === right.match &&
    left.conditions.length === right.conditions.length &&
    left.conditions.every((condition, index) => {
      const other = right.conditions[index]
      if (!other || condition.field_id !== other.field_id || condition.operator !== other.operator)
        return false
      return Array.isArray(condition.value)
        ? Array.isArray(other.value) &&
            condition.value.length === other.value.length &&
            condition.value.every(
              (item, valueIndex) => item === (other.value as string[])[valueIndex],
            )
        : Object.is(condition.value, other.value)
    })
  )
}

export const authRuleDraftsFromSet = (value: AuthRuleSet): AuthRuleDraftState => {
  if (value.mode === 'unrestricted') return { mode: 'unrestricted', match: 'all', conditions: [] }
  return {
    mode: 'conditions',
    match: value.match,
    conditions: value.conditions.map(authRuleConditionToDraft),
  }
}

const hasRegistrationDomainCondition = (
  conditions: readonly AuthRuleCondition[] | readonly AuthRuleConditionDraft[],
  fields: readonly AuthAttributeField[],
): boolean =>
  conditions.some((condition) => {
    const field = findAuthAttributeField(fields, condition.field_id)
    return (
      condition.field_id === 'registration.email_domain' &&
      !!field &&
      isDnsDomainAttributeField(field)
    )
  })

export const validateAuthRuleSet = (
  value: AuthRuleSet,
  fields: readonly AuthAttributeField[],
  options: AuthRuleValidationOptions = {},
): AuthRuleSetProblem[] => {
  if (value.mode === 'unrestricted') return []
  const problems: AuthRuleSetProblem[] = []
  if (value.conditions.length < AUTH_RULE_MIN_CONDITIONS)
    problems.push({ scope: 'conditions', code: 'emptyConditions' })
  if (value.conditions.length > AUTH_RULE_MAX_CONDITIONS)
    problems.push({ scope: 'conditions', code: 'conditionLimitReached' })
  if (
    hasRegistrationDomainCondition(value.conditions, fields) &&
    options.emailRequirement !== 'required'
  )
    problems.push({ scope: 'rule', code: 'emailDomainRequired' })
  value.conditions.forEach((condition, index) => {
    const problem = authRuleConditionProblem(authRuleConditionToDraft(condition), fields)
    if (problem) problems.push({ scope: 'condition', code: problem, index })
  })
  return problems
}

export const unknownAuthRuleSetProblems = (
  value: AuthRuleSet,
  fields: readonly AuthAttributeField[],
): AuthRuleSetProblem[] =>
  value.mode === 'unrestricted'
    ? []
    : value.conditions.flatMap((condition, index) =>
        findAuthAttributeField(fields, condition.field_id)
          ? []
          : [{ scope: 'condition' as const, code: 'unknownField', index }],
      )

export const validateAuthRuleDraftState = (
  state: AuthRuleDraftState,
  fields: readonly AuthAttributeField[],
  options: AuthRuleValidationOptions = {},
): AuthRuleSetProblem[] => {
  if (state.mode === 'unrestricted') return []
  const problems: AuthRuleSetProblem[] = []
  if (state.conditions.length < AUTH_RULE_MIN_CONDITIONS)
    problems.push({ scope: 'conditions', code: 'emptyConditions' })
  if (state.conditions.length > AUTH_RULE_MAX_CONDITIONS)
    problems.push({ scope: 'conditions', code: 'conditionLimitReached' })
  if (
    hasRegistrationDomainCondition(state.conditions, fields) &&
    options.emailRequirement !== 'required'
  )
    problems.push({ scope: 'rule', code: 'emailDomainRequired' })
  state.conditions.forEach((draft, index) => {
    const problem = authRuleConditionProblem(draft, fields)
    if (problem) problems.push({ scope: 'condition', code: problem, index })
  })
  return problems
}

/**
 * Explicit allow/block templates. They intentionally use `all`; callers must obtain
 * confirmation before replacing an existing `any` rule or explain its OR meaning.
 */
export const authDomainRuleTemplate = (
  kind: 'allow' | 'block',
  domains: readonly string[],
  field: AuthAttributeField,
): AuthRuleSet => ({
  mode: 'conditions',
  match: 'all',
  conditions: [
    {
      field_id: field.field_id,
      operator: kind === 'allow' ? 'in' : 'not_in',
      value: [...domains],
    },
  ],
})
