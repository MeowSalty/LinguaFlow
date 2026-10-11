import { describe, expect, it } from 'vitest'
import {
  buildProfileConfigInput,
  createProfileConfig,
  QA_CHECKS,
  readProfileConfig,
} from '../../src/utils/execution-profile-config'
import {
  ADJUDICATE_CODES,
  buildExecutionRoundInput,
  buildRubyRetryInput,
  cloneExecutionPlanValue,
  createExecutionPlanRound,
  createInlineTermExtractionConfig,
  createRoundCodeSelection,
  createRoundModeSelection,
  createRubyRetryConfig,
  mergeInlineTermExtractionConfig,
  mergeRubyRetryConfig,
  mergeTranslateRoundConfig,
  planUsesTermExtraction,
  REVISE_CODES,
  roundCodes,
  SEMANTIC_QA_CODES,
  setRoundCodes,
  validateRoundCodes,
  validateInlineTermExtractionConfig,
  validateRubyRetryConfig,
  type ExecutionPlanFormRound,
  type InlineTermExtractionConfig,
  type ExecutionRound,
  type ExecutionPlanFormRubyRetry,
} from '../../src/utils/execution-plan-config'
import { clearUnavailablePlanDependencies } from '../../src/utils/organization-copy'

describe('ruby retry concurrency configuration', () => {
  it('keeps new and legacy plans implicit without inheriting main round concurrency', () => {
    const created = createRubyRetryConfig()
    expect(created).not.toHaveProperty('concurrency')
    expect(created).not.toBe(createRubyRetryConfig())
    const legacy = mergeRubyRetryConfig({ enabled: true, max_attempts: 3, backend_id: 5 })
    expect(legacy).not.toHaveProperty('concurrency')
    expect(buildRubyRetryInput(legacy)).toEqual({ enabled: true, max_attempts: 3, backend_id: 5 })
    expect(buildRubyRetryInput(created)).toEqual({ enabled: false, max_attempts: 1 })
    expect(createExecutionPlanRound().concurrency).toBe(3)
  })

  it.each([1, 4, 101, 10000])(
    'preserves explicit concurrency %s through edit and serialization',
    (value) => {
      const source = { enabled: false, concurrency: value, max_attempts: 2, backend_id: 7 }
      const draft = mergeRubyRetryConfig(source)
      expect(validateRubyRetryConfig(draft)).toEqual([])
      expect(buildRubyRetryInput(draft)).toEqual(source)
      draft.concurrency = null
      expect(buildRubyRetryInput(draft)).not.toHaveProperty('concurrency')
      expect(source.concurrency).toBe(value)
    },
  )

  it.each([undefined, null])('serializes an empty form value %s as omission', (value) => {
    const draft = mergeRubyRetryConfig({ enabled: true, concurrency: value })
    expect(validateRubyRetryConfig(draft)).toEqual([])
    expect(buildRubyRetryInput(draft)).not.toHaveProperty('concurrency')
  })

  for (const enabled of [false, true]) {
    it.each([
      0,
      -1,
      1.5,
      Number.NaN,
      Number.POSITIVE_INFINITY,
      Number.NEGATIVE_INFINITY,
      '2',
      false,
    ])(`rejects explicit invalid concurrency %s while enabled=${enabled}`, (value) => {
      const draft = mergeRubyRetryConfig({
        enabled,
        concurrency: value,
      } as ExecutionPlanFormRubyRetry)
      expect(draft.concurrency).toBe(value)
      expect(validateRubyRetryConfig(draft)).toEqual(['concurrency'])
      expect(() => buildRubyRetryInput(draft)).toThrow('Invalid ruby retry concurrency')
    })
  }

  it.each([undefined, null, 1, 8, Number.NaN, Number.POSITIVE_INFINITY])(
    'retains concurrency %s when copying and clearing inaccessible dependencies',
    (concurrency) => {
      const source = {
        name: 'Copied plan',
        profile_id: 2,
        ruby_retry: mergeRubyRetryConfig({ enabled: true, backend_id: 5, concurrency }),
        rounds: [{ ...createExecutionPlanRound(), backend_id: 5 }],
      }
      const copy = clearUnavailablePlanDependencies(source, {
        profiles: [],
        backends: [],
        prompts: [],
        bootstrap: [],
      })
      expect(copy.ruby_retry.concurrency).toBe(concurrency)
      expect(copy.ruby_retry.backend_id).toBeNull()
      expect(copy.profile_id).toBeNull()
      expect(source.ruby_retry.backend_id).toBe(5)
      expect(copy.ruby_retry).not.toBe(source.ruby_retry)
      if (concurrency == null)
        expect(buildRubyRetryInput(copy.ruby_retry)).not.toHaveProperty('concurrency')
      else if (!Number.isFinite(concurrency))
        expect(() => buildRubyRetryInput(copy.ruby_retry)).toThrow()
      else expect(buildRubyRetryInput(copy.ruby_retry).concurrency).toBe(concurrency)
    },
  )
})

