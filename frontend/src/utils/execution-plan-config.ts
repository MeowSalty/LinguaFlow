import type { ApiSchemas } from '@/api/client'

export type ExecutionRound = ApiSchemas['ExecutionRoundConfig']
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
export function roundCodes(round: ExecutionRound): string[] | undefined {
  if (round.mode === 'adjudicate') return round.adjudicate?.adjudicate_codes
  if (round.mode === 'semantic_qa') return round.semantic_qa?.issue_codes
  if (round.mode === 'revise') return round.revise?.issue_codes
  return undefined
}

export function setRoundCodes(round: ExecutionRound, codes: string[] | undefined): void {
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
  const specified = new WeakMap<ExecutionRound, string[]>()
  return (round: ExecutionRound, mode: 'default' | 'specified'): void => {
    if (mode === 'default') {
      const codes = roundCodes(round)
      if (Array.isArray(codes)) specified.set(round, [...codes])
      setRoundCodes(round, undefined)
    } else setRoundCodes(round, [...(specified.get(round) ?? roundCodes(round) ?? [])])
  }
}

export function validateRoundCodes(round: ExecutionRound): 'required' | 'invalid' | undefined {
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
export function buildExecutionRoundInput(round: ExecutionRound): ExecutionRound {
  if (validateRoundCodes(round)) throw new Error('Invalid round issue codes')
  const result: ExecutionRound = {
    mode: round.mode,
    concurrency: round.mode === 'correct' ? 1 : round.concurrency,
  }
  if (round.mode !== 'correct') result.backend_id = round.backend_id
  const clone = <T>(value: T): T => JSON.parse(JSON.stringify(value))
  if (round.mode === 'translate' && round.translate) result.translate = clone(round.translate)
  if (round.mode === 'extract' && round.extract) result.extract = clone(round.extract)
  if (round.mode === 'adjudicate' && round.adjudicate) result.adjudicate = clone(round.adjudicate)
  if (round.mode === 'semantic_qa' && round.semantic_qa)
    result.semantic_qa = clone(round.semantic_qa)
  if (round.mode === 'revise' && round.revise) result.revise = clone(round.revise)
  if (round.mode === 'correct' && round.correct) result.correct = clone(round.correct)
  return result
}
