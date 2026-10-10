import type { ApiSchemas } from '@/api/client'

export type ExecutionRound = ApiSchemas['ExecutionRoundConfig']
export type ExecutionPlanRubyRetry = ApiSchemas['ExecutionPlanRubyRetryConfig']

/**
 * 表单态轮次：依赖引用（backend_id / prompt_template_id / template_id）以 null 表示未选择，
 * 与 NSelect 清空行为对齐；提交时经 buildExecutionRoundInput 归一化为省略（backend_id）或真实 ID。
 * prompt_template_id 与 template_id 规范必填（省略/0 会被后端拒绝），未选择状态只能存在于表单。
 */
export type ExecutionPlanFormRound = Omit<
  ExecutionRound,
  'backend_id' | 'translate' | 'extract'
> & {
  backend_id?: number | null
  translate?: Omit<NonNullable<ExecutionRound['translate']>, 'prompt_template_id'> & {
    prompt_template_id: number | null
  }
  extract?: Omit<NonNullable<ExecutionRound['extract']>, 'template_id'> & {
    template_id: number | null
  }
}

/** 表单中的 null 表示未选择后端或使用服务端默认注音并发，提交时均省略。 */
export type ExecutionPlanFormRubyRetry = Omit<
  ExecutionPlanRubyRetry,
  'backend_id' | 'concurrency'
> & {
  backend_id?: number | null
  concurrency?: number | null
}

export type InlineTermExtractionConfig = ApiSchemas['InlineTermExtractionConfig']
export type InlineTermExtractionError = keyof InlineTermExtractionConfig | 'config'
type FormTranslateConfig = NonNullable<ExecutionPlanFormRound['translate']>

/** Clone form values without turning invalid numbers into null before validation. */
export function cloneExecutionPlanValue<T>(value: T): T {
  if (Array.isArray(value)) return value.map((item) => cloneExecutionPlanValue(item)) as T
  if (value !== null && typeof value === 'object')
    return Object.fromEntries(
      Object.entries(value).map(([key, item]) => [key, cloneExecutionPlanValue(item)]),
    ) as T
  return value
}

export function createRubyRetryConfig(): ExecutionPlanFormRubyRetry {
  return { enabled: false, backend_id: null, max_attempts: 1 }
}

/** Preserve omitted concurrency and explicit invalid values until validation. */
export function mergeRubyRetryConfig(
  source?: Partial<ExecutionPlanFormRubyRetry>,
): ExecutionPlanFormRubyRetry {
  const defaults = createRubyRetryConfig()
  return {
    enabled: source?.enabled ?? defaults.enabled,
    backend_id: source?.backend_id ?? defaults.backend_id,
    max_attempts: source?.max_attempts ?? defaults.max_attempts,
    ...(source?.concurrency === undefined ? {} : { concurrency: source.concurrency }),
  }
}

export function validateRubyRetryConfig(retry: ExecutionPlanFormRubyRetry): 'concurrency'[] {
  const value = retry.concurrency
  return value != null &&
    (typeof value !== 'number' || !Number.isFinite(value) || !Number.isInteger(value) || value < 1)
    ? ['concurrency']
    : []
}

export function createInlineTermExtractionConfig(): Required<InlineTermExtractionConfig> {
  return {
    enabled: false,
    max_terms_per_1000_words: 3,
    min_source_len: 2,
    conflict_strategy: 'rewrite-local',
  }
}

/** Fill only missing properties. Explicit invalid values remain visible to validation. */
export function mergeInlineTermExtractionConfig(
  source: InlineTermExtractionConfig,
): InlineTermExtractionConfig {
  if (source === null || typeof source !== 'object' || Array.isArray(source)) return source
  const defaults = createInlineTermExtractionConfig()
  return {
    enabled: source.enabled === undefined ? defaults.enabled : source.enabled,
    max_terms_per_1000_words:
      source.max_terms_per_1000_words === undefined
        ? defaults.max_terms_per_1000_words
        : source.max_terms_per_1000_words,
    min_source_len:
      source.min_source_len === undefined ? defaults.min_source_len : source.min_source_len,
    conflict_strategy:
      source.conflict_strategy === undefined
        ? defaults.conflict_strategy
        : source.conflict_strategy,
  }
}

export function validateInlineTermExtractionConfig(value: unknown): InlineTermExtractionError[] {
  if (value === undefined) return []
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return ['config']
  const config = value as Record<string, unknown>
  const errors: InlineTermExtractionError[] = []
  if (config.enabled !== undefined && typeof config.enabled !== 'boolean') errors.push('enabled')
  const density = config.max_terms_per_1000_words
  if (
    density !== undefined &&
    (typeof density !== 'number' || !Number.isFinite(density) || density <= 0)
  )
    errors.push('max_terms_per_1000_words')
  const minLength = config.min_source_len
  if (
    minLength !== undefined &&
    (typeof minLength !== 'number' || !Number.isInteger(minLength) || minLength < 1)
  )
    errors.push('min_source_len')
  if (
    config.conflict_strategy !== undefined &&
    config.conflict_strategy !== 'off' &&
    config.conflict_strategy !== 'rewrite-local'
  )
    errors.push('conflict_strategy')
  return errors
}

