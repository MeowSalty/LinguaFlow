import { describe, expect, it } from 'vitest'
import {
  authRuleConditionProblem,
  authRuleDraftsFromSet,
  authRuleOperatorsForType,
  authRuleSetFromDrafts,
  authRuleSetsEqual,
  authRuleValueShape,
  checkAuthRuleValue,
  createAuthRuleConditionDraft,
  defaultOperatorForType,
  isRfc3339WithTimezone,
  parseAuthRuleText,
  retargetAuthRuleConditionDraft,
  validateAuthRuleDraftState,
  validateAuthRuleSet,
  type AuthAttributeField,
  type AuthAttributeType,
  type AuthRuleOperator,
  type AuthRuleSet,
} from '@/components/admin/authentication/auth-rule-model'

const field = (type: AuthAttributeType): AuthAttributeField => ({
  field_id: `custom.${type}`,
  label: type,
  type,
  source: 'oidc_id_token',
  pointer: `/${type}`,
})

describe('authentication rule contract', () => {
  const matrix: [AuthAttributeType, AuthRuleOperator[]][] = [
    ['string', ['eq', 'ne', 'in']],
    ['boolean', ['eq']],
    ['integer', ['eq', 'ne', 'gt', 'gte', 'lt', 'lte']],
    ['number', ['eq', 'ne', 'gt', 'gte', 'lt', 'lte']],
    ['datetime', ['gt', 'gte', 'lt', 'lte']],
    ['string_list', ['contains', 'not_contains', 'contains_any', 'contains_all']],
  ]
  it.each(matrix)('matches backend allowedOperator for %s', (type, operators) => {
    expect(authRuleOperatorsForType(type)).toEqual(operators)
    expect(defaultOperatorForType(type)).toBe(operators[0])
    expect(
      authRuleConditionProblem(createAuthRuleConditionDraft(field(type)), [field(type)]),
    ).toBeNull()
  })
  it('uses typed operands for list membership and string-list containment', () => {
    expect(authRuleValueShape('string', 'in')).toBe('list')
    expect(authRuleValueShape('string_list', 'contains')).toBe('string')
    expect(authRuleValueShape('string_list', 'contains_any')).toBe('list')
    expect(authRuleValueShape('string_list', 'contains_all')).toBe('list')
    expect(checkAuthRuleValue([], 'list')).toEqual({ ok: false, error: 'invalidListEmpty' })
    expect(checkAuthRuleValue(['a', 1], 'list').ok).toBe(false)
    expect(checkAuthRuleValue(['Admin', 'member'], 'list')).toEqual({
      ok: true,
      value: ['Admin', 'member'],
    })
    expect(checkAuthRuleValue('true', 'boolean').ok).toBe(false)
  })
  it.each(['integer', 'number'] as const)('rejects unsafe %s text without rounding', (shape) => {
    for (const text of ['9007199254740992', '9007199254740993', '-9007199254740993']) {
      expect(parseAuthRuleText(text, shape)).toEqual({ ok: false, error: 'invalidUnsafeInteger' })
    }
    expect(parseAuthRuleText('9007199254740991', shape)).toEqual({
      ok: true,
      value: Number.MAX_SAFE_INTEGER,
    })
    expect(checkAuthRuleValue(Infinity, shape).ok).toBe(false)
  })
  it('rejects invalid numeric input and preserves number versus string types', () => {
    for (const value of ['', 'NaN', 'Infinity', '1,000', '0x10']) {
      expect(parseAuthRuleText(value, 'number').ok).toBe(false)
    }
    expect(parseAuthRuleText('1.5', 'integer').ok).toBe(false)
    expect(parseAuthRuleText('1e3', 'integer').ok).toBe(false)
    expect(parseAuthRuleText('1.25', 'number')).toEqual({ ok: true, value: 1.25 })
    expect(parseAuthRuleText('1', 'string')).toEqual({ ok: true, value: '1' })
  })
  it('rejects decimal precision loss and underflow while allowing equivalent decimal notation', () => {
    for (const value of ['0.10000000000000000001', '1e-324', '1.00000000000000001']) {
      expect(parseAuthRuleText(value, 'number')).toEqual({ ok: false, error: 'invalidPrecision' })
    }
    for (const value of ['1.2500', '+001.2500', '12.5e-1']) {
      expect(parseAuthRuleText(value, 'number')).toEqual({ ok: true, value: 1.25 })
    }
  })
  it('requires a real calendar date and RFC3339 timezone', () => {
    for (const value of ['2026-10-13T12:00:00+08:00', '2024-02-29T23:59:59.123456789Z']) {
      expect(isRfc3339WithTimezone(value)).toBe(true)
    }
    for (const value of [
      '2026-02-29T12:00:00Z',
      '2026-02-30T12:00:00Z',
      '2026-04-31T12:00:00Z',
      '2026-10-13T24:00:00Z',
      '2026-10-13T12:00:00',
      '2026-10-13T12:00:00+24:00',
    ]) {
      expect(isRfc3339WithTimezone(value)).toBe(false)
    }
  })
  it('retains unknown field references and values while marking the draft invalid', () => {
    const rules: AuthRuleSet = {
      mode: 'conditions',
      match: 'any',
      conditions: [{ field_id: 'custom.removed', operator: 'in', value: ['A', 'B'] }],
    }
    const draft = authRuleDraftsFromSet(rules)
    expect(authRuleSetFromDrafts(draft)).toEqual(rules)
    expect(validateAuthRuleDraftState(draft, [])).toEqual([
      { scope: 'condition', code: 'unknownField', index: 0 },
    ])
    expect(authRuleSetsEqual(authRuleSetFromDrafts(draft), rules)).toBe(true)
    expect(draft.conditions[0]?.value).not.toBe(rules.conditions[0]?.value)
  })
  it('rejects loaded values of the wrong type instead of treating text coercion as valid', () => {
    const rules: AuthRuleSet = {
      mode: 'conditions',
      match: 'all',
      conditions: [{ field_id: 'custom.integer', operator: 'eq', value: '42' }],
    }
    expect(
      validateAuthRuleDraftState(authRuleDraftsFromSet(rules), [field('integer')])[0]?.code,
    ).toBe('invalidValueShape')
    expect(validateAuthRuleSet(rules, [field('integer')])[0]?.code).toBe('invalidValueShape')
  })
  it('keeps the last safe integer value while an unsafe edit is invalid', () => {
    const draft = createAuthRuleConditionDraft(field('integer'))
    draft.text = '9007199254740993'
    expect(authRuleConditionProblem(draft, [field('integer')])).toBe('invalidUnsafeInteger')
    expect(draft.value).toBe(0)
    const next = retargetAuthRuleConditionDraft(draft, field('boolean'))
    expect(next.operator).toBe('eq')
    expect(typeof next.value).toBe('boolean')
  })
  it('validates rule limits, disallowed operators and unrestricted mode', () => {
    expect(validateAuthRuleSet({ mode: 'unrestricted' }, [])).toEqual([])
    expect(
      validateAuthRuleSet({ mode: 'conditions', match: 'all', conditions: [] }, [
        field('string'),
      ])[0]?.code,
    ).toBe('emptyConditions')
    const condition = { field_id: 'custom.boolean', operator: 'ne' as const, value: true }
    expect(
      validateAuthRuleSet({ mode: 'conditions', match: 'all', conditions: [condition] }, [
        field('boolean'),
      ])[0]?.code,
    ).toBe('operatorNotAllowed')
    expect(
      validateAuthRuleSet(
        {
          mode: 'conditions',
          match: 'all',
          conditions: Array.from({ length: 65 }, () => condition),
        },
        [field('boolean')],
      )[0]?.code,
    ).toBe('conditionLimitReached')
    expect(
      authRuleSetsEqual(
        { mode: 'unrestricted' },
        { mode: 'conditions', match: 'all', conditions: [] },
      ),
    ).toBe(false)
  })
})
