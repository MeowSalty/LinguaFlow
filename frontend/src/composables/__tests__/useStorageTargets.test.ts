import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { ApiSchemas } from '@/api/client-core'
import { changeSessionContext } from '@/api/session-context'
import { createStorageTargets, type StorageTargetContext } from '../useStorageTargets'

vi.mock('@/i18n', () => ({ t: (key: string) => key }))
const result = (
  overrides: Partial<ApiSchemas['StorageOptions']> = {},
): ApiSchemas['StorageOptions'] => ({
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
  it('drops a revoked selected target and never sends another request after permission loss', async () => {
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
    expect(targets.selectedId.value).toBeNull()
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
})
