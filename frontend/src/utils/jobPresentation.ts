import type { ApiSchemas } from '@/api/client'

type Job = ApiSchemas['Job']
type Resource = ApiSchemas['JobResource']
type Round = ApiSchemas['JobResourceRound']

export type RoundDisplayState = Round['status'] | 'paused' | 'stopped' | 'not_run'
export type JobEventFilter = 'all' | 'anomalies'

export const isJobTerminal = (status: Job['status']): boolean =>
  status === 'completed' || status === 'failed' || status === 'cancelled'

/** Preserve historical outcomes; only unfinished rounds inherit their parent's stop state. */
export const getRoundDisplayState = (
  jobStatus: Job['status'],
  resourceStatus: Resource['status'],
  roundStatus: Round['status'],
): RoundDisplayState => {
  if (roundStatus === 'completed' || roundStatus === 'failed' || roundStatus === 'skipped') {
    return roundStatus
  }
  const stopped =
    isJobTerminal(jobStatus) || ['completed', 'failed', 'cancelled'].includes(resourceStatus)
  if (roundStatus === 'pending') return stopped ? 'not_run' : 'pending'
  if (stopped) return 'stopped'
  if (jobStatus === 'paused') return 'paused'
  return jobStatus === 'running' && resourceStatus === 'running' ? 'running' : 'pending'
}

/** A failed resource points at its failure; a live recovery points at its next round. */
export const selectResourceRound = (
  resource: Resource,
  jobStatus?: Job['status'],
): Round | null => {
  const rounds = [...(resource.rounds ?? [])].sort((a, b) => a.round_index - b.round_index)
  const reverse = [...rounds].reverse()
  if (resource.status === 'failed') {
    const failed = reverse.find((round) => round.status === 'failed')
    if (failed) return failed
  }
  if (resource.status === 'completed') return reverse[0] ?? null
  if (resource.status === 'cancelled' || (jobStatus && isJobTerminal(jobStatus))) {
    return (
      reverse.find((round) => round.status === 'running') ??
      reverse.find((round) => round.started_at || round.status !== 'pending') ??
      rounds[0] ??
      null
    )
  }
  return (
    rounds.find((round) => round.status === 'running') ??
    rounds.find((round) => round.status === 'pending') ??
    reverse.find((round) => round.status === 'failed') ??
    reverse[0] ??
    null
  )
}

export const getResourceRoundSummary = (
  resource: Resource,
): {
  kind: 'legacy' | 'current' | 'completed' | 'mixed' | 'ended'
  total: number
  completed: number
  skipped: number
} => {
  const rounds = resource.rounds ?? []
  const total = rounds.length
  const completed = rounds.filter((round) => round.status === 'completed').length
  const skipped = rounds.filter((round) => round.status === 'skipped').length
  const kind = !total
    ? 'legacy'
    : resource.status !== 'completed'
      ? 'current'
      : completed === total
        ? 'completed'
        : completed + skipped === total
          ? 'mixed'
          : 'ended'
  return { kind, total, completed, skipped }
}

export const isJobEventAnomaly = (event: {
  level: string
  type: string
  metadata?: Record<string, unknown> | null
}): boolean => {
  if (['warn', 'warning', 'error'].includes(event.level.toLowerCase())) return true
  return event.type === 'batch' && ['partial', 'failed'].includes(String(event.metadata?.status))
}

/** An interrupted round never accumulates more elapsed time after the task has stopped. */
export const getDetailRoundSeconds = (round: Round, job: Job, now = Date.now()): number | null => {
  if (!round.started_at) return null
  const end = round.finished_at
    ? new Date(round.finished_at).getTime()
    : isJobTerminal(job.status) || job.status === 'paused'
      ? new Date(job.updated_at).getTime()
      : now
  const seconds = (end - new Date(round.started_at).getTime()) / 1000
  return Number.isFinite(seconds) && seconds >= 0 ? seconds : null
}
