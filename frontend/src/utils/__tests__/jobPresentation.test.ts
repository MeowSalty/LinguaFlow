import { expect, it } from 'vitest'
import type { ApiSchemas } from '@/api/client'
import {
  getDetailRoundSeconds,
  getResourceRoundSummary,
  getRoundDisplayState,
  isJobEventAnomaly,
  selectResourceRound,
} from '../jobPresentation'

type Round = ApiSchemas['JobResourceRound']
type Resource = ApiSchemas['JobResource']
type Job = ApiSchemas['Job']

const START = '2026-09-27T00:00:00.000Z'
const STOP = '2026-09-27T00:02:00.000Z'
const NOW = Date.parse('2026-09-27T01:00:00.000Z')

const round = (
  roundIndex: number,
  status: Round['status'],
  overrides: Partial<Round> = {},
): Round => ({
  round_index: roundIndex,
  mode: 'translate',
  status,
  segment_total: 25,
  segment_completed: status === 'completed' ? 25 : 0,
  started_at: status === 'pending' ? null : START,
  finished_at: ['completed', 'failed'].includes(status) ? STOP : null,
  ...overrides,
})

const resource = (status: Resource['status'], rounds: Round[]): Resource => ({
  id: 1,
  resource_id: 1,
  status,
  segment_count: 25,
  completed_segments: 0,
  skipped_segments: 0,
  work_weight: 25,
  rounds,
  created_at: START,
  updated_at: STOP,
})

const job = (status: Job['status'], overrides: Partial<Job> = {}): Job => ({
  execution_config: {},
  id: 1,
  project_id: 1,
  execution_plan_id: 1,
  trigger_type: 'manual',
  status,
  progress: {
    total_resources: 1,
    completed_resources: 0,
    failed_resources: 0,
    progress_total: 25,
    progress_completed: 0,
  },
  created_at: START,
  updated_at: STOP,
  ...overrides,
})

const failed = round(0, 'failed')
const pending = round(1, 'pending')
const running = round(2, 'running', { segment_completed: 7 })
const completed = round(3, 'completed')

for (const [name, row, status, expected] of [
  [
    'failed resource identifies failure before pending work',
    resource('failed', [pending, failed]),
    'failed',
    failed,
  ],
  [
    'live recovery identifies its next round',
    resource('running', [pending, failed]),
    'running',
    pending,
  ],
  [
    'live resource identifies running work',
    resource('running', [pending, running, failed]),
    'running',
    running,
  ],
  [
    'cancelled resource identifies interrupted work',
    resource('cancelled', [completed, running, pending]),
    'cancelled',
    running,
  ],
  [
    'cancelled unstarted resource identifies its first round',
    resource('cancelled', [round(2, 'pending'), pending]),
    'cancelled',
    pending,
  ],
  [
    'cancelled task overrides stale live resource',
    resource('running', [pending, failed]),
    'cancelled',
    failed,
  ],
  [
    'cancelled resource identifies latest executed round',
    resource('cancelled', [pending, completed, failed]),
    'cancelled',
    completed,
  ],
  ['legacy resource has no selected round', resource('completed', []), 'completed', null],
  [
    'completed recovery identifies its last round',
    resource('completed', [completed, failed]),
    'completed',
    completed,
  ],
] as const) {
  it(name, () => expect(selectResourceRound(row, status)).toBe(expected))
}

it('round selection preserves the supplied array order', () => {
  const row = resource('running', [running, failed, pending])
  selectResourceRound(row, 'running')
  expect(row.rounds).toEqual([running, failed, pending])
})

for (const [jobStatus, resourceStatus, roundStatus, expected] of [
  ['running', 'running', 'running', 'running'],
  ['running', 'pending', 'running', 'pending'],
  ['pending', 'running', 'running', 'pending'],
  ['paused', 'running', 'running', 'paused'],
  ['cancelled', 'running', 'running', 'stopped'],
  ['failed', 'running', 'running', 'stopped'],
  ['completed', 'running', 'running', 'stopped'],
  ['running', 'cancelled', 'running', 'stopped'],
  ['running', 'failed', 'running', 'stopped'],
  ['running', 'completed', 'running', 'stopped'],
  ['cancelled', 'pending', 'pending', 'not_run'],
  ['running', 'failed', 'pending', 'not_run'],
  ['paused', 'running', 'pending', 'pending'],
] as const) {
  it(`round display ${jobStatus}/${resourceStatus}/${roundStatus}`, () => {
    expect(getRoundDisplayState(jobStatus, resourceStatus, roundStatus)).toBe(expected)
  })
}

