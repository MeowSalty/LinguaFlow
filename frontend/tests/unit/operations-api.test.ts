import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fetchOperationsSummary, listOperations, type OperationsSummary } from '@/api/operations'

const { get } = vi.hoisted(() => ({ get: vi.fn() }))
vi.mock('@/api/client-core', () => ({ apiClient: { GET: get } }))
vi.mock('@/i18n', () => ({ t: (key: string) => key }))

const counts = (pausing = 0): OperationsSummary['total'] => ({
  running: 2,
  pending: 3,
  pausing,
  paused: 4,
  waiting_retry: 5,
  needs_action: 6,
  recent_failed: 99,
})
const zeroCounts = (): OperationsSummary['total'] => ({
  pending: 0,
  running: 0,
  pausing: 0,
  paused: 0,
  waiting_retry: 0,
  needs_action: 0,
  recent_failed: 0,
})
const summary = (): OperationsSummary => ({
  total: counts(7),
  by_type: { translation: counts(7), glossary_sync: counts(), storage: counts() },
  as_of: '2026-10-10T00:00:00.123456789Z',
  recent_failed_since: '2026-10-03T00:00:00.123456789Z',
})

beforeEach(() => get.mockReset())

describe('Operations wire boundary', () => {
  it('normalizes missing legacy pausing and missing retirement buckets without erasing unknowns', async () => {
    const data = summary()
    for (const bucket of [
      data.total,
      data.by_type.translation,
      data.by_type.glossary_sync,
      data.by_type.storage,
    ]) {
      delete (bucket as Partial<typeof bucket>).pausing
    }
    get.mockResolvedValue({ data, response: new Response() })
    const result = await fetchOperationsSummary()
    for (const bucket of [
      result.total,
      result.by_type.translation,
      result.by_type.glossary_sync,
      result.by_type.storage,
    ]) {
      expect(bucket).toEqual(counts())
    }
    expect(result.by_type.storage_retirement).toEqual(zeroCounts())
    expect(result.as_of).toBe(data.as_of)
    expect(result.recent_failed_since).toBe(data.recent_failed_since)
    expect(data.total).not.toHaveProperty('pausing')
  })

  it('preserves independent pausing and retirement counts and forwards cancellation', async () => {
    const data = summary()
    data.by_type.storage_retirement = counts(8)
    get.mockResolvedValue({ data, response: new Response() })
    const signal = new AbortController().signal
    const query = {
      task_type: 'translation' as const,
      project_id: 7,
      trigger_type: 'manual' as const,
    }
    expect(await fetchOperationsSummary(query, { signal })).toEqual(data)
    expect(get).toHaveBeenCalledWith('/operations/summary', { params: { query }, signal })
  })

  it('does not manufacture a successful empty summary on an unavailable response', async () => {
    get.mockResolvedValue({
      error: { title: 'Unavailable', status: 503 },
      response: new Response(null, { status: 503 }),
    })
    await expect(fetchOperationsSummary()).rejects.toMatchObject({ status: 503 })
  })

  it('forwards the pausing filter without a state filter and preserves real pagination cursors', async () => {
    const translation = { task_type: 'translation', task_id: '1', project_id: 7 }
    const storage = { task_type: 'storage', task_id: '2', project_id: 7 }
    const retirement = { task_type: 'storage_retirement', task_id: '3' }
    const future = { task_type: 'future_unsupported', task_id: '4' } as never
    const data = {
      items: [retirement, translation, future, storage],
      next_cursor: 'next-real-cursor',
    }
    get.mockResolvedValue({ data, response: new Response() })
    expect(await listOperations({ status: 'pausing', limit: 50 })).toEqual({
      items: [translation, storage],
      next_cursor: 'next-real-cursor',
    })
    expect(get).toHaveBeenCalledWith('/operations', {
      params: { query: { status: 'pausing', limit: 50 } },
      signal: undefined,
    })
  })
})
