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
  createRoundCodeSelection,
  REVISE_CODES,
  roundCodes,
  SEMANTIC_QA_CODES,
  setRoundCodes,
  validateRoundCodes,
  type ExecutionRound,
} from '../../src/utils/execution-plan-config'

describe('profile configuration contract', () => {
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
      glossary: {
        bootstrap: {
          enabled: false,
          max_terms_per_1000_chars: 3,
          min_source_len: 2,
          inline_conflict_strategy: 'rewrite-local',
        },
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
    'glossary',
    'glossary.bootstrap',
    'glossary.bootstrap.enabled',
    'glossary.bootstrap.max_terms_per_1000_chars',
    'glossary.bootstrap.min_source_len',
    'glossary.bootstrap.inline_conflict_strategy',
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
    ['glossary.bootstrap.min_source_len', 0],
    ['glossary.bootstrap.inline_conflict_strategy', 'unknown'],
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