for (const historicalStatus of ['completed', 'failed', 'skipped'] as const) {
  for (const parentStatus of ['running', 'paused', 'cancelled', 'failed', 'completed'] as const) {
    it(`preserve historical ${historicalStatus} under ${parentStatus}`, () => {
      expect(getRoundDisplayState(parentStatus, 'cancelled', historicalStatus)).toBe(
        historicalStatus,
      )
    })
  }
}

for (const [name, row, expected] of [
  [
    'complete',
    resource('completed', [round(0, 'completed'), completed]),
    { kind: 'completed', total: 2, completed: 2, skipped: 0 },
  ],
  [
    'mixed completed and skipped',
    resource('completed', [completed, round(4, 'skipped')]),
    { kind: 'mixed', total: 2, completed: 1, skipped: 1 },
  ],
  [
    'completed recovery with historical failure',
    resource('completed', [failed, completed]),
    { kind: 'ended', total: 2, completed: 1, skipped: 0 },
  ],
  [
    'interrupted work',
    resource('cancelled', [failed, running, pending]),
    { kind: 'current', total: 3, completed: 0, skipped: 0 },
  ],
  [
    'current live work',
    resource('running', [completed, running]),
    { kind: 'current', total: 2, completed: 1, skipped: 0 },
  ],
  ['legacy', resource('completed', []), { kind: 'legacy', total: 0, completed: 0, skipped: 0 }],
] as const) {
  it(`${name} summary`, () => expect(getResourceRoundSummary(row)).toEqual(expected))
}

for (const level of ['warn', 'warning', 'error', 'WARN', 'Warning', 'ERROR'] as const) {
  it(`raw ${level} event is anomalous even for a dimmed pool row`, () => {
    expect(isJobEventAnomaly({ type: 'pool', level })).toBe(true)
  })
}

for (const [name, event, expected] of [
  ['partial batch', { type: 'batch', level: 'info', metadata: { status: 'partial' } }, true],
  ['failed batch', { type: 'batch', level: 'info', metadata: { status: 'failed' } }, true],
  ['successful batch', { type: 'batch', level: 'info', metadata: { status: 'success' } }, false],
  [
    'success metadata cannot hide raw error',
    { type: 'batch', level: 'error', metadata: { status: 'success' } },
    true,
  ],
  ['ordinary pool', { type: 'pool', level: 'info', metadata: { phase: 'pool_start' } }, false],
  ['missing batch metadata', { type: 'batch', level: 'info' }, false],
  ['null batch metadata', { type: 'batch', level: 'info', metadata: null }, false],
] as const) {
  it(name, () => expect(isJobEventAnomaly(event)).toBe(expected))
}

for (const status of ['paused', 'cancelled', 'failed', 'completed'] as const) {
  it(`${status} elapsed time remains frozen`, () => {
    expect(getDetailRoundSeconds(running, job(status), NOW)).toBe(120)
    expect(getDetailRoundSeconds(running, job(status), NOW + 3_600_000)).toBe(120)
  })
}

for (const [name, item, parent, now, expected] of [
  ['live elapsed time advances', running, job('running'), NOW, 3600],
  ['live elapsed time advances again', running, job('running'), NOW + 1000, 3601],
  [
    'finished round uses its own finish time',
    completed,
    job('paused', { updated_at: START }),
    NOW,
    120,
  ],
  [
    'zero elapsed time is valid',
    round(0, 'completed', { finished_at: START }),
    job('completed'),
    NOW,
    0,
  ],
  ['unstarted round has no elapsed time', pending, job('pending'), NOW, null],
  [
    'invalid start has no elapsed time',
    round(0, 'running', { started_at: 'invalid' }),
    job('running'),
    NOW,
    null,
  ],
  [
    'invalid finish has no elapsed time',
    round(0, 'completed', { finished_at: 'invalid' }),
    job('completed'),
    NOW,
    null,
  ],
  [
    'invalid stop time has no elapsed time',
    running,
    job('paused', { updated_at: 'invalid' }),
    NOW,
    null,
  ],
  [
    'finish before start has no elapsed time',
    round(0, 'completed', { started_at: STOP, finished_at: START }),
    job('completed'),
    NOW,
    null,
  ],
] as const) {
  it(name, () => expect(getDetailRoundSeconds(item, parent, now)).toBe(expected))
}
