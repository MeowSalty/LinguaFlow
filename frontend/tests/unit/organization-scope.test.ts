import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope } from 'vue'
import { changeSessionContext } from '@/api/session-context'
import { ApiError } from '@/api/utils'
import { createScopedEntityState, type ScopedEntity } from '@/stores/scopedEntity'
import {
  canManageOrganization,
  canRemoveMember,
  invalidateOrganization,
  isOrganizationDependency,
  organizationRoles,
  parseOrganizationId,
} from '@/utils/organization-scope'
import { clearUnavailablePlanDependencies } from '@/utils/organization-copy'

vi.mock('@/i18n', () => ({ t: (key: string) => key }))
const scopes: ReturnType<typeof effectScope>[] = []
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (error: unknown) => void
  const promise = new Promise<T>((yes, no) => {
    resolve = yes
    reject = no
  })
  return { promise, resolve, reject }
}
function setup(list = vi.fn(async () => ({ items: [] as ScopedEntity[] }))) {
  const api = {
    list,
    create: vi.fn(async (body: ScopedEntity) => body),
    update: vi.fn(async (_id: number, body: ScopedEntity) => body),
    remove: vi.fn(async () => {}),
  }
  const scope = effectScope()
  scopes.push(scope)
  const state = scope.run(() =>
    createScopedEntityState<ScopedEntity, ScopedEntity, ScopedEntity>(api),
  )!
  return { state, api }
}
beforeEach(() => {
  changeSessionContext('https://instance.test/api/v1', 1, true)
  organizationRoles.value = { 7: 'owner', 8: 'admin' }
})
afterEach(() => {
  for (const scope of scopes.splice(0)) scope.stop()
})

describe('organization identity and permission boundaries', () => {
  it('rejects malformed, duplicate, nonpositive and unsafe organization IDs', () => {
    for (const id of ['', '0', '-1', '1.5', '9007199254740992', ['7', '8'], null])
      expect(parseOrganizationId(id)).toBeNull()
    expect(parseOrganizationId('7')).toBe(7)
  })
  it('uses organization roles independently of system admin status', () => {
    expect(canManageOrganization('member')).toBe(false)
    expect(canManageOrganization('admin')).toBe(true)
    expect(canRemoveMember('admin', 'owner', false)).toBe(false)
    expect(canRemoveMember('admin', 'member', false)).toBe(true)
    expect(canRemoveMember('member', 'member', true)).toBe(true)
  })
  it('permits only same-organization and system dependencies', () => {
    expect(isOrganizationDependency({ scope: 'system' }, 7)).toBe(true)
    expect(isOrganizationDependency({ scope: 'org', owner_org_id: 7 }, 7)).toBe(true)
    expect(isOrganizationDependency({ scope: 'org', owner_org_id: 8 }, 7)).toBe(false)
    expect(isOrganizationDependency({ scope: 'user' }, 7)).toBe(false)
  })
  it('copies eligible system dependencies and clears foreign dependencies without mutating the source', () => {
    const draft = {
      profile_id: -1,
      ruby_retry: { enabled: true, backend_id: 9 },
      rounds: [
        { mode: 'translate' as const, backend_id: 9, translate: { prompt_template_id: -1 } },
        { mode: 'extract' as const, backend_id: 7, extract: { template_id: 9 } },
      ],
    }
    const copy = clearUnavailablePlanDependencies(draft, {
      profiles: [{ id: -1 }],
      backends: [{ id: 7 }],
      prompts: [{ id: -1 }],
      bootstrap: [{ id: -2 }],
    })
    expect(copy.profile_id).toBe(-1)
    expect(copy.ruby_retry.backend_id).toBe(null)
    expect(copy.rounds[0]?.backend_id).toBe(null)
    expect(copy.rounds[0]?.translate?.prompt_template_id).toBe(-1)
    expect(copy.rounds[1]?.backend_id).toBe(7)
    expect(copy.rounds[1]?.extract?.template_id).toBe(null)
    expect(draft.ruby_retry.backend_id).toBe(9)
    expect(draft.rounds[1]?.extract?.template_id).toBe(9)
  })
})

