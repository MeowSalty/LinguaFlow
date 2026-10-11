import type { ApiSchemas } from '@/api/client'

type Stages = ApiSchemas['JobStageCounts']
type Job = ApiSchemas['Job']

const counters = [
  'main_requests',
  'pending_alignment',
  'alignment_requests',
  'saving_requests',
  'ready_to_commit',
  'confirmed_work',
  'unknown_requests',
  'draining_requests',
] as const satisfies readonly (keyof Stages)[]

/** Preserve the API's sub-millisecond precision when REST and SSE race. */
export function jobObservationTime(value: unknown): bigint | null {
  if (typeof value !== 'string') return null
  const match =
    /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.(\d{1,9}))?(Z|[+-]\d{2}:\d{2})$/.exec(
      value,
    )
  if (!match) return null
  const [, year, month, day, hour, minute, second, fraction] = match
  const y = Number(year),
    m = Number(month),
    d = Number(day)
  const leap = y % 4 === 0 && (y % 100 !== 0 || y % 400 === 0)
  const days = [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31]
  const milliseconds = Date.parse(value)
  if (
    !Number.isFinite(milliseconds) ||
    m < 1 ||
    m > 12 ||
    d < 1 ||
    d > days[m - 1]! ||
    Number(hour) > 23 ||
    Number(minute) > 59 ||
    Number(second) > 59
  )
    return null
  return (
    BigInt(Math.floor(milliseconds / 1000)) * 1_000_000_000n +
    BigInt((fraction ?? '').padEnd(9, '0'))
  )
}

/** Metadata is untyped on the wire; malformed observations must not replace good data. */
export function readJobStages(value: unknown): Stages | undefined {
  if (value === null || typeof value !== 'object' || Array.isArray(value)) return undefined
  const record = value as Record<string, unknown>
  if (jobObservationTime(record.as_of) === null) return undefined
  for (const key of counters) {
    if (typeof record[key] !== 'number' || !Number.isSafeInteger(record[key]) || record[key] < 0)
      return undefined
  }
  return Object.fromEntries([
    ...counters.map((key) => [key, record[key]]),
    ['as_of', record.as_of],
  ]) as Stages
}

export function latestJobStages(current: unknown, incoming: unknown): Stages | undefined {
  const previous = readJobStages(current),
    next = readJobStages(incoming)
  if (!next) return previous
  if (!previous) return next
  return jobObservationTime(next.as_of)! > jobObservationTime(previous.as_of)! ? next : previous
}

/** Stage observations never change effective progress or a job's lifecycle state. */
export function mergeJobObservation(current: Job | null, incoming: Job): Job {
  const previous = current?.id === incoming.id ? current : null
  const oldTime = jobObservationTime(previous?.updated_at)
  const nextTime = jobObservationTime(incoming.updated_at)
  const base =
    previous && oldTime !== null && nextTime !== null && oldTime > nextTime ? previous : incoming
  if (!base.progress) return base
  const stages = latestJobStages(previous?.progress?.stages, incoming.progress?.stages)
  const { stages: _stages, ...progress } = base.progress
  return { ...base, progress: stages ? { ...progress, stages } : progress }
}
