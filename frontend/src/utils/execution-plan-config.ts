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

/** 表单态注音重试：backend_id 以 null 表示未选择（后端语义为回退到翻译主后端）。 */
export type ExecutionPlanFormRubyRetry = Omit<ExecutionPlanRubyRetry, 'backend_id'> & {
  backend_id?: number | null
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
  const specified = new WeakMap<ExecutionPlanFormRound, string[]>()
  return (round: ExecutionPlanFormRound, mode: 'default' | 'specified'): void => {
    if (mode === 'default') {
      const codes = roundCodes(round)
      if (Array.isArray(codes)) specified.set(round, [...codes])
      setRoundCodes(round, undefined)
    } else setRoundCodes(round, [...(specified.get(round) ?? roundCodes(round) ?? [])])
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
  const clone = <T>(value: T): T => JSON.parse(JSON.stringify(value))
  if (round.mode === 'translate' && round.translate) {
    const { prompt_template_id, ...translate } = clone(round.translate)
    if (prompt_template_id == null) throw new Error('Missing translate prompt template')
    result.translate = { ...translate, prompt_template_id }
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

/** Normalize the form-state ruby retry; an unset backend (null) is omitted. */
export function buildRubyRetryInput(retry: ExecutionPlanFormRubyRetry): ExecutionPlanRubyRetry {
  const { backend_id, ...rest } = retry
  return backend_id == null ? rest : { ...rest, backend_id }
}
