import { describe, expect, it } from 'vitest'
import type { Operation, OperationsSummary } from '@/api/operations'
import { taskTrackerPresentation } from '../taskTrackerPresentation'

const operation = (id: string, status: Operation['status'] = 'running'): Operation => ({
  task_type: 'glossary_sync',
  task_id: id,
  project_id: 7,
  project_name: 'Project',
  status,
  can_delete: ['completed', 'failed', 'cancelled'].includes(status),
  finished_at: null,
  created_at: '2026-10-05T00:00:00Z',
  updated_at: '2026-10-05T00:00:00Z',
  supported_actions: ['view'],
  progress: { processed_segments: 1, total_segments: 10 },
})
function summary(total: Partial<OperationsSummary['total']> = {}): OperationsSummary {
  const counts = {
    running: 0,
    pending: 0,
    pausing: 0,
    paused: 0,
    waiting_retry: 0,
    needs_action: 0,
    recent_failed: 0,
    ...total,
  }
  return {
    total: counts,
    by_type: { translation: counts, glossary_sync: counts, storage: counts },
    as_of: '2026-10-05T00:00:00Z',
    recent_failed_since: '2026-09-28T00:00:00Z',
  }
}
const input = () => ({
  summary: summary(),
  active: [] as Operation[],
  terminal: [] as Operation[],
  retainTerminal: true,
  hiddenTerminalKeys: [] as string[],
})

describe('task tracker presentation', () => {
  it('uses the complete summary for six active states independently of visible rows', () => {
    const result = taskTrackerPresentation({
      ...input(),
      summary: summary({
        running: 100,
        pending: 10,
        pausing: 7,
        paused: 3,
        waiting_retry: 5,
        needs_action: 2,
        recent_failed: 70,
      }),
      active: Array.from({ length: 25 }, (_, i) => operation(String(i + 1))),
      terminal: [operation('90', 'failed')],
    })
    expect(result.activeCount).toBe(127)
    expect(result.countLabel).toBe('99+')
    expect(result.displayed).toHaveLength(20)
    expect(result.displayed.every((row) => row.status === 'running')).toBe(true)
    expect(result.failedCount).toBe(1)
    expect(result.attention).toBe('failed')
  })
  it('counts a sole pausing task even before its discovery page arrives', () => {
    expect(
      taskTrackerPresentation({ ...input(), summary: summary({ pausing: 1, recent_failed: 12 }) }),
    ).toMatchObject({
      activeCount: 1,
      countLabel: '1',
      attention: 'normal',
      displayed: [],
    })
  })
  it('keeps unknown counts distinct from a successfully loaded empty summary', () => {
    expect(taskTrackerPresentation({ ...input(), summary: null })).toMatchObject({
      activeCount: null,
      countLabel: '—',
      needsActionCount: null,
      waitingRetryCount: null,
    })
    expect(taskTrackerPresentation(input())).toMatchObject({
      activeCount: 0,
      countLabel: '0',
      attention: 'normal',
    })
  })
  it('honors reminders and composite identities instead of recent failure statistics', () => {
    const current = {
      ...input(),
      summary: summary({ recent_failed: 80 }),
      terminal: [operation('42', 'failed'), operation('43', 'completed')],
      hiddenTerminalKeys: ['translation:42'],
    }
    expect(taskTrackerPresentation(current).failedCount).toBe(1)
    const hidden = taskTrackerPresentation({ ...current, hiddenTerminalKeys: ['glossary_sync:42'] })
    expect(hidden.failedCount).toBe(0)
    expect(hidden.attention).toBe('normal')
    expect(hidden.displayed.map((row) => row.task_id)).toEqual(['43'])
    expect(taskTrackerPresentation({ ...current, retainTerminal: false }).displayed).toEqual([])
  })
  it('removing failed reminders exposes actionable states without changing the active count', () => {
    const current = {
      ...input(),
      summary: summary({ needs_action: 2, waiting_retry: 3 }),
      terminal: [operation('42', 'failed')],
    }
    expect(taskTrackerPresentation(current).attention).toBe('failed')
    const hidden = taskTrackerPresentation({ ...current, hiddenTerminalKeys: ['glossary_sync:42'] })
    expect(hidden).toMatchObject({ attention: 'needs_action', activeCount: 5 })
    expect(
      taskTrackerPresentation({ ...input(), summary: summary({ waiting_retry: 3 }) }).attention,
    ).toBe('waiting_retry')
    expect(
      taskTrackerPresentation({
        ...input(),
        summary: summary({ running: 3, pending: 1, paused: 1 }),
      }).attention,
    ).toBe('normal')
  })
})
