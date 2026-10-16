/**
 * Pure helpers for the admin authentication rule editor.
 *
 * The editor keeps an intentionally lossless draft: values are only written to the
 * model after they pass strict, coercion-free validation, so an invalid draft never
 * silently rounds, truncates or type-casts a rule value.
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

/** Value shape of a condition, derived from the referenced field type and operator. */
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

export type AuthRuleConditionProblem = 'unknownField' | 'operatorNotAllowed' | AuthRuleValueError

const OPERATORS_BY_TYPE: Record<
  AuthAttributeType,
  readonly [AuthRuleOperator, ...AuthRuleOperator[]]
> = {
  string: ['eq', 'ne', 'in'],
  boolean: ['eq'],
  integer: ['eq', 'ne', 'gt', 'gte', 'lt', 'lte'],
  number: ['eq', 'ne', 'gt', 'gte', 'lt', 'lte'],
  datetime: ['gt', 'gte', 'lt', 'lte'],
  string_list: ['contains', 'not_contains', 'contains_any', 'contains_all'],
}

const ALL_OPERATORS: readonly AuthRuleOperator[] = [
  'eq',
  'ne',
  'in',
  'gt',
  'gte',
  'lt',
  'lte',
  'contains',
  'not_contains',
  'contains_any',
  'contains_all',
]

const MULTI_VALUE_OPERATORS: readonly AuthRuleOperator[] = ['in', 'contains_any', 'contains_all']

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

/** Operators the API contract allows for the given field type. */
export const authRuleOperatorsForType = (type: AuthAttributeType): AuthRuleOperator[] => [
  ...OPERATORS_BY_TYPE[type],
]

export const isOperatorAllowedForType = (
  type: AuthAttributeType,
  operator: AuthRuleOperator,
): boolean => OPERATORS_BY_TYPE[type].includes(operator)

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

/** Value shape is only meaningful when the operator itself is allowed. */
export const authRuleValueShapeFor = (
  type: AuthAttributeType,
  operator: AuthRuleOperator,
): AuthRuleValueShape | null =>
  isOperatorAllowedForType(type, operator) ? authRuleValueShape(type, operator) : null

export type AuthRuleValueCheck =
  | { ok: true; value: AuthRuleValue }
  | { ok: false; error: AuthRuleValueError }

/**
 * Strictly validate an already-typed value. Never coerces: numbers must be finite and
 * safe integers stay exact, so callers can surface an explicit error instead of rounding.
 */
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
    case 'integer':
      if (typeof value !== 'number') return { ok: false, error: 'invalidValueShape' }
      if (!Number.isFinite(value)) return { ok: false, error: 'invalidNonFinite' }
      if (!Number.isInteger(value)) return { ok: false, error: 'invalidNotInteger' }
      if (!Number.isSafeInteger(value)) return { ok: false, error: 'invalidUnsafeInteger' }
      return { ok: true, value }
    case 'number':
      if (typeof value !== 'number') return { ok: false, error: 'invalidValueShape' }
      if (!Number.isFinite(value)) return { ok: false, error: 'invalidNonFinite' }
      if (Number.isInteger(value) && !Number.isSafeInteger(value))
        return { ok: false, error: 'invalidUnsafeInteger' }
      return { ok: true, value }
    case 'datetime':
      return typeof value === 'string' && isRfc3339WithTimezone(value)
        ? { ok: true, value }
        : { ok: false, error: 'invalidDatetime' }
    case 'list': {
      if (!Array.isArray(value)) return { ok: false, error: 'invalidValueShape' }
      if (value.length === 0) return { ok: false, error: 'invalidListEmpty' }
      if (value.some((item) => typeof item !== 'string')) {
        return { ok: false, error: 'invalidValueShape' }
      }
      if (value.some((item) => item.trim().length === 0)) {
        return { ok: false, error: 'invalidListEntry' }
      }
      return { ok: true, value: [...(value as string[])] }
    }
  }
}

/**
 * Parse raw editor text for the text-backed shapes (string / integer / number / datetime).
 * Deliberately rejects `1e3`-style integers and thousands separators
 * rather than letting `Number()` quietly reinterpret user input.
 */
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
  const days = [31, leapYear ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31]
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
    if (hours > 23 || minutes > 59) return false
  }
  return true
}

/** A single editable condition, including the raw text buffer for text-backed shapes. */
export interface AuthRuleConditionDraft {
  field_id: string
  operator: AuthRuleOperator
  /** Raw input text; the source of truth for string / integer / number / datetime shapes. */
  text: string
  /** Last strictly valid typed value; only ``text``-shaped drafts can be out of sync. */
  value: AuthRuleValue
  /** True for boolean / list shapes, whose controls produce typed values directly. */
  typedValue: boolean
}

export const findAuthAttributeField = (
  fields: readonly AuthAttributeField[],
  fieldId: string,
): AuthAttributeField | undefined => fields.find((field) => field.field_id === fieldId)