describe('inline term extraction configuration', () => {
  function roundWith(value?: unknown): ExecutionPlanFormRound {
    return {
      ...createExecutionPlanRound(),
      backend_id: 1,
      translate: {
        ...mergeTranslateRoundConfig(),
        prompt_template_id: -1,
        ...(value === undefined
          ? {}
          : { inline_term_extraction: value as InlineTermExtractionConfig }),
      },
    }
  }

  it('keeps omitted extraction absent through draft creation, reading and submission', () => {
    expect(createExecutionPlanRound().translate).not.toHaveProperty('inline_term_extraction')
    const source = roundWith()
    const read = mergeTranslateRoundConfig(source.translate)
    expect(read).not.toHaveProperty('inline_term_extraction')
    expect(buildExecutionRoundInput({ ...source, translate: read }).translate).not.toHaveProperty(
      'inline_term_extraction',
    )
    expect(source.translate).not.toHaveProperty('inline_term_extraction')
  })

  it.each([{}, { enabled: true }, { enabled: false }, { min_source_len: 5 }])(
    'fills contract defaults for a present partial configuration %j',
    (source) => {
      const expected = { ...createInlineTermExtractionConfig(), ...source }
      expect(validateInlineTermExtractionConfig(source)).toEqual([])
      expect(mergeTranslateRoundConfig(roundWith(source).translate).inline_term_extraction).toEqual(
        expected,
      )
      expect(buildExecutionRoundInput(roundWith(source)).translate?.inline_term_extraction).toEqual(
        expected,
      )
    },
  )

  it('preserves disabled parameters, decimal precision and copy independence', () => {
    const config = {
      enabled: false,
      max_terms_per_1000_words: 0.025,
      min_source_len: 4,
      conflict_strategy: 'off' as const,
    }
    const source = roundWith(config)
    const copy = mergeTranslateRoundConfig(source.translate)
    expect(copy.inline_term_extraction).toEqual(config)
    expect(copy.inline_term_extraction).not.toBe(config)
    const output = buildExecutionRoundInput({ ...source, translate: copy })
    expect(output.translate?.inline_term_extraction).toEqual(config)
    copy.inline_term_extraction!.enabled = true
    copy.inline_term_extraction!.max_terms_per_1000_words = 8
    expect(source.translate?.inline_term_extraction).toEqual(config)
    expect(output.translate?.inline_term_extraction).toEqual(config)
    const fresh = createInlineTermExtractionConfig()
    fresh.min_source_len = 9
    expect(createInlineTermExtractionConfig().min_source_len).toBe(2)
  })

  const invalid: [keyof InlineTermExtractionConfig, unknown][] = [
    ['enabled', null],
    ['enabled', 'false'],
    ['enabled', 0],
    ...[0, -1, Number.NaN, Number.POSITIVE_INFINITY, Number.NEGATIVE_INFINITY, null, '3'].map(
      (value): [keyof InlineTermExtractionConfig, unknown] => ['max_terms_per_1000_words', value],
    ),
    ...[0, -1, 1.5, Number.NaN, Number.POSITIVE_INFINITY, null, '2'].map(
      (value): [keyof InlineTermExtractionConfig, unknown] => ['min_source_len', value],
    ),
    ['conflict_strategy', 'unknown'],
    ['conflict_strategy', null],
    ['conflict_strategy', false],
  ]

  it.each(invalid)(
    'rejects an explicit invalid %s value %s even while disabled',
    (field, value) => {
      const config = { ...createInlineTermExtractionConfig(), [field]: value }
      expect(validateInlineTermExtractionConfig(config)).toContain(field)
      const read = mergeTranslateRoundConfig(roundWith(config).translate)
      expect(read.inline_term_extraction?.[field]).toBe(value)
      expect(() => buildExecutionRoundInput(roundWith(config))).toThrow(
        'Invalid inline term extraction',
      )
      expect(() => buildExecutionRoundInput({ ...roundWith(), translate: read })).toThrow(
        'Invalid inline term extraction',
      )
    },
  )

  it.each([null, false, [], 3])(
    'rejects malformed configuration %j without defaulting it',
    (value) => {
      expect(validateInlineTermExtractionConfig(value)).toEqual(['config'])
      const read = mergeTranslateRoundConfig(roundWith(value).translate)
      expect(validateInlineTermExtractionConfig(read.inline_term_extraction)).toEqual(['config'])
      expect(() => buildExecutionRoundInput({ ...roundWith(), translate: read })).toThrow()
    },
  )

  it('only serializes known extraction properties', () => {
    const config = { ...createInlineTermExtractionConfig(), old_field: 10 }
    expect(mergeInlineTermExtractionConfig(config)).not.toHaveProperty('old_field')
    expect(
      buildExecutionRoundInput(roundWith(config)).translate?.inline_term_extraction,
    ).not.toHaveProperty('old_field')
  })

  it('identifies extraction only in active modes', () => {
    expect(planUsesTermExtraction(undefined)).toBe(false)
    expect(planUsesTermExtraction([roundWith(), roundWith({ enabled: false })])).toBe(false)
    expect(planUsesTermExtraction([roundWith({ enabled: true })])).toBe(true)
    expect(planUsesTermExtraction([{ mode: 'extract', concurrency: 1 }])).toBe(true)
    expect(planUsesTermExtraction([{ ...roundWith({ enabled: true }), mode: 'revise' }])).toBe(
      false,
    )
  })
})

