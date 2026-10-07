import { beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope } from 'vue'

import { changeSessionContext } from '@/api/session-context'
import type { ApiSchemas } from '@/api/client'
import { useProjectStorageSnapshot } from '@/composables/useProjectStorageSnapshot'

const api = vi.hoisted(() => ({ getProjectStorage: vi.fn() }))
vi.mock('@/api/storage', () => ({ getProjectStorage: api.getProjectStorage }))
vi.mock('@/i18n', () => ({ t: (key: string) => key }))

const payload = (): ApiSchemas['ProjectStorage'] =>
  ({
    project_id: 1,
    storage_generation: 0,
    runtime: { deployment_enabled: true, maintenance: false },
    reason_codes: [],
  }) as ApiSchemas['ProjectStorage']

describe('useProjectStorageSnapshot.whenSettled', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    changeSessionContext('/api/v1', 1, true)
  })

  const create = () => {
    const project = { id: 1, storage_generation: 0 }
    const scope = effectScope()
    const snapshot = scope.run(() => useProjectStorageSnapshot(() => project))!
    return { snapshot, stop: () => scope.stop() }
  }

  it('resolves immediately when the snapshot is not revalidating', async () => {
    api.getProjectStorage.mockResolvedValue(payload())
    const { snapshot, stop } = create()
    await vi.waitFor(() => expect(snapshot.status.value).toBe('ready'))

    await snapshot.whenSettled()

    expect(snapshot.contentWritable.value).toBe(true)
    stop()
  })

  it('waits for an in-flight refresh to land', async () => {
    let release!: (value: ApiSchemas['ProjectStorage']) => void
    api.getProjectStorage.mockImplementation(
      () => new Promise<ApiSchemas['ProjectStorage']>((resolve) => (release = resolve)),
    )
    const { snapshot, stop } = create()
    await vi.waitFor(() => expect(snapshot.status.value).toBe('loading'))

    let settled = false
    const settledPromise = snapshot.whenSettled(500).then(() => {
      settled = true
    })
    await Promise.resolve()
    expect(settled).toBe(false)

    release(payload())
    await settledPromise
    expect(snapshot.status.value).toBe('ready')
    stop()
  })

  it('waits out a focus invalidation until the snapshot is ready again', async () => {
    api.getProjectStorage.mockResolvedValue(payload())
    const { snapshot, stop } = create()
    await vi.waitFor(() => expect(snapshot.status.value).toBe('ready'))

    // 模拟窗口重新聚焦：同步失效后短暂不可写，新一轮读取完成后恢复
    snapshot.invalidate()
    expect(snapshot.status.value).toBe('stale')
    expect(snapshot.contentWritable.value).toBe(false)

    const settled = snapshot.whenSettled(500)
    await snapshot.refresh()
    await settled
    expect(snapshot.status.value).toBe('ready')
    expect(snapshot.contentWritable.value).toBe(true)
    stop()
  })

  it('gives up after the timeout and leaves the judgement to the caller', async () => {
    api.getProjectStorage.mockImplementation(
      () => new Promise<ApiSchemas['ProjectStorage']>(() => {}),
    )
    const { snapshot, stop } = create()
    await vi.waitFor(() => expect(snapshot.status.value).toBe('loading'))

    await snapshot.whenSettled(20)

    expect(snapshot.status.value).toBe('loading')
    expect(snapshot.contentWritable.value).toBe(false)
    stop()
  })
})
