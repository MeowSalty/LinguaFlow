import { effectScope, nextTick, shallowRef } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ApiSchemas } from '@/api/client-core'
import { changeSessionContext } from '@/api/session-context'
import { StorageApiError } from '@/api/storage-errors'
import { useProjectStorageSnapshot } from '../useProjectStorageSnapshot'

const api = vi.hoisted(() => ({ getProjectStorage: vi.fn() }))
vi.mock('@/api/storage', () => api)
vi.mock('@/i18n', () => ({ t: (key: string) => key }))
const project = {
  id: 7,
  owner_user_id: 1,
  owner_org_id: null,
  storage_generation: 0,
  storage_state: 'active',
} as ApiSchemas['Project']
const summary = (
  overrides: Partial<ApiSchemas['ProjectStorage']> = {},
): ApiSchemas['ProjectStorage'] => ({
  project_id: 7,
  runtime: { deployment_enabled: true, maintenance: false },
  storage_generation: 0,
  storage_state: 'active',
  migration_task_id: null,
  binding: null,
  reason_codes: [],
  ...overrides,
})
let scope: ReturnType<typeof effectScope>
async function setup() {
  scope = effectScope()
  const current = shallowRef(project)
  const snapshot = scope.run(() => useProjectStorageSnapshot(() => current.value))!
  await nextTick()
  return { snapshot, current }
}
beforeEach(() => {
  changeSessionContext('/api/v1', 1, true)
  api.getProjectStorage.mockResolvedValue(summary())
})
afterEach(() => scope?.stop())
describe('project storage snapshot admission', () => {
  it('does not reset or replace an in-flight snapshot when a project refresh returns the same identity and generations', async () => {
    const { snapshot, current } = await setup()
    let resolve!: (value: ApiSchemas['ProjectStorage']) => void
    api.getProjectStorage.mockReturnValue(
      new Promise((yes) => {
        resolve = yes
      }),
    )
    const reading = snapshot.refresh()
    current.value = { ...project }
    expect(api.getProjectStorage).toHaveBeenCalledTimes(2)
    expect(snapshot.value.value?.project_id).toBe(7)
    resolve(summary())
    expect(await reading).toBe(true)
    expect(snapshot.contentWritable.value).toBe(true)
  })
  it('allows Local content with deployment closed and blocks only a binding deployment refusal', async () => {
    api.getProjectStorage.mockResolvedValue(
      summary({ runtime: { deployment_enabled: false, maintenance: false } }),
    )
    const { snapshot } = await setup()
    expect(snapshot.contentWritable.value).toBe(true)
    api.getProjectStorage.mockResolvedValue(
      summary({
        runtime: { deployment_enabled: false, maintenance: false },
        reason_codes: ['storage_deployment_disabled'],
      }),
    )
    await snapshot.refresh()
    expect(snapshot.ready.value).toBe(true)
    expect(snapshot.contentWritable.value).toBe(false)
    expect(snapshot.value.value?.runtime.deployment_enabled).toBe(false)
  })
  it('keeps readable metadata when refresh fails or policy denies while pausing new writes', async () => {
    const { snapshot } = await setup()
    api.getProjectStorage.mockRejectedValue(
      new StorageApiError('policy', 403, { error_code: 'storage_policy_violation' }),
    )
    await snapshot.refresh()
    expect(snapshot.value.value?.project_id).toBe(7)
    expect(snapshot.ready.value).toBe(false)
    expect(snapshot.contentWritable.value).toBe(false)
    api.getProjectStorage.mockRejectedValue(
      new StorageApiError('forbidden', 403, { error_code: 'forbidden' }),
    )
    await snapshot.refresh()
    expect(snapshot.value.value).toBeNull()
  })
  it('rejects a late read after synchronous invalidation', async () => {
    const { snapshot } = await setup()
    let resolve!: (value: ApiSchemas['ProjectStorage']) => void
    api.getProjectStorage.mockReturnValue(
      new Promise((yes) => {
        resolve = yes
      }),
    )
    const reading = snapshot.refresh()
    snapshot.invalidate()
    resolve(summary())
    expect(await reading).toBe(false)
    expect(snapshot.ready.value).toBe(false)
    expect(snapshot.value.value?.project_id).toBe(7)
  })
  it('does not manufacture runtime for an old contract, and maintenance keeps metadata readable', async () => {
    const { snapshot } = await setup()
    api.getProjectStorage.mockResolvedValue(summary({ runtime: undefined as never }))
    await snapshot.refresh()
    expect(snapshot.status.value).toBe('unsupported')
    expect(snapshot.contentWritable.value).toBe(false)
    api.getProjectStorage.mockResolvedValue(
      summary({ runtime: { deployment_enabled: true, maintenance: true } }),
    )
    await snapshot.refresh()
    expect(snapshot.ready.value).toBe(true)
    expect(snapshot.contentWritable.value).toBe(false)
  })
})