export function mergeTranslateRoundConfig(
  source?: Partial<FormTranslateConfig>,
): FormTranslateConfig {
  return {
    prompt_template_id: source?.prompt_template_id ?? null,
    batch_size: source?.batch_size ?? 10,
    max_words_per_batch: source?.max_words_per_batch ?? 0,
    fallback_shrink: source?.fallback_shrink ?? 1,
    segment_filter: { status_filter: source?.segment_filter?.status_filter ?? 'pending_only' },
    retry: {
      max_attempts: source?.retry?.max_attempts ?? 3,
      backoff_ms: source?.retry?.backoff_ms ?? 2000,
      jitter: source?.retry?.jitter ?? true,
    },
    ...(source?.inline_term_extraction === undefined
      ? {}
      : { inline_term_extraction: mergeInlineTermExtractionConfig(source.inline_term_extraction) }),
  }
}

export function createExecutionPlanRound(): ExecutionPlanFormRound {
  return {
    mode: 'translate',
    backend_id: null,
    concurrency: 3,
    translate: mergeTranslateRoundConfig(),
  }
}

export function planUsesTermExtraction(
  rounds: readonly ExecutionPlanFormRound[] | undefined,
): boolean {
  return Boolean(
    rounds?.some(
      (round) =>
        round.mode === 'extract' ||
        (round.mode === 'translate' && round.translate?.inline_term_extraction?.enabled === true),
    ),
  )
}

/** Drafts are private to an editor and follow a round through reorder and mode changes. */
export function createRoundModeSelection(
  createRound: (source?: Partial<ExecutionPlanFormRound>) => ExecutionPlanFormRound,
) {
  const drafts = new WeakMap<
    ExecutionPlanFormRound,
    Map<ExecutionRound['mode'], ExecutionPlanFormRound>
  >()
  return (round: ExecutionPlanFormRound, mode: ExecutionRound['mode']): void => {
    if (round.mode === mode) return
    const saved = drafts.get(round) ?? new Map<ExecutionRound['mode'], ExecutionPlanFormRound>()
    saved.set(round.mode, cloneExecutionPlanValue(round))
    drafts.set(round, saved)
    const next = cloneExecutionPlanValue(
      saved.get(mode) ??
        createRound({ mode, backend_id: round.backend_id, concurrency: round.concurrency }),
    )
    delete round.translate
    delete round.extract
    delete round.adjudicate
    delete round.semantic_qa
    delete round.revise
    delete round.correct
    Object.assign(round, next)
  }
}

export const ADJUDICATE_CODES = [
  'source_residual',
  'length_ratio',
  'punctuation_surplus',
  'untranslated',
] as const
export const REVISE_CODES = [
  'calque',
  'term_fidelity',
  'naturalness',
  'mistranslation',
  'omission',
  'addition',
  'grammar',
  'register',
] as const
export const SEMANTIC_QA_CODES = [
  'untranslated',
  'length_ratio',
  'duplicate',
  'source_residual',
  'punctuation_pairing',
  'punctuation_missing',
  'punctuation_surplus',
  'punctuation_wrap_loss',
  'whitespace_irregular',
  'repeated_space',
  'width_mix',
  'script_mismatch',
  'number_mismatch',
  'url_email_mismatch',
  'subtitle_line_count',
  'forbidden_term',
  'term_inconsistency',
  'leftover_placeholder',
  'xml_tag_mismatch',
  'duplicate_source_divergence',
  ...REVISE_CODES,
  'ruby_restore_incomplete',
  'ruby_tag_loss',
] as const

export type CodeRound = Extract<ExecutionRound['mode'], 'adjudicate' | 'semantic_qa' | 'revise'>
export function roundCodes(round: ExecutionPlanFormRound): string[] | undefined {
  if (round.mode === 'adjudicate') return round.adjudicate?.adjudicate_codes
  if (round.mode === 'semantic_qa') return round.semantic_qa?.issue_codes
  if (round.mode === 'revise') return round.revise?.issue_codes
  return undefined
}

export function setRoundCodes(round: ExecutionPlanFormRound, codes: string[] | undefined): void {
  if (round.mode === 'adjudicate' && round.adjudicate)
    round.adjudicate.adjudicate_codes = codes as NonNullable<
      ExecutionRound['adjudicate']
    >['adjudicate_codes']
  if (round.mode === 'semantic_qa' && round.semantic_qa)
    round.semantic_qa.issue_codes = codes as NonNullable<
      ExecutionRound['semantic_qa']
    >['issue_codes']
  if (round.mode === 'revise' && round.revise)
    round.revise.issue_codes = codes as NonNullable<ExecutionRound['revise']>['issue_codes']
}

