import { it, expect, vi } from 'vitest'
import { createPinia, disposePinia, setActivePinia } from 'pinia'
import { useStatsStore } from '@/stores/stats'
import { changeSessionContext } from '@/api/session-context'
import type { ApiSchemas } from '@/api/client-core'
const api = vi.hoisted(() => ({ stats: vi.fn() }))
vi.mock('@/api/client', () => ({ fetchStatsSummary: api.stats, fetchActivity: vi.fn() }))
vi.mock('@/i18n', () => ({ t: (key: string) => key }))
it('a history deletion fences an earlier same-session stats request and its finally', async () => {
  const pinia = createPinia()
  setActivePinia(pinia)
  changeSessionContext('/api/v1', 1, true)
  let old!: (value: ApiSchemas['UsageStats']) => void,
    fresh!: (value: ApiSchemas['UsageStats']) => void
  api.stats
    .mockReturnValueOnce(
      new Promise((resolve) => {
        old = resolve
      }),
    )
    .mockReturnValueOnce(
      new Promise((resolve) => {
        fresh = resolve
      }),
    )
  const store = useStatsStore(),
    first = store.loadStats()
  store.invalidate()
  const second = store.loadStats()
  old({ completed_jobs: 99, api_calls: 100 } as ApiSchemas['UsageStats'])
  await first
  expect(store.stats).toBeNull()
  expect(store.statsLoading).toBe(true)
  fresh({ completed_jobs: 2, api_calls: 100 } as ApiSchemas['UsageStats'])
  await second
  expect(store.stats).toMatchObject({ completed_jobs: 2, api_calls: 100 })
  disposePinia(pinia)
})
