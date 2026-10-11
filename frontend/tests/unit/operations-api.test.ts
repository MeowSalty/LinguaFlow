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
const summary = (): OperationsSummary => ({
  total: counts(7),
  by_type: { translation: counts(7), glossary_sync: counts(), storage: counts() },
  as_of: '2026-10-10T00:00:00.123456789Z',
  recent_failed_since: '2026-10-03T00:00:00.123456789Z',
})

beforeEach(() => get.mockReset())

describe('Operations wire boundary', () => {
  it('normalizes only the missing legacy pausing bucket for totals and every task type', async () => {
    const data = summary()
    for (const bucket of [data.total, ...Object.values(data.by_type)]) {
      delete (bucket as Partial<typeof bucket>).pausing
    }
    get.mockResolvedValue({ data, response: new Response() })
    const result = await fetchOperationsSummary()
    for (const bucket of [result.total, ...Object.values(result.by_type)]) {
      expect(bucket).toEqual(counts())
    }
    expect(result.as_of).toBe(data.as_of)
    expect(result.recent_failed_since).toBe(data.recent_failed_since)
    expect(data.total).not.toHaveProperty('pausing')
  })

  it('preserves independent pausing counts and forwards supported dimensions and cancellation', async () => {
    const data = summary()
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
    const data = { items: [], next_cursor: 'next-real-cursor' }
    get.mockResolvedValue({ data, response: new Response() })
    expect(await listOperations({ status: 'pausing', limit: 50 })).toEqual(data)
    expect(get).toHaveBeenCalledWith('/operations', {
      params: { query: { status: 'pausing', limit: 50 } },
      signal: undefined,
    })
  })
})