/** Presence lives in the DTO; this cache only remembers the user's unsubmitted specified draft. */
export function createRoundCodeSelection() {
  const specified = new WeakMap<ExecutionPlanFormRound, Partial<Record<CodeRound, string[]>>>()
  return (round: ExecutionPlanFormRound, mode: 'default' | 'specified'): void => {
    if (round.mode !== 'adjudicate' && round.mode !== 'semantic_qa' && round.mode !== 'revise')
      return
    const drafts = specified.get(round) ?? {}
    if (mode === 'default') {
      const codes = roundCodes(round)
      if (Array.isArray(codes)) {
        drafts[round.mode] = [...codes]
        specified.set(round, drafts)
      }
      setRoundCodes(round, undefined)
    } else setRoundCodes(round, [...(drafts[round.mode] ?? roundCodes(round) ?? [])])
  }
}

export function validateRoundCodes(
  round: ExecutionPlanFormRound,
): 'required' | 'invalid' | undefined {
  if (round.mode !== 'adjudicate' && round.mode !== 'semantic_qa' && round.mode !== 'revise')
    return undefined
  const codes = roundCodes(round)
  const scope =
    round.mode === 'semantic_qa' ? round.semantic_qa?.segment_scope : round.revise?.segment_scope
  const scopes =
    round.mode === 'semantic_qa'
      ? ['all', 'with_issues', 'with_issue_codes']
      : ['with_issues', 'with_issue_codes']
  if (scope !== undefined && !scopes.includes(scope)) return 'invalid'
  if (
    scope === 'with_issue_codes' &&
    (codes === undefined || (Array.isArray(codes) && codes.length === 0))
  )
    return 'required'
  const allowed: readonly string[] =
    round.mode === 'adjudicate'
      ? ADJUDICATE_CODES
      : round.mode === 'semantic_qa'
        ? SEMANTIC_QA_CODES
        : REVISE_CODES
  if (
    codes !== undefined &&
    (!Array.isArray(codes) || codes.some((code) => !allowed.includes(code)))
  )
    return 'invalid'
  return undefined
}

/** Serialize only the active round. An omitted array and [] remain distinct in every scope. */
export function buildExecutionRoundInput(round: ExecutionPlanFormRound): ExecutionRound {
  if (validateRoundCodes(round)) throw new Error('Invalid round issue codes')
  const result: ExecutionRound = {
    mode: round.mode,
    concurrency: round.mode === 'correct' ? 1 : round.concurrency,
  }
  // backend_id 未选择（null）时省略；prompt/template 规范必填（省略/0 被后端拒绝），
  // 未选择由表单校验拦截，此处为与轮次代码校验同级的兜底
  if (round.mode !== 'correct' && round.backend_id != null) result.backend_id = round.backend_id
  // Wire objects omit undefined properties; validate extraction before JSON can coerce numbers.
  const clone = <T>(value: T): T => JSON.parse(JSON.stringify(value))
  if (round.mode === 'translate' && round.translate) {
    if (validateInlineTermExtractionConfig(round.translate.inline_term_extraction).length)
      throw new Error('Invalid inline term extraction configuration')
    const { prompt_template_id, inline_term_extraction, ...translate } = clone(round.translate)
    if (prompt_template_id == null) throw new Error('Missing translate prompt template')
    result.translate = {
      ...translate,
      prompt_template_id,
      ...(inline_term_extraction === undefined
        ? {}
        : { inline_term_extraction: mergeInlineTermExtractionConfig(inline_term_extraction) }),
    }
  }
  if (round.mode === 'extract' && round.extract) {
    const { template_id, ...extract } = clone(round.extract)
    if (template_id == null) throw new Error('Missing extract template')
    result.extract = { ...extract, template_id }
  }
  if (round.mode === 'adjudicate' && round.adjudicate) result.adjudicate = clone(round.adjudicate)
  if (round.mode === 'semantic_qa' && round.semantic_qa)
    result.semantic_qa = clone(round.semantic_qa)
  if (round.mode === 'revise' && round.revise) result.revise = clone(round.revise)
  if (round.mode === 'correct' && round.correct) result.correct = clone(round.correct)
  return result
}

/** Validate before serializing; cleared fields remain omitted, never materialized as defaults. */
export function buildRubyRetryInput(retry: ExecutionPlanFormRubyRetry): ExecutionPlanRubyRetry {
  if (validateRubyRetryConfig(retry).length) throw new Error('Invalid ruby retry concurrency')
  return {
    enabled: retry.enabled,
    ...(retry.backend_id == null ? {} : { backend_id: retry.backend_id }),
    ...(retry.max_attempts === undefined ? {} : { max_attempts: retry.max_attempts }),
    ...(retry.concurrency == null ? {} : { concurrency: retry.concurrency }),
  }
}
