import { describe, expect, it } from 'vitest'
import type { ApiSchemas } from '@/api/client'
import {
  jobObservationTime,
  latestJobStages,
  mergeJobObservation,
  readJobStages,
} from '../jobStages'

const stages = (as_of = '2026-10-09T11:00:00.123456789Z') => ({
  main_requests: 2,
  pending_alignment: 3,
  alignment_requests: 1,
  saving_requests: 2,
  ready_to_commit: 1,
  confirmed_work: 10,
  unknown_requests: 0,
  draining_requests: 5,
  as_of,
})
const job = (status: ApiSchemas['Job']['status'], updated_at: string, observation?: unknown) =>
  ({
    id: 12,
    project_id: 7,
    status,
    updated_at,
    progress: { progress_completed: 30, progress_total: 50, stages: observation },
  }) as ApiSchemas['Job']

describe('job stage observations', () => {
  it('validates every counter and timestamp without fabricating missing fields', () => {
    expect(readJobStages(stages())).toEqual(stages())
    for (const value of [
      undefined,
      null,
      [],
      {},
      { ...stages(), main_requests: -1 },
      { ...stages(), saving_requests: 0.5 },
      { ...stages(), alignment_requests: Infinity },
      { ...stages(), confirmed_work: Number.MAX_SAFE_INTEGER + 1 },
      { ...stages(), as_of: '2026-02-30T11:00:00Z' },
      { ...stages(), as_of: 'not a date' },
    ]) {
      expect(readJobStages(value)).toBeUndefined()
    }
  })

  it('orders sub-millisecond observations including equivalent time zones', () => {
    const earlier = stages('2026-10-09T11:00:00.123456788Z')
    expect(latestJobStages(stages(), earlier)).toEqual(stages())
    expect(latestJobStages(earlier, stages())).toEqual(stages())
    expect(jobObservationTime('2026-10-09T19:00:00.123456789+08:00')).toBe(
      jobObservationTime(stages().as_of),
    )
    expect(latestJobStages(stages(), { ...stages(), main_requests: 9 })).toEqual(stages())
  })

  it('keeps newer observations across stale or missing REST stages', () => {
    const current = job('pausing', '2026-10-09T11:00:01Z', stages())
    const incoming = job('running', '2026-10-09T11:00:00Z', stages('2026-10-09T10:59:59Z'))
    const merged = mergeJobObservation(current, incoming)
    expect(merged.status).toBe('pausing')
    expect(merged.progress.stages).toEqual(stages())
    expect(merged.progress.progress_completed).toBe(30)
    expect(merged.progress.progress_total).toBe(50)
    expect(
      mergeJobObservation(current, job('paused', '2026-10-09T11:00:02Z')).progress.stages,
    ).toEqual(stages())
  })

  it('accepts decreasing request counts and never infers lifecycle or completion', () => {
    const current = job('pausing', '2026-10-09T11:00:01Z', stages())
    const next = { ...stages('2026-10-09T11:00:02Z'), main_requests: 0, draining_requests: 0 }
    const merged = mergeJobObservation(current, job('pausing', '2026-10-09T11:00:01Z', next))
    expect(merged.progress.stages).toEqual(next)
    expect(merged.status).toBe('pausing')
    expect(merged.progress.progress_completed).toBe(30)
  })

  it('does not carry observations to another job or expose malformed REST data', () => {
    const current = job('running', '2026-10-09T11:00:01Z', stages())
    expect(
      mergeJobObservation(current, { ...job('pending', '2026-10-09T11:00:02Z'), id: 13 }).progress
        .stages,
    ).toBeUndefined()
    expect(
      mergeJobObservation(null, job('running', '2026-10-09T11:00:00Z', {})).progress.stages,
    ).toBeUndefined()
  })
})
