import { describe, expect, it } from 'vitest'
import {
  buildRubyRetryInput,
  cloneExecutionPlanValue,
  createExecutionPlanRound,
  createRubyRetryConfig,
  mergeRubyRetryConfig,
  RUBY_BATCH_FIELDS,
  RUBY_BATCH_WAIT_MAX_MS,
  sameRubyRetryConfig,
  validateRubyRetryConfig,
  type ExecutionPlanFormRubyRetry,
} from '../../src/utils/execution-plan-config'
import { clearUnavailablePlanDependencies } from '../../src/utils/organization-copy'

describe('ruby batching form contract', () => {
  it('keeps new and legacy plans implicit until a setting is provided', () => {
    for (const source of [createRubyRetryConfig(), { enabled: true, backend_id: 7 }]) {
      const draft = mergeRubyRetryConfig(source)
      expect(validateRubyRetryConfig(draft)).toEqual([])
      const payload = buildRubyRetryInput(draft)
      for (const field of RUBY_BATCH_FIELDS) {
        expect(draft).not.toHaveProperty(field)
        expect(payload).not.toHaveProperty(field)
      }
    }
  })

  it.each([
    {},
    { batch_size: 1, max_words_per_batch: 0, batch_wait_ms: 25 },
    { batch_size: 4 },
    { batch_size: 0, max_words_per_batch: 800, batch_wait_ms: 0 },
    { max_words_per_batch: 0, batch_wait_ms: 0 },
    { batch_size: Number.MAX_SAFE_INTEGER, max_words_per_batch: Number.MAX_SAFE_INTEGER },
    { batch_wait_ms: RUBY_BATCH_WAIT_MAX_MS },
  ])('preserves explicit fields through editing and serialization: %j', (settings) => {
    for (const enabled of [false, true]) {
      const source = { enabled, max_attempts: 2, concurrency: 3, ...settings }
      const draft = mergeRubyRetryConfig(source)
      expect(validateRubyRetryConfig(draft)).toEqual([])
      expect(buildRubyRetryInput(draft)).toEqual(source)
      expect(source).toEqual({ enabled, max_attempts: 2, concurrency: 3, ...settings })
    }
  })

  it.each(RUBY_BATCH_FIELDS)('clears %s without materializing its default', (field) => {
    const draft = mergeRubyRetryConfig({
      enabled: true,
      batch_size: 4,
      max_words_per_batch: 800,
      batch_wait_ms: 50,
    })
    for (const empty of [undefined, null]) {
      draft[field] = empty
      const merged = mergeRubyRetryConfig(draft)
      expect(validateRubyRetryConfig(merged)).toEqual([])
      expect(buildRubyRetryInput(merged)).not.toHaveProperty(field)
      for (const other of RUBY_BATCH_FIELDS.filter((item) => item !== field))
        expect(buildRubyRetryInput(merged)[other]).toBe(draft[other])
    }
  })

  it.each([
    { batch_size: 0 },
    { batch_size: 0, max_words_per_batch: 0 },
    { batch_size: 0, max_words_per_batch: null },
    { batch_size: 0, max_words_per_batch: undefined },
  ])('rejects unbounded batches after applying omitted defaults: %j', (settings) => {
    for (const enabled of [false, true]) {
      const draft = mergeRubyRetryConfig({ enabled, ...settings })
      expect(validateRubyRetryConfig(draft)).toEqual(['batch_limits'])
      expect(() => buildRubyRetryInput(draft)).toThrow('batch_limits')
      draft.batch_size = null
      expect(validateRubyRetryConfig(draft)).toEqual([])
      expect(buildRubyRetryInput(draft)).not.toHaveProperty('batch_size')
    }
  })

  for (const field of RUBY_BATCH_FIELDS) {
    it.each([
      -1,
      1.5,
      Number.NaN,
      Number.POSITIVE_INFINITY,
      Number.NEGATIVE_INFINITY,
      Number.MAX_SAFE_INTEGER + 1,
      '2',
      false,
    ])(`retains and rejects invalid ${field}=%s, including when disabled`, (invalid) => {
      for (const enabled of [false, true]) {
        const source = { enabled, [field]: invalid } as ExecutionPlanFormRubyRetry
        const draft = mergeRubyRetryConfig(cloneExecutionPlanValue(source))
        expect(draft[field]).toBe(invalid)
        expect(validateRubyRetryConfig(draft)).toEqual([field])
        expect(() => buildRubyRetryInput(draft)).toThrow(field)
      }
    })
  }

  it('rejects a duration above the API limit and returns field-specific errors together', () => {
    const draft = mergeRubyRetryConfig({
      enabled: false,
      concurrency: 0,
      batch_size: -1,
      max_words_per_batch: 0.1,
      batch_wait_ms: RUBY_BATCH_WAIT_MAX_MS + 1,
    })
    expect(validateRubyRetryConfig(draft)).toEqual([
      'concurrency',
      'batch_size',
      'max_words_per_batch',
      'batch_wait_ms',
    ])
    expect(() => buildRubyRetryInput(draft)).toThrow('batch_wait_ms')
  })

  it.each(RUBY_BATCH_FIELDS)('detects changes to %s independently of the other fields', (field) => {
    const before = mergeRubyRetryConfig({ enabled: true, concurrency: 4 })
    const after = mergeRubyRetryConfig({ ...before, [field]: 0 })
    expect(sameRubyRetryConfig(before, after)).toBe(false)
    expect(sameRubyRetryConfig(after, { ...after })).toBe(true)
    expect(sameRubyRetryConfig(after, { ...after, [field]: null })).toBe(false)
    expect(sameRubyRetryConfig(after, { ...after, [field]: Number.NaN })).toBe(false)
    expect(
      sameRubyRetryConfig({ ...after, [field]: Number.NaN }, { ...after, [field]: Number.NaN }),
    ).toBe(true)
  })

  it.each([
    {},
    { batch_size: 0, max_words_per_batch: 800, batch_wait_ms: 0 },
    { batch_size: null, max_words_per_batch: null, batch_wait_ms: null },
    { batch_size: Number.NaN, batch_wait_ms: Number.POSITIVE_INFINITY },
  ])('retains batching values when inaccessible copy dependencies are cleared: %j', (settings) => {
    const source = {
      profile_id: 2,
      ruby_retry: mergeRubyRetryConfig({ enabled: true, backend_id: 5, ...settings }),
      rounds: [{ ...createExecutionPlanRound(), backend_id: 5 }],
    }
    const before = cloneExecutionPlanValue(source)
    const copy = clearUnavailablePlanDependencies(source, {
      profiles: [],
      backends: [],
      prompts: [],
      bootstrap: [],
    })
    expect(source).toEqual(before)
    expect(copy.ruby_retry).not.toBe(source.ruby_retry)
    expect(copy.ruby_retry.backend_id).toBeNull()
    expect(copy.profile_id).toBeNull()
    for (const field of RUBY_BATCH_FIELDS)
      expect(copy.ruby_retry[field]).toBe(source.ruby_retry[field])
    expect(validateRubyRetryConfig(copy.ruby_retry)).toEqual(
      validateRubyRetryConfig(source.ruby_retry),
    )
  })
})