export const isKnownAuthAttributeField = (
  fields: readonly AuthAttributeField[],
  fieldId: string,
): boolean => findAuthAttributeField(fields, fieldId) !== undefined

/** Default typed value for a shape, used when a condition is first created. */
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

export const defaultOperatorForType = (type: AuthAttributeType): AuthRuleOperator =>
  OPERATORS_BY_TYPE[type][0]

export const createAuthRuleConditionDraft = (
  field: AuthAttributeField,
  now: Date = new Date(),
): AuthRuleConditionDraft => {
  const operator = defaultOperatorForType(field.type)
  const shape = authRuleValueShape(field.type, operator)
  const value = defaultAuthRuleValue(shape, now)
  return {
    field_id: field.field_id,
    operator,
    text: String(value),
    value,
    typedValue: shape === 'boolean' || shape === 'list',
  }
}

/** Re-target an existing draft onto a new field, keeping compatible edits. */
export const retargetAuthRuleConditionDraft = (
  draft: AuthRuleConditionDraft,
  field: AuthAttributeField,
  now: Date = new Date(),
): AuthRuleConditionDraft => {
  const keepOperator = isOperatorAllowedForType(field.type, draft.operator)
  const operator = keepOperator ? draft.operator : defaultOperatorForType(field.type)
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
    text: String(value),
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

/** Defensive copy so drafts never alias the caller's rule set object. */
export const normalizeAuthRuleValue = (value: AuthRuleValue): AuthRuleValue =>
  Array.isArray(value) ? [...value] : value

/** Validation problem of a single draft, or ``null`` when the condition is usable. */
export const authRuleConditionProblem = (
  draft: AuthRuleConditionDraft,
  fields: readonly AuthAttributeField[],
): AuthRuleConditionProblem | null => {
  const field = findAuthAttributeField(fields, draft.field_id)
  if (!field) return 'unknownField'
  if (!isOperatorAllowedForType(field.type, draft.operator)) return 'operatorNotAllowed'
  const shape = authRuleValueShape(field.type, draft.operator)
  if (draft.typedValue) {
    const check = checkAuthRuleValue(draft.value, shape)
    return check.ok ? null : check.error
  }
  const check = parseAuthRuleText(draft.text, shape)
  if (!check.ok) return check.error
  const typedCheck = checkAuthRuleValue(draft.value, shape)
  return typedCheck.ok ? null : typedCheck.error
}

export interface AuthRuleDraftState {
  mode: AuthRuleMode
  match: AuthRuleMatch
  conditions: AuthRuleConditionDraft[]
}

export interface AuthRuleSetProblem {
  scope: 'conditions' | 'condition'
  code: string
  index?: number
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
  if (value.mode === 'unrestricted') {
    return { mode: 'unrestricted', match: 'all', conditions: [] }
  }
  return {
    mode: 'conditions',
    match: value.match,
    conditions: value.conditions.map((condition) => authRuleConditionToDraft(condition)),
  }
}

/**
 * Validate a rule set against the attribute catalog. Returns every problem instead of
 * the first one so the editor can annotate each offending condition.
 */
export const validateAuthRuleSet = (
  value: AuthRuleSet,
  fields: readonly AuthAttributeField[],
): AuthRuleSetProblem[] => {
  if (value.mode === 'unrestricted') return []
  const problems: AuthRuleSetProblem[] = []
  if (value.conditions.length < AUTH_RULE_MIN_CONDITIONS) {
    problems.push({ scope: 'conditions', code: 'emptyConditions' })
  }
  if (value.conditions.length > AUTH_RULE_MAX_CONDITIONS) {
    problems.push({ scope: 'conditions', code: 'conditionLimitReached' })
  }
  value.conditions.forEach((condition, index) => {
    const field = findAuthAttributeField(fields, condition.field_id)
    if (!field) {
      problems.push({ scope: 'condition', code: 'unknownField', index })
      return
    }
    if (!isOperatorAllowedForType(field.type, condition.operator)) {
      problems.push({ scope: 'condition', code: 'operatorNotAllowed', index })
      return
    }
    const shape = authRuleValueShape(field.type, condition.operator)
    const check = checkAuthRuleValue(condition.value, shape)
    if (!check.ok) problems.push({ scope: 'condition', code: check.error, index })
  })
  return problems
}

export const validateAuthRuleDraftState = (
  state: AuthRuleDraftState,
  fields: readonly AuthAttributeField[],
): AuthRuleSetProblem[] => {
  if (state.mode === 'unrestricted') return []
  const problems: AuthRuleSetProblem[] = []
  if (state.conditions.length < AUTH_RULE_MIN_CONDITIONS) {
    problems.push({ scope: 'conditions', code: 'emptyConditions' })
  }
  if (state.conditions.length > AUTH_RULE_MAX_CONDITIONS) {
    problems.push({ scope: 'conditions', code: 'conditionLimitReached' })
  }
  state.conditions.forEach((draft, index) => {
    const problem = authRuleConditionProblem(draft, fields)
    if (problem) problems.push({ scope: 'condition', code: problem, index })
  })
  return problems
}