describe('round mode drafts', () => {
  function createRound(source: Partial<ExecutionPlanFormRound> = {}): ExecutionPlanFormRound {
    const mode = source.mode ?? 'translate'
    const round: ExecutionPlanFormRound = {
      mode,
      backend_id: source.backend_id ?? 1,
      concurrency: source.concurrency ?? 3,
    }
    if (mode === 'translate')
      round.translate = mergeTranslateRoundConfig({ prompt_template_id: -1 })
    if (mode === 'extract') round.extract = { template_id: -2, min_source_len: 2 }
    if (mode === 'revise') round.revise = {}
    if (mode === 'adjudicate') round.adjudicate = {}
    return round
  }

  it('restores complete drafts by round identity after reordering and submits only the active mode', () => {
    const choose = createRoundModeSelection(createRound)
    const first = createRound()
    first.translate!.inline_term_extraction = {
      ...createInlineTermExtractionConfig(),
      enabled: true,
      max_terms_per_1000_words: 1.25,
    }
    const second = createRound()
    second.translate!.inline_term_extraction = {
      ...createInlineTermExtractionConfig(),
      min_source_len: 7,
    }
    const originalFirst = cloneExecutionPlanValue(first)
    const originalSecond = cloneExecutionPlanValue(second)
    choose(first, 'extract')
    first.backend_id = 8
    first.concurrency = 2
    first.extract!.min_source_len = 9
    const reordered = [second, first]
    choose(reordered[1]!, 'translate')
    expect(first).toEqual(originalFirst)
    expect(second).toEqual(originalSecond)
    expect(buildExecutionRoundInput(first)).not.toHaveProperty('extract')
    choose(first, 'extract')
    expect(first).toMatchObject({ backend_id: 8, concurrency: 2, extract: { min_source_len: 9 } })
    expect(first).not.toHaveProperty('translate')
    expect(buildExecutionRoundInput(first)).not.toHaveProperty('translate')
  })

  it('keeps code selection drafts separate for each mode of the same round', () => {
    const chooseMode = createRoundModeSelection(createRound)
    const chooseCodes = createRoundCodeSelection()
    const round = createRound({ mode: 'revise' })
    round.revise!.issue_codes = ['grammar']
    chooseCodes(round, 'default')
    chooseMode(round, 'adjudicate')
    round.adjudicate!.adjudicate_codes = ['source_residual']
    chooseCodes(round, 'default')
    chooseMode(round, 'revise')
    chooseCodes(round, 'specified')
    expect(roundCodes(round)).toEqual(['grammar'])
    chooseMode(round, 'adjudicate')
    chooseCodes(round, 'specified')
    expect(roundCodes(round)).toEqual(['source_residual'])
  })

  it('does not share discarded drafts with a new editor or a newly loaded round', () => {
    const choose = createRoundModeSelection(createRound)
    const round = createRound()
    round.translate!.inline_term_extraction = { enabled: true }
    choose(round, 'extract')
    const reloaded = cloneExecutionPlanValue(round)
    choose(reloaded, 'translate')
    expect(reloaded.translate).not.toHaveProperty('inline_term_extraction')
    createRoundModeSelection(createRound)(round, 'translate')
    expect(round.translate).not.toHaveProperty('inline_term_extraction')
  })
})

