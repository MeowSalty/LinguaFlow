import type { ApiSchemas } from '@/api/client'

export type ProfileConfig = ApiSchemas['ExecutionProfileConfig']
export type ProfileConfigInput = ApiSchemas['ExecutionProfileConfigInput']

export const QA_CHECKS = [
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
] as const

/** These defaults create a new draft only. Never repair a server response with them. */
export function createProfileConfig(): ProfileConfig {
  return {
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
  }
}

type Field = { required?: boolean } & (
  | { kind: 'boolean' }
  | { kind: 'number'; minimum?: number; integer?: boolean; values?: readonly number[] }
  | { kind: 'string'; values: readonly string[] }
  | { kind: 'array'; values: readonly string[] }
  | { kind: 'object'; fields: Record<string, Field> }
)
const bool: Field = { kind: 'boolean', required: true }
const number = (minimum: number, integer = false): Field => ({
  kind: 'number',
  minimum,
  integer,
  required: true,
})
const object = (fields: Record<string, Field>, required = true): Field => ({
  kind: 'object',
  fields,
  required,
})
const schema = object({
  schema_version: { kind: 'number', values: [1], integer: true, required: true },
  protect: object({
    enabled: bool,
    rules: { kind: 'array', values: ['code', 'link', 'placeholder', 'xml'] },
  }),
  ruby: object(
    {
      enabled: bool,
      preserve_kinds: { kind: 'array', values: ['phonetic', 'semantic', 'creative'] },
    },
    false,
  ),
  postprocess: object({ enabled: bool, trim_spaces: bool }),
  repair: object({
    enabled: bool,
    json_structural: bool,
    schema_aliases: bool,
    placeholder_normalize: bool,
    prompt_upgrade: bool,
  }),
  context: object({
    enabled: bool,
    before: number(0, true),
    after: number(0, true),
    max_chars: number(0, true),
  }),
  qa: object(
    {
      enabled: bool,
      auto_reject: { kind: 'boolean' },
      checks: { kind: 'array', values: QA_CHECKS },
      length_method: { kind: 'string', values: ['char_weight', 'word_count'] },
      length_ratio_min: { kind: 'number', minimum: 0 },
      length_ratio_max: { kind: 'number', minimum: 0 },
    },
    false,
  ),
})

export interface ProfileConfigError {
  path: string
  reason: 'version' | 'missing' | 'invalid' | 'range'
}
export type ProfileConfigRead =
  | { ok: true; config: ProfileConfig }
  | { ok: false; errors: ProfileConfigError[] }

/** Validate the response boundary, preserving absent optional fields and explicit empty arrays. */
export function readProfileConfig(value: unknown): ProfileConfigRead {
  const errors: ProfileConfigError[] = []
  const visit = (input: unknown, field: Field, path: string): unknown => {
    if (input === undefined) {
      if (field.required)
        errors.push({ path, reason: path === 'schema_version' ? 'version' : 'missing' })
      return undefined
    }
    let valid = true
    if (field.kind === 'object') {
      if (input === null || typeof input !== 'object' || Array.isArray(input)) valid = false
      else {
        const result: Record<string, unknown> = {}
        for (const [key, child] of Object.entries(field.fields)) {
          const next = visit(
            (input as Record<string, unknown>)[key],
            child,
            path ? `${path}.${key}` : key,
          )
          if (next !== undefined) result[key] = next
        }
        return result
      }
    } else if (field.kind === 'boolean') valid = typeof input === 'boolean'
    else if (field.kind === 'number')
      valid =
        typeof input === 'number' &&
        Number.isFinite(input) &&
        (!field.integer || Number.isInteger(input)) &&
        (field.minimum === undefined || input >= field.minimum) &&
        (!field.values || field.values.includes(input))
    else if (field.kind === 'string')
      valid = typeof input === 'string' && field.values.includes(input)
    else
      valid =
        Array.isArray(input) &&
        input.every((item) => typeof item === 'string' && field.values.includes(item))
    if (!valid) errors.push({ path, reason: path === 'schema_version' ? 'version' : 'invalid' })
    return Array.isArray(input) ? [...input] : input
  }
  const config = visit(value, schema, '') as ProfileConfig
  if (errors.length === 0 && config.qa?.enabled) {
    const min = config.qa.length_ratio_min ?? 0.2
    const max = config.qa.length_ratio_max ?? 3
    if (min > 0 && max > 0 && min > max)
      errors.push({ path: 'qa.length_ratio_min', reason: 'range' })
  }
  return errors.length ? { ok: false, errors } : { ok: true, config }
}

/** The reader whitelists known fields, so a response's extra fields never become input. */
export function buildProfileConfigInput(
  draft: unknown,
  original?: ProfileConfig,
): ProfileConfigInput {
  const read = readProfileConfig(draft)
  if (!read.ok) throw new Error('Invalid profile configuration')
  if (!original) return read.config
  if (!readProfileConfig(original).ok)
    throw new Error('Incompatible original profile configuration')
  const diff = (next: unknown, before: unknown): unknown => {
    if (JSON.stringify(next) === JSON.stringify(before)) return undefined
    if (next !== null && typeof next === 'object' && !Array.isArray(next)) {
      const result: Record<string, unknown> = {}
      for (const [key, value] of Object.entries(next)) {
        const delta = diff(
          value,
          before && typeof before === 'object'
            ? (before as Record<string, unknown>)[key]
            : undefined,
        )
        if (delta !== undefined) result[key] = delta
      }
      return Object.keys(result).length ? result : undefined
    }
    return next
  }
  return { ...(diff(read.config, original) as ProfileConfigInput | undefined), schema_version: 1 }
}
