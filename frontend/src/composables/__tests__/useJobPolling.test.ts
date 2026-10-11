import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope, nextTick, reactive, ref, type EffectScope } from 'vue'
import type { ApiSchemas } from '@/api/client'
import { useJobPolling } from '../useJobPolling'

type Status = ApiSchemas['Job']['status']
const state = vi.hoisted(() => ({
  mounted: [] as (() => void)[],
  unmounted: [] as (() => void)[],
  store: { jobs: [] as { status: Status }[], loadJobs: vi.fn() },
}))
vi.mock('vue', async (original) => ({
  ...(await original<typeof import('vue')>()),
  onMounted: (callback: () => void) => state.mounted.push(callback),
  onUnmounted: (callback: () => void) => state.unmounted.push(callback),
}))
vi.mock('@/stores/job', () => ({ useJobStore: () => state.store }))

describe('job polling through safe pause', () => {
  let scope: EffectScope
  let page: EventTarget & { hidden: boolean }
  beforeEach(() => {
    vi.useFakeTimers()
    page = Object.assign(new EventTarget(), { hidden: false })
    vi.stubGlobal('document', page)
    state.store = reactive({ jobs: [{ status: 'pausing' as Status }], loadJobs: vi.fn() })
    state.mounted = []
    state.unmounted = []
    scope = effectScope()
  })
  afterEach(() => {
    state.unmounted.forEach((callback) => callback())
    scope.stop()
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })
  const mount = (enabled = ref(true), projectId = ref<number | null>(7)) => {
    const polling = scope.run(() => useJobPolling({ projectId, enabled }))!
    state.mounted.forEach((callback) => callback())
    return polling
  }
  const status = async (value: Status) => {
    state.store.jobs = [{ status: value }]
    await nextTick()
    state.store.loadJobs.mockClear()
  }

  it('polls draining work at running frequency until the server confirms paused', async () => {
    const polling = mount()
    expect(polling.hasActiveJobs.value).toBe(true)
    expect(polling.isPolling.value).toBe(true)
    state.store.loadJobs.mockClear()
    await vi.advanceTimersByTimeAsync(6000)
    expect(state.store.loadJobs).toHaveBeenCalledTimes(3)
    await status('paused')
    await vi.advanceTimersByTimeAsync(14000)
    expect(state.store.loadJobs).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(1000)
    expect(state.store.loadJobs).toHaveBeenCalledOnce()
  })

  it('immediately changes a paused timer back to running frequency for pausing', async () => {
    state.store.jobs = [{ status: 'paused' }]
    mount()
    await status('pausing')
    await vi.advanceTimersByTimeAsync(2000)
    expect(state.store.loadJobs).toHaveBeenCalledOnce()
  })

  it.each(['cancelled', 'completed', 'failed'] as const)(
    'stops when draining resolves to %s',
    async (terminal) => {
      const polling = mount()
      await status(terminal)
      await vi.advanceTimersByTimeAsync(20000)
      expect(state.store.loadJobs).not.toHaveBeenCalled()
      expect(polling.hasActiveJobs.value).toBe(false)
      expect(polling.isPolling.value).toBe(false)
    },
  )

  it('pauses for disabled and hidden views and resumes with an immediate refresh', async () => {
    const enabled = ref(false)
    const polling = mount(enabled)
    expect(polling.isPolling.value).toBe(false)
    expect(state.store.loadJobs).not.toHaveBeenCalled()
    enabled.value = true
    await nextTick()
    expect(state.store.loadJobs).toHaveBeenCalledOnce()
    page.hidden = true
    page.dispatchEvent(new Event('visibilitychange'))
    state.store.loadJobs.mockClear()
    await vi.advanceTimersByTimeAsync(20000)
    expect(polling.isPolling.value).toBe(false)
    expect(state.store.loadJobs).not.toHaveBeenCalled()
    page.hidden = false
    page.dispatchEvent(new Event('visibilitychange'))
    expect(polling.isPolling.value).toBe(true)
    expect(state.store.loadJobs).toHaveBeenCalledOnce()
  })
})
