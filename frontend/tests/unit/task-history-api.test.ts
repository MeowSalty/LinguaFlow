import { describe, it, expect, vi } from 'vitest'
import type { ApiClient } from '@/api/client-core'
import {
  batchDeleteTaskHistory,
  deleteTaskHistory,
  fetchTaskRetentionPreview,
  fetchTaskRetentionStatus,
  TaskHistoryApiError,
  type TaskHistoryTarget,
  retentionCountLabel,
} from '@/api/task-history'
vi.mock('@/i18n', () => ({ t: (key: string) => key }))
const translation: TaskHistoryTarget = { kind: 'translation', id: '7', project_id: 3 }
const sync: TaskHistoryTarget = { ...translation, kind: 'glossary_sync' }
const mockClient = (value: unknown) => {
  const method = vi.fn().mockResolvedValue(value)
  return { method, client: { GET: method, POST: method, DELETE: method } as unknown as ApiClient }
}
const counts = () => ({
  active: 1,
  terminal: null,
  missing_anchor: 0,
  not_expired: 2,
  expired: { blocked: 0, busy: null, deletable: 3 },
  timing_sources: { finished_at: 2, legacy_anchor: 0, missing_anchor: 0 },
  terminal_age: {
    lt_1d: 0,
    days_1_6: 0,
    days_7_29: 1,
    days_30_89: 1,
    days_90_plus: null,
    unknown: 0,
  },
  deletable_dependencies: {
    sse_events: 3,
    job_resources: 1,
    job_rounds: 1,
    job_round_segments: 1,
    credential_job_references: 1,
  },
})
const preview = () => ({
  policy_revision: 1,
  retention_days: 30,
  partial: true,
  incomplete_reasons: ['evaluation_budget_exhausted'],
  as_of: '2026-10-07T00:00:00Z',
  cutoff: '2026-09-07T00:00:00Z',
  by_type: { translation: counts(), glossary_sync: counts() },
})
describe('task history wire boundaries', () => {
  it('accepts empty 204 responses and keeps large sync identifiers as strings', async () => {
    const { method, client } = mockClient({ response: new Response(null, { status: 204 }) })
    await deleteTaskHistory(translation, client)
    expect(method).toHaveBeenCalledWith(
      '/jobs/{jobId}',
      expect.objectContaining({ params: { path: { jobId: 7 } } }),
    )
    await deleteTaskHistory({ ...sync, id: '90071992547409930' }, client)
    expect(method).toHaveBeenLastCalledWith(
      '/projects/{projectId}/sync-tasks/{taskId}',
      expect.objectContaining({
        params: { path: { projectId: 3, taskId: '90071992547409930' } },
      }),
    )
  })
  it('correlates reordered same-ID cross-kind results and does not infer missing or duplicate success', async () => {
    const { client } = mockClient({
      response: new Response(),
      data: {
        items: [
          { ...sync, status: 'forbidden' },
          { ...translation, status: 'deleted' },
        ],
      },
    })
    expect((await batchDeleteTaskHistory([translation, sync], client)).items).toEqual([
      { ...translation, status: 'deleted' },
      { ...sync, status: 'forbidden' },
    ])
    const broken = mockClient({
      response: new Response(),
      data: {
        items: [
          { ...translation, status: 'deleted' },
          { ...translation, status: 'deleted' },
          { ...sync, id: '99', status: 'deleted' },
        ],
      },
    })
    const outcomes = await batchDeleteTaskHistory([translation, sync], broken.client)
    expect(outcomes.items.map((item) => item.status)).toEqual(['unknown', 'unknown'])
    expect(outcomes.contractError).toBe(true)
  })
  it('rejects invalid cardinality, identity and unsafe numeric IDs before sending', async () => {
    const { method, client } = mockClient({ response: new Response() })
    for (const targets of [
      [],
      [translation, translation],
      Array.from({ length: 101 }, (_, i) => ({ ...translation, id: String(i + 1) })),
    ])
      await expect(batchDeleteTaskHistory(targets, client)).rejects.toBeInstanceOf(
        TaskHistoryApiError,
      )
    await expect(
      deleteTaskHistory({ ...translation, id: '90071992547409930' }, client),
    ).rejects.toThrow()
    expect(method).not.toHaveBeenCalled()
  })
  it('keeps unknown and unsafe counts distinct and rejects missing nested preview groups', async () => {
    const data = preview()
    const { client } = mockClient({ response: new Response(), data })
    expect(
      (await fetchTaskRetentionPreview(30, undefined, client)).by_type.translation.terminal,
    ).toBeNull()
    expect(retentionCountLabel(null)).toBe('taskHistoryErrors.unknown')
    expect(retentionCountLabel(9007199254740992)).toBe('taskHistoryErrors.imprecise')
    delete (data.by_type.translation as Partial<ReturnType<typeof counts>>).expired
    await expect(fetchTaskRetentionPreview(30, undefined, client)).rejects.toMatchObject({
      error_code: 'invalid_contract',
    })
  })
  it('rejects malformed scan summaries and maps errors without exposing raw server details', async () => {
    const { client } = mockClient({
      response: new Response(),
      data: {
        task_retention: { enabled: true, retention_days: 30, revision: 1 },
        policy_revision: 1,
        state: 'running',
        reason_codes: [],
        last_scan: {},
        backlog: null,
      },
    })
    await expect(fetchTaskRetentionStatus(undefined, client)).rejects.toMatchObject({
      error_code: 'invalid_contract',
    })
    const refused = mockClient({
      response: new Response(null, { status: 409 }),
      error: { error_code: 'task_busy', detail: 'SECRET INTERNAL PATH' },
    })
    await expect(deleteTaskHistory(translation, refused.client)).rejects.toMatchObject({
      message: 'taskHistoryErrors.busy',
      status: 409,
    })
  })
})
