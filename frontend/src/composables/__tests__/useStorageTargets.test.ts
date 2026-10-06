import { beforeEach, describe, expect, it, vi } from 'vitest'
import { nextTick, shallowRef } from 'vue'
import type { ApiSchemas } from '@/api/client-core'
import { changeSessionContext } from '@/api/session-context'
import {
  createStorageTargets,
  watchStorageTargetContext,
  type StorageTargetContext,
} from '../useStorageTargets'

vi.mock('@/i18n', () => ({ t: (key: string) => key }))
const result = (
  overrides: Partial<ApiSchemas['StorageOptions']> = {},
): ApiSchemas['StorageOptions'] => ({
  runtime: { deployment_enabled: true, maintenance: false },
  scope: 'user',
  owner_id: 1,
  policy: { mode: 'both', default_choice: 'site', generation: 0 },
  default_space_id: 2,
  default_unavailable_reason: null,
  items: [{ space_id: 2, name: 'site', scope: 'site', selectable: true, reason_codes: [] }],
  ...overrides,
})
function setup() {
  let context: StorageTargetContext = { kind: 'create', organizationId: null }
  const available = vi.fn(() => true)
  const deps = {
    getStorageOptions: vi.fn(async () => result()),
    getProjectStorageOptions: vi.fn(async () => result()),
  }
  return {
    deps,
    available,
    targets: createStorageTargets({ context: () => context, available }, deps),
    setContext: (value: StorageTargetContext) => {
      context = value
    },
  }
}
beforeEach(() => changeSessionContext('/api/v1', 1, true))
describe('storage target discovery', () => {
  it('does not refetch or clear selection for an equivalent context object but refreshes a changed generation', async () => {
    const project = {
      id: 1,
      owner_user_id: 1,
      storage_generation: 0,
      storage_state: 'active',
    } as ApiSchemas['Project']
    const context = shallowRef<StorageTargetContext>({ kind: 'project', project, purpose: 'bind' })
    const transport = {
      getStorageOptions: vi.fn(async () => result()),
      getProjectStorageOptions: vi.fn(async () => result()),
    }
    const targets = createStorageTargets(
      { context: () => context.value, available: () => true },
      transport,
    )
    const stop = watchStorageTargetContext(
      () => context.value,
      () => true,
      targets,
    )
    await nextTick()
    expect(transport.getProjectStorageOptions).toHaveBeenCalledTimes(1)
    context.value = { kind: 'project', project: { ...project }, purpose: 'bind' }
    await nextTick()
    expect(transport.getProjectStorageOptions).toHaveBeenCalledTimes(1)
    expect(targets.valid.value).toBe(true)
    expect(targets.selectedId.value).toBe(2)
    context.value = {
      kind: 'project',
      project: { ...project, storage_generation: 1 },
      purpose: 'bind',
    }
    expect(targets.valid.value).toBe(false)
    await nextTick()
    expect(transport.getProjectStorageOptions).toHaveBeenCalledTimes(2)
    expect(targets.selectedId.value).toBe(2)
    stop()
    targets.clear()
  })
  it('uses the personal discovery endpoint and accepts its explicit site default', async () => {
    const { targets, deps } = setup()
    await targets.refresh()
    expect(deps.getStorageOptions).toHaveBeenCalledWith({ kind: 'user' }, expect.any(Object))
    expect(targets.selectedId.value).toBe(2)
  })
  it('never substitutes the first available target for an unavailable default', async () => {
    const { targets, deps } = setup()
    deps.getStorageOptions.mockResolvedValue(
      result({ default_space_id: null, default_unavailable_reason: 'space_disabled' }),
    )
    await targets.refresh()
    expect(targets.selectedId.value).toBeNull()
    expect(targets.valid.value).toBe(false)
  })
  it('requires explicit choice for user default policy', async () => {
    const { targets, deps } = setup()
    deps.getStorageOptions.mockResolvedValue(
      result({ policy: { mode: 'both', default_choice: 'user', generation: 0 } }),
    )
    await targets.refresh()
    expect(targets.selectedId.value).toBeNull()
    targets.select(2)
    expect(targets.valid.value).toBe(true)
  })
  it('retains a revoked selected target as invalid and never requests after permission loss', async () => {
    const { targets, deps, available } = setup()
    await targets.refresh()
    deps.getStorageOptions.mockResolvedValue(
      result({
        items: [
          {
            space_id: 2,
            name: 'site',
            scope: 'site',
            selectable: false,
            reason_codes: ['space_disabled'],
          },
        ],
      }),
    )
    await targets.refresh()
    expect(targets.selectedId.value).toBe(2)
    expect(targets.valid.value).toBe(false)
    available.mockReturnValue(false)
    await targets.refresh()
    expect(deps.getStorageOptions).toHaveBeenCalledTimes(2)
  })
  it('discards a response after organization context changes', async () => {
    const { targets, deps, setContext } = setup()
    deps.getStorageOptions.mockImplementation(async () => {
      setContext({ kind: 'create', organizationId: 3 })
      return result()
    })
    expect(await targets.refresh()).toBe(false)
    expect(targets.response.value).toBeNull()
  })
  it('discovers repair targets for the selected historical version', async () => {
    const { targets, deps, setContext } = setup()
    setContext({
      kind: 'project',
      project: { id: 1 } as ApiSchemas['Project'],
      purpose: 'repair',
      sourceRevisionId: 3,
    })
    await targets.refresh()
    expect(deps.getProjectStorageOptions).toHaveBeenCalledWith(
      1,
      { purpose: 'repair', source_revision_id: 3 },
      expect.any(Object),
    )
  })
  it('does not reinstate a default after the user deliberately clears it', async () => {
    const { targets } = setup()
    await targets.refresh()
    targets.select(null)
    await targets.refresh()
    expect(targets.selectedId.value).toBeNull()
    expect(targets.valid.value).toBe(false)
  })
  it('retains the original ID and safe name when options disappear or a refresh fails', async () => {
    const { targets, deps } = setup()
    await targets.refresh()
    deps.getStorageOptions.mockRejectedValueOnce(new Error('offline'))
    await targets.refresh()
    expect(targets.selectedId.value).toBe(2)
    expect(targets.valid.value).toBe(false)
    deps.getStorageOptions.mockResolvedValue(result({ items: [], default_space_id: 3 }))
    await targets.refresh()
    expect(targets.selectedId.value).toBe(2)
    expect(targets.retainedSelection.value?.name).toBe('site')
    expect(targets.valid.value).toBe(false)
  })
  it('accepts an explicitly selectable Local target while deployment is closed', async () => {
    const { targets, deps } = setup()
    deps.getStorageOptions.mockResolvedValue(
      result({ runtime: { deployment_enabled: false, maintenance: false } }),
    )
    await targets.refresh()
    expect(targets.valid.value).toBe(true)
  })
  it('marks an incomplete runtime unsupported without inventing availability', async () => {
    const { targets, deps } = setup()
    deps.getStorageOptions.mockResolvedValue(result({ runtime: undefined as never }))
    await targets.refresh()
    expect(targets.status.value).toBe('unsupported')
    expect(targets.valid.value).toBe(false)
  })
  it('invalidates synchronously while preserving the same choice through a storage generation change', async () => {
    const { targets, deps, setContext } = setup()
    const project = {
      id: 1,
      owner_user_id: 1,
      storage_generation: 0,
      storage_state: 'active',
    } as ApiSchemas['Project']
    setContext({ kind: 'project', project, purpose: 'bind' })
    await targets.refresh()
    setContext({ kind: 'project', project: { ...project, storage_generation: 1 }, purpose: 'bind' })
    targets.invalidate()
    expect(targets.selectedId.value).toBe(2)
    expect(targets.valid.value).toBe(false)
    deps.getProjectStorageOptions.mockResolvedValue(result({ default_space_id: 8 }))
    await targets.refresh()
    expect(targets.selectedId.value).toBe(2)
    expect(targets.valid.value).toBe(true)
  })
})