describe('profile configuration contract', () => {
  it('accepts profiles without glossary and discards historical bootstrap without migrating it', () => {
    const current = createProfileConfig()
    expect(readProfileConfig(current)).toEqual({ ok: true, config: current })
    const historical = {
      ...current,
      glossary: { bootstrap: { enabled: true, max_terms_per_1000_chars: 20 } },
    }
    expect(readProfileConfig(historical)).toEqual({ ok: true, config: current })
    expect(buildProfileConfigInput(historical)).not.toHaveProperty('glossary')
    expect(buildProfileConfigInput(historical, historical)).not.toHaveProperty('glossary')
    expect(createExecutionPlanRound().translate).not.toHaveProperty('inline_term_extraction')
    expect(historical.glossary.bootstrap.enabled).toBe(true)
  })
  it('creates independent version 1 drafts matching the domain defaults', () => {
    const draft = createProfileConfig()
    expect(draft).toEqual({
      schema_version: 1,
      protect: { enabled: true, rules: ['code', 'link', 'placeholder', 'xml'] },
      ruby: { enabled: true, preserve_kinds: ['creative'] },
      postprocess: { enabled: true, trim_spaces: true },
      repair: {
        enabled: true,
        json_structural: true,
        schema_aliases: true,
        placeholder_normalize: true,
        prompt_upgrade: true,
      },
      context: { enabled: true, before: 1, after: 1, max_chars: 0 },
      qa: {
        enabled: false,
        auto_reject: false,
        length_method: 'char_weight',
        length_ratio_min: 0.2,
        length_ratio_max: 3,
      },
    })
    draft.protect.rules!.push('code')
    expect(createProfileConfig().protect.rules).toHaveLength(4)
    expect(buildProfileConfigInput(createProfileConfig()).qa).not.toHaveProperty('checks')
  })

  const requiredPaths = [
    'schema_version',
    'protect',
    'protect.enabled',
    'postprocess',
    'postprocess.enabled',
    'postprocess.trim_spaces',
    'repair',
    'repair.enabled',
    'repair.json_structural',
    'repair.schema_aliases',
    'repair.placeholder_normalize',
    'repair.prompt_upgrade',
    'context',
    'context.enabled',
    'context.before',
    'context.after',
    'context.max_chars',
    'ruby.enabled',
    'qa.enabled',
  ]
  function alter(path: string, value: unknown, remove = false) {
    const config = createProfileConfig()
    const parts = path.split('.')
    let target = config as unknown as Record<string, unknown>
    for (const part of parts.slice(0, -1)) target = target[part] as Record<string, unknown>
    if (remove) delete target[parts.at(-1)!]
    else target[parts.at(-1)!] = value
    return config
  }
  it.each(requiredPaths)('rejects missing required response field %s', (path) => {
    expect(readProfileConfig(alter(path, undefined, true)).ok).toBe(false)
    expect(() => buildProfileConfigInput(alter(path, undefined, true))).toThrow()
  })
  it.each([
    ...requiredPaths,
    'ruby',
    'qa',
    'ruby.preserve_kinds',
    'protect.rules',
    'qa.checks',
    'qa.auto_reject',
    'qa.length_method',
    'qa.length_ratio_min',
    'qa.length_ratio_max',
  ])('rejects null rather than defaulting %s', (path) => {
    expect(readProfileConfig(alter(path, null)).ok).toBe(false)
  })
  it.each([
    ['schema_version', 2],
    ['schema_version', '1'],
    ['context.before', -1],
    ['context.after', 0.5],
    ['context.enabled', 0],
    ['qa.auto_reject', 'false'],
    ['ruby.preserve_kinds', ['unknown']],
    ['protect.rules', ['unknown']],
    ['qa.checks', ['unknown']],
    ['qa.length_method', 'unknown'],
    ['qa.length_ratio_max', Number.NaN],
  ])('rejects invalid type, enum or range for %s', (path, value) => {
    expect(readProfileConfig(alter(path as string, value)).ok).toBe(false)
  })
  it('preserves optional groups and optional fields without synthesizing display defaults', () => {
    const source = createProfileConfig()
    delete source.ruby
    delete source.qa
    delete source.protect.rules
    const result = readProfileConfig(source)
    expect(result).toEqual({ ok: true, config: source })
    const draft = structuredClone(source)
    draft.context.enabled = false
    expect(buildProfileConfigInput(draft, source)).toEqual({
      schema_version: 1,
      context: { enabled: false },
    })
    draft.ruby = createProfileConfig().ruby
    expect(buildProfileConfigInput(draft, source).ruby).toEqual({
      enabled: true,
      preserve_kinds: ['creative'],
    })
  })
  it('keeps false, zero and explicit empty arrays through reading, creation and editing', () => {
    const source = createProfileConfig()
    const draft = structuredClone(source)
    draft.context = { enabled: false, before: 0, after: 0, max_chars: 0 }
    draft.ruby!.preserve_kinds = []
    draft.protect.rules = []
    draft.qa!.checks = []
    expect(readProfileConfig(draft)).toEqual({ ok: true, config: draft })
    expect(buildProfileConfigInput(draft)).toEqual(draft)
    expect(buildProfileConfigInput(draft, source)).toEqual({
      schema_version: 1,
      context: { enabled: false, before: 0, after: 0 },
      ruby: { preserve_kinds: [] },
      protect: { rules: [] },
      qa: { checks: [] },
    })
  })
  it('does not change checks when only toggling QA and sends all current checks explicitly', () => {
    const original = createProfileConfig()
    original.qa!.checks = ['untranslated']
    const draft = structuredClone(original)
    draft.qa!.enabled = true
    expect(buildProfileConfigInput(draft, original)).toEqual({
      schema_version: 1,
      qa: { enabled: true },
    })
    draft.qa!.checks = [...QA_CHECKS]
    expect(buildProfileConfigInput(draft, original).qa?.checks).toEqual(QA_CHECKS)
    expect(QA_CHECKS).toHaveLength(20)
  })
  it('strips unknown response fields instead of sending them as input', () => {
    const draft = { ...createProfileConfig(), future_field: 'ignored' }
    expect(buildProfileConfigInput(draft)).not.toHaveProperty('future_field')
    expect(() =>
      buildProfileConfigInput(createProfileConfig(), alter('schema_version', 2)),
    ).toThrow()
  })
})