describe('scoped resource requests', () => {
  it('coalesces matching loads and aborts obsolete organization reads', async () => {
    const first = deferred<{ items: ScopedEntity[] }>()
    const second = deferred<{ items: ScopedEntity[] }>()
    const list = vi.fn().mockReturnValueOnce(first.promise).mockReturnValueOnce(second.promise)
    const { state } = setup(list)
    const one = state.load(7)
    expect(state.load(7)).toBe(one)
    const firstSignal = list.mock.calls[0]![1] as AbortSignal
    const two = state.load(8)
    expect(firstSignal.aborted).toBe(true)
    second.resolve({ items: [{ id: 8, name: 'new', owner_org_id: 8 }] })
    await two
    first.resolve({ items: [{ id: 7, name: 'old', owner_org_id: 7 }] })
    await one
    expect(state.items.value.map((item) => item.id)).toEqual([8])
    expect(state.loading.value).toBe(false)
  })
  it('does not widen an invalid explicit scope into a personal request', async () => {
    const { state, api } = setup()
    await state.load(0)
    expect(api.list).not.toHaveBeenCalled()
    expect(state.error.value).toBe('team.unavailable')
  })
  it('keeps legacy dashboard callers unfiltered after visiting an organization page', async () => {
    const { state, api } = setup()
    await state.load(7)
    await state.load()
    expect(state.orgId.value).toBeNull()
    expect(api.list.mock.calls.map((call) => call[0])).toEqual([7, null])
  })
  it('clears session data and refuses late data or errors', async () => {
    const request = deferred<{ items: ScopedEntity[] }>()
    const { state } = setup(vi.fn().mockReturnValue(request.promise))
    const load = state.load(7)
    changeSessionContext('https://another.test/api/v1', 2)
    request.reject(new Error('old account error'))
    await load
    expect(state.items.value).toEqual([])
    expect(state.error.value).toBeNull()
    expect(state.loading.value).toBe(false)
  })
  it('preserves a snapshot on a temporary failure and clears it on denied access', async () => {
    const list = vi
      .fn()
      .mockResolvedValueOnce({ items: [{ id: 7, name: 'retained', owner_org_id: 7 }] })
      .mockRejectedValueOnce(new ApiError('offline', 503))
      .mockRejectedValueOnce(new ApiError('revoked', 403))
    const { state } = setup(list)
    await state.load(7)
    await state.load(7)
    expect(state.items.value).toHaveLength(1)
    await state.load(7)
    expect(state.items.value).toEqual([])
  })
  it('removes revoked membership resources and ignores its pending response', async () => {
    const pending = deferred<{ items: ScopedEntity[] }>()
    const { state } = setup(
      vi
        .fn()
        .mockResolvedValueOnce({ items: [{ id: 7, name: 'a', owner_org_id: 7 }] })
        .mockReturnValueOnce(pending.promise),
    )
    await state.load(7)
    const load = state.load(7)
    organizationRoles.value = {}
    invalidateOrganization(7)
    pending.resolve({ items: [{ id: 7, name: 'secret', owner_org_id: 7 }] })
    await load
    expect(state.items.value).toEqual([])
    expect(state.canEdit()).toBe(false)
  })
  it('refuses cross-scope mutation results and does not replay conflicts', async () => {
    const { state, api } = setup()
    state.setOrganization(7)
    const mutation = deferred<ScopedEntity>()
    api.create.mockReturnValueOnce(mutation.promise)
    const create = state.create({ id: 7, name: 'a', owner_org_id: 7 })
    state.setOrganization(8)
    mutation.resolve({ id: 7, name: 'a', owner_org_id: 7 })
    await expect(create).rejects.toHaveProperty('name', 'AbortError')
    expect(state.items.value).toEqual([])
    api.update.mockRejectedValueOnce(new ApiError('conflict', 409))
    await expect(state.update(8, { id: 8, name: 'b' })).rejects.toHaveProperty('status', 409)
    expect(api.update).toHaveBeenCalledTimes(1)
    expect(api.list).toHaveBeenCalledTimes(1)
    expect(state.error.value).toBe('conflict')
  })
})