describe('plan round presence and code constraints', () => {
  const round = (
    mode: 'adjudicate' | 'semantic_qa' | 'revise',
    scope?: string,
  ): ExecutionRound => ({
    mode,
    backend_id: 1,
    concurrency: 1,
    [mode]: { batch_size: 10, ...(scope === undefined ? {} : { segment_scope: scope }) },
  })
  for (const mode of ['adjudicate', 'semantic_qa', 'revise'] as const) {
    const scopes =
      mode === 'adjudicate'
        ? [undefined]
        : mode === 'semantic_qa'
          ? [undefined, 'all', 'with_issues', 'with_issue_codes']
          : [undefined, 'with_issues', 'with_issue_codes']
    for (const scope of scopes) {
      for (const codes of [
        undefined,
        [],
        [mode === 'revise' ? 'mistranslation' : 'source_residual'],
      ]) {
        it(`${mode}/${scope ?? 'omitted scope'}/${codes === undefined ? 'omitted' : JSON.stringify(codes)}`, () => {
          const draft = round(mode, scope)
          setRoundCodes(draft, codes)
          const invalid = scope === 'with_issue_codes' && !codes?.length
          expect(validateRoundCodes(draft)).toBe(invalid ? 'required' : undefined)
          if (invalid) expect(() => buildExecutionRoundInput(draft)).toThrow()
          else {
            const output = buildExecutionRoundInput(draft)
            expect(roundCodes(output)).toEqual(codes)
            expect(
              Object.hasOwn(
                output[mode]!,
                mode === 'adjudicate' ? 'adjudicate_codes' : 'issue_codes',
              ),
            ).toBe(codes !== undefined)
            expect(Object.hasOwn(output[mode]!, 'segment_scope')).toBe(scope !== undefined)
          }
        })
      }
    }
  }
  it('uses distinct complete code allowlists and rejects null/unknown values', () => {
    for (const [mode, allowed] of [
      ['adjudicate', ADJUDICATE_CODES],
      ['semantic_qa', SEMANTIC_QA_CODES],
      ['revise', REVISE_CODES],
    ] as const) {
      const draft = round(mode)
      setRoundCodes(draft, [...allowed])
      expect(validateRoundCodes(draft)).toBeUndefined()
      setRoundCodes(draft, ['unknown'])
      expect(validateRoundCodes(draft)).toBe('invalid')
      setRoundCodes(draft, null as unknown as string[])
      expect(validateRoundCodes(draft)).toBe('invalid')
    }
    const revise = round('revise')
    setRoundCodes(revise, ['source_residual'])
    expect(validateRoundCodes(revise)).toBe('invalid')
  })
  it('remembers specified drafts through default mode and round reordering without implicit codes', () => {
    const choose = createRoundCodeSelection()
    const a = round('revise')
    const b = round('adjudicate')
    choose(a, 'specified')
    expect(roundCodes(a)).toEqual([])
    setRoundCodes(a, ['grammar'])
    choose(a, 'default')
    choose(b, 'specified')
    expect(roundCodes(a)).toBeUndefined()
    const reordered = [b, a]
    choose(reordered[1]!, 'specified')
    expect(roundCodes(reordered[1]!)).toEqual(['grammar'])
    expect(roundCodes(reordered[0]!)).toEqual([])
  })
  it('retains translate segment filters, retry values, and explicit zeroes while removing inactive modes', () => {
    const draft: ExecutionRound = {
      mode: 'translate',
      backend_id: 1,
      concurrency: 1,
      translate: {
        prompt_template_id: 7,
        batch_size: 0,
        max_words_per_batch: 100,
        fallback_shrink: 1,
        segment_filter: { status_filter: 'skip_approved' },
        retry: { max_attempts: 1, backoff_ms: 0, jitter: false },
      },
      revise: { issue_codes: [] },
    }
    const output = buildExecutionRoundInput(draft)
    expect(output.translate).toEqual(draft.translate)
    expect(output).not.toHaveProperty('revise')
    expect(output.translate).not.toBe(draft.translate)
  })
  it('throws when a spec-required template reference is left unselected', () => {
    const translateRound: ExecutionRound = {
      mode: 'translate',
      backend_id: 1,
      concurrency: 1,
      translate: {
        prompt_template_id: null as unknown as number,
        batch_size: 10,
        max_words_per_batch: 0,
        fallback_shrink: 1,
      },
    }
    expect(() => buildExecutionRoundInput(translateRound)).toThrow(
      'Missing translate prompt template',
    )
    const extractRound: ExecutionRound = {
      mode: 'extract',
      backend_id: 1,
      concurrency: 1,
      extract: {
        template_id: null as unknown as number,
        batch_size: 20,
        max_words_per_batch: 0,
      },
    }
    expect(() => buildExecutionRoundInput(extractRound)).toThrow('Missing extract template')
  })
})
