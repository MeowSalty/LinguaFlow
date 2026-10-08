import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope, isRef, watch, type EffectScope } from 'vue'
import * as api from '@/api/storage'
import type { ApiSchemas } from '@/api/client-core'
import { changeSessionContext } from '@/api/session-context'
import { ApiError } from '@/api/utils'
import { storageRequestError } from '@/api/storage-errors'
import { createStorageState } from '@/stores/storage'
import { invalidateOrganization, organizationRoles } from '@/utils/organization-scope'
import { subscribeStorageRefresh } from '@/utils/storage-snapshots'
import {
  connectionActions,
  spaceActions,
  policyCapabilities,
  storageCapabilities,
  storageAction,
} from '../storage-fixtures'

const connection = (id = 1, scope = 'user', ownerId = 1): ApiSchemas['StorageConnection'] => ({
  id,
  scope,
  owner_id: ownerId,
  name: `Connection ${id}`,
  driver: 's3',
  endpoint: 'https://example.invalid',
  region: 'test',
  status: 'enabled',
  health: 'healthy',
  has_auth: false,
  management_generation: 4,
  auth_generation: 2,
  management_actions: connectionActions(),
})
const quotaSpace = (capacity: number | null = 200, generation = 3): ApiSchemas['StorageSpace'] => ({
  id: 11,
  connection_id: 1,
  name: 'Quota space',
  status: 'active',
  verified: true,
  management_generation: generation,
  management_actions: spaceActions(),
  capacity_bytes: capacity,
  reserved_bytes: 10,
  candidate_bytes: 8,
  live_bytes: 100,
  pending_delete_bytes: 10,
  available_bytes: capacity === null ? null : Math.max(capacity - 128, 0),
})
const deferred = <T>() => {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => (resolve = done))
  return { promise, resolve }
}
let scope: EffectScope
const state = (overrides: Partial<typeof api> = {}, admin = false) =>
  scope.run(() =>
    createStorageState(
      {
        ...api,
        listStorageChecks: vi.fn().mockResolvedValue({ items: [] }),
        getStorageCapabilities: vi
          .fn()
          .mockImplementation((scope: api.StorageScope) =>
            Promise.resolve(
              storageCapabilities(
                scope.kind === 'org' ? 'org' : 'user',
                scope.kind === 'org' ? scope.id : 1,
              ),
            ),
          ),
        ...overrides,
      },
      () => admin,
    ),
  )!
beforeEach(() => {
  changeSessionContext('/api/v1', 1, true)
  scope = effectScope()
})
afterEach(() => scope.stop())

describe('storage scope and concurrency', () => {
  it('retains fresh site runtime actions when quota fields are missing, never reuses stale runtime', async () => {
    const partial = {
      ...policyCapabilities(),
      mode: 'site_only',
      default_choice: 'site',
      generation: 1,
      logical_limit_bytes: null,
    }
    const read = vi.fn().mockResolvedValue(partial)
    const save = vi.fn()
    const store = state(
      {
        getStoragePolicy: read,
        setStoragePolicy: save,
        listStorageConnections: vi.fn().mockResolvedValue({ items: [connection(1, 'site', 0)] }),
        listStorageSpaces: vi.fn().mockResolvedValue({ items: [quotaSpace()] }),
      },
      true,
    )
    await store.load({ kind: 'site' })
    expect(await store.loadPolicy()).toBe(false)
    await store.loadSpaces(1)
    expect(store.connectionAllowed(1, 'check_read')).toBe(true)
    expect(store.spaceAllowed(1, 11, 'set_status')).toBe(true)
    expect(store.policyStale.value).toBe(true)
    await store.savePolicy({
      mode: 'site_only',
      default_choice: 'site',
      generation: 1,
      logical_limit_bytes: null,
      default_space_capacity_bytes: null,
    })
    expect(save).not.toHaveBeenCalled()
    store.markPolicyStale()
    expect(store.spaceAllowed(1, 11, 'set_status')).toBe(false)
    read.mockResolvedValue({ ...partial, runtime: undefined })
    await store.loadPolicy()
    expect(store.spaceAllowed(1, 11, 'set_status')).toBe(false)
  })
  it('shows authorized connection metadata while capability discovery is still pending', async () => {
    const capability = deferred<ApiSchemas['StorageCapabilities']>()
    const store = state({
      listStorageConnections: vi.fn().mockResolvedValue({ items: [connection()] }),
      getStorageCapabilities: vi.fn().mockReturnValue(capability.promise),
      listStorageSpaces: vi.fn().mockResolvedValue({ items: [] }),
    })
    const loading = store.load()
    await vi.waitFor(() => expect(store.connections.value.items).toHaveLength(1))
    expect(store.connections.value.loading).toBe(false)
    expect(store.connectionAllowed(1, 'check_read')).toBe(false)
    expect(await store.loadSpaces(1)).toBe(true)
    capability.resolve(storageCapabilities())
    await loading
    expect(store.connectionAllowed(1, 'check_read')).toBe(true)
  })
  it('refreshes policy after a generic legacy 409 without losing access or resending PUT', async () => {
    vi.useFakeTimers()
    const original: ApiSchemas['StoragePolicy'] = {
      ...policyCapabilities(),
      mode: 'both',
      default_choice: 'user',
      generation: 2,
      logical_limit_bytes: 100,
      default_space_capacity_bytes: null,
    }
    const read = vi
      .fn()
      .mockResolvedValueOnce(original)
      .mockResolvedValue({ ...original, generation: 3, logical_limit_bytes: 150 })
    const save = vi.fn().mockRejectedValue(new ApiError('conflict', 409))
    const store = state({ getStoragePolicy: read, setStoragePolicy: save }, true)
    store.setScope({ kind: 'site' })
    await store.loadPolicy()
    const unsubscribe = subscribeStorageRefresh({
      invalidate: store.markPolicyStale,
      refresh: store.loadPolicy,
    })
    try {
      expect(await store.savePolicy({ ...original, logical_limit_bytes: 200 })).toEqual({
        status: 'error',
      })
      expect(store.policy.value?.generation).toBe(2)
      expect(store.unknownWrites.value.policy).toBe(false)
      expect(store.denied.value).toBe(false)
      await vi.runAllTimersAsync()
      expect(read).toHaveBeenCalledTimes(2)
      expect(store.policy.value?.generation).toBe(3)
      expect(save).toHaveBeenCalledTimes(1)
    } finally {
      unsubscribe()
      vi.useRealTimers()
    }
  })
  it('invalidates in-flight admission reads immediately before the coalesced refresh starts', async () => {
    const listing = deferred<ApiSchemas['StorageConnectionList']>()
    const capability = deferred<ApiSchemas['StorageCapabilities']>()
    const store = state({
      listStorageConnections: vi.fn().mockReturnValue(listing.promise),
      getStorageCapabilities: vi.fn().mockReturnValue(capability.promise),
    })
    const reading = store.load()
    store.markStale()
    capability.resolve(storageCapabilities())
    listing.resolve({ items: [connection()] })
    expect(await reading).toBe(false)
    expect(store.connections.value.items).toEqual([])
    expect(store.connections.value.loading).toBe(false)
    expect(store.capabilityStatus.value).toBe('stale')
    expect(store.canCreateConnection.value).toBe(false)
  })
  it('rejects a stale space response and a stale policy response before new reads begin', async () => {
    const listing = deferred<ApiSchemas['StorageSpaceList']>()
    const policy = deferred<ApiSchemas['StoragePolicy']>()
    const store = state(
      {
        listStorageConnections: vi.fn().mockResolvedValue({ items: [connection()] }),
        listStorageSpaces: vi.fn().mockReturnValue(listing.promise),
        getStoragePolicy: vi.fn().mockReturnValue(policy.promise),
      },
      true,
    )
    await store.load()
    const spaces = store.loadSpaces(1)
    store.markStale()
    listing.resolve({ items: [] })
    expect(await spaces).toBe(false)
    expect(store.spaces.value[1]?.loaded).toBe(false)
    store.setScope({ kind: 'site' })
    const reading = store.loadPolicy()
    store.markPolicyStale()
    policy.resolve({
      ...policyCapabilities(),
      mode: 'site_only',
      default_choice: 'site',
      logical_limit_bytes: 100,
      default_space_capacity_bytes: null,
      generation: 1,
    })
    expect(await reading).toBe(false)
    expect(store.policy.value).toBeNull()
    expect(store.policyLoading.value).toBe(false)
  })
  it('keeps org capability 404 separate from an unsupported personal discovery route', async () => {
    organizationRoles.value = { 7: 'admin' }
    const capabilities = vi.fn().mockRejectedValue(new ApiError('ambiguous', 404))
    const store = state({
      getStorageCapabilities: capabilities,
      listStorageConnections: vi.fn().mockResolvedValue({ items: [connection(1, 'org', 7)] }),
    })
    await store.load({ kind: 'org', id: 7 })
    expect(store.capabilityStatus.value).toBe('error')
    expect(store.connections.value.items).toHaveLength(1)
    expect(store.canCreateConnection.value).toBe(false)
    expect(capabilities).toHaveBeenCalledTimes(1)
    expect(capabilities.mock.calls[0]?.[0]).toEqual({ kind: 'org', id: 7 })
  })
  it('discovers creation capability even for an empty list and never treats disabled deployment as an invalid snapshot', async () => {
    const cap = storageCapabilities()
    cap.runtime.deployment_enabled = false
    cap.management_actions.create_connection = storageAction(false)
    const create = vi.fn()
    const capabilities = vi.fn().mockResolvedValue(cap)
    const store = state({
      getStorageCapabilities: capabilities,
      listStorageConnections: vi.fn().mockResolvedValue({ items: [] }),
      createStorageConnection: create,
    })
    await store.load()
    expect(capabilities.mock.calls[0]?.[0]).toEqual({ kind: 'user' })
    expect(store.capabilityStatus.value).toBe('ready')
    expect(store.canCreateConnection.value).toBe(false)
    await store.createConnection({
      name: 'blocked',
      endpoint: 'https://example.invalid',
      region: 'test',
    })
    expect(create).not.toHaveBeenCalled()
  })
  it('keeps readable metadata on unsupported capability routes and incomplete object actions', async () => {
    const item = connection()
    delete (item.management_actions as Partial<typeof item.management_actions>).check_write
    const store = state({
      getStorageCapabilities: vi.fn().mockRejectedValue(new ApiError('missing route', 404)),
      listStorageConnections: vi.fn().mockResolvedValue({ items: [item] }),
    })
    await store.load()
    expect(store.connections.value.items).toHaveLength(1)
    expect(store.capabilityStatus.value).toBe('unsupported')
    expect(store.denied.value).toBe(false)
    expect(store.connectionAllowed(1, 'check_read')).toBe(false)
  })
  it.each([
    'create_space',
    'authorize_read',
    'authorize_write',
    'check_read',
    'check_write',
    'revoke_auth',
    'set_status',
  ] as const)('uses the %s action without deriving permission from deployment', async (action) => {
    const item = connection()
    item.management_actions[action] = storageAction(false, ['storage_maintenance'])
    const cap = storageCapabilities()
    cap.runtime.deployment_enabled = false
    const request = vi.fn()
    const store = state({
      getStorageCapabilities: vi.fn().mockResolvedValue(cap),
      listStorageConnections: vi.fn().mockResolvedValue({ items: [item] }),
      createStorageSpace: request,
      authorizeStorage: request,
      checkStorageConnection: request,
      revokeStorageAuthorization: request,
      setStorageConnectionState: request,
    })
    await store.load()
    expect(store.connectionAllowed(1, action)).toBe(false)
    const other = action === 'revoke_auth' ? 'check_read' : 'revoke_auth'
    expect(store.connectionAllowed(1, other)).toBe(true)
    const result =
      action === 'create_space'
        ? await store.createSpace(1, { name: 'space', bucket: 'bucket', capacity_bytes: 100 })
        : action === 'authorize_read' || action === 'authorize_write'
          ? await store.authorize(1, {
              access_key_id: 'id',
              secret_access_key: 'secret',
              write_check: action === 'authorize_write',
            })
          : action === 'check_read' || action === 'check_write'
            ? await store.check(1, action === 'check_write')
            : action === 'revoke_auth'
              ? await store.revoke(1)
              : await store.setConnectionState(1, 'disabled')
    expect(result).toEqual({ status: 'error' })
    expect(request).not.toHaveBeenCalled()
  })
  it('retains metadata and definite write outcome on a policy refusal returned with legacy 403', async () => {
    const store = state({
      listStorageConnections: vi.fn().mockResolvedValue({ items: [connection()] }),
      checkStorageConnection: vi
        .fn()
        .mockRejectedValue(
          storageRequestError({ status: 403 }, { error_code: 'storage_policy_violation' }),
        ),
    })
    await store.load()
    expect(await store.check(1)).toEqual({ status: 'error' })
    expect(store.connections.value.items).toHaveLength(1)
    expect(store.denied.value).toBe(false)
    expect(store.unknownWrites.value['connection:1']).toBe(false)
  })
  it('rejects quota-only changes to restricted policies and permits legal metadata saves during maintenance', async () => {
    const original: ApiSchemas['StoragePolicy'] = {
      ...policyCapabilities(),
      runtime: { deployment_enabled: false, maintenance: true },
      allowed_policy_modes: ['site_only'],
      mode: 'both',
      default_choice: 'user',
      generation: 2,
      logical_limit_bytes: 100,
      default_space_capacity_bytes: null,
    }
    const save = vi
      .fn()
      .mockResolvedValue({ ...original, mode: 'site_only', default_choice: 'site', generation: 3 })
    const read = vi.fn().mockResolvedValue(original)
    const store = state({ getStoragePolicy: read, setStoragePolicy: save }, true)
    store.setScope({ kind: 'site' })
    await store.loadPolicy()
    expect(await store.savePolicy({ ...original, logical_limit_bytes: 200 })).toEqual({
      status: 'error',
    })
    expect(save).not.toHaveBeenCalled()
    await store.savePolicy({
      ...original,
      mode: 'site_only',
      default_choice: 'site',
      logical_limit_bytes: 200,
      default_space_capacity_bytes: null,
    })
    expect(save.mock.calls[0]?.[0]).toEqual({
      mode: 'site_only',
      default_choice: 'site',
      logical_limit_bytes: 200,
      default_space_capacity_bytes: null,
      generation: 2,
    })
    expect(store.policy.value?.generation).toBe(3)
    read.mockRejectedValue(new ApiError('temporary', 503))
    await store.loadPolicy()
    expect(store.policySavedPendingRefresh.value).toBe(true)
    expect(store.policy.value?.generation).toBe(3)
    expect(save).toHaveBeenCalledTimes(1)
  })
  it('revokes during a slow authorization and never restores its late result', async () => {
    const late = deferred<ApiSchemas['StorageConnection']>()
    const revoked = { ...connection(), has_auth: false, management_generation: 5 }
    const revoke = vi.fn().mockResolvedValue(revoked)
    const store = state({
      listStorageConnections: vi.fn().mockResolvedValue({ items: [connection()] }),
      authorizeStorage: vi.fn().mockReturnValue(late.promise),
      revokeStorageAuthorization: revoke,
    })
    await store.load()
    const authorizing = store.authorize(1, {
      access_key_id: 'id',
      secret_access_key: 'secret',
      write_check: true,
    })
    expect(store.busy.value['connection:1']).toBe(true)
    expect((await store.revoke(1)).status).toBe('success')
    expect(revoke.mock.calls[0]?.[1]).toEqual({ expected_generation: 4 })
    late.resolve({ ...connection(), has_auth: true })
    expect(await authorizing).toEqual({ status: 'stale' })
    expect(store.connections.value.items[0]).toEqual(revoked)
    expect(store.busy.value['connection:1']).toBe(false)
  })

  it('reads durable checks after an unknown response without resubmitting a check', async () => {
    const checking = vi.fn().mockRejectedValue(storageRequestError(undefined, { check_id: 9 }))
    const fact = {
      check_id: 9,
      connection_id: 1,
      status: 'completed',
      authorization_activated: false,
      cleanup_status: 'blocked',
    } as ApiSchemas['StorageCheck']
    const store = state({
      listStorageConnections: vi.fn().mockResolvedValue({ items: [connection()] }),
      checkStorageConnection: checking,
      getStorageCheck: vi.fn().mockResolvedValue(fact),
      listStorageChecks: vi.fn().mockResolvedValue({ items: [fact] }),
    })
    await store.load()
    expect(await store.check(1, true)).toEqual({ status: 'unknown' })
    await store.loadChecks(1)
    expect(store.checks.value[1]?.items).toEqual([fact])
    expect(checking).toHaveBeenCalledTimes(1)
    expect(store.unknownWrites.value['connection:1']).toBe(true)
  })

  it('discards check history after switching scope and does not leak another connection', async () => {
    const late = deferred<ApiSchemas['StorageCheckList']>()
    const store = state({
      listStorageConnections: vi.fn().mockResolvedValue({ items: [connection()] }),
      listStorageChecks: vi.fn().mockReturnValue(late.promise),
    })
    await store.load()
    const loading = store.loadChecks(1)
    store.setScope({ kind: 'org', id: 7 })
    late.resolve({ items: [{ check_id: 9, connection_id: 1 } as ApiSchemas['StorageCheck']] })
    expect(await loading).toBe(false)
    expect(store.checks.value).toEqual({})
  })
  it('publishes asynchronously loaded space metadata through Vue reactivity', async () => {
    const result = deferred<ApiSchemas['StorageSpaceList']>()
    const store = state({
      listStorageConnections: vi.fn().mockResolvedValue({ items: [connection()] }),
      listStorageSpaces: vi.fn().mockReturnValue(result.promise),
    })
    await store.load()
    const observed = vi.fn()
    scope.run(() => watch(() => store.spaces.value[1]?.items.length, observed, { flush: 'sync' }))
    const pending = store.loadSpaces(1)
    result.resolve({
      items: [
        {
          id: 2,
          connection_id: 1,
          name: 'space',
          status: 'active',
          verified: true,
          management_generation: 1,
          management_actions: spaceActions(),
          capacity_bytes: 100,
          reserved_bytes: 0,
          candidate_bytes: 0,
          live_bytes: 0,
          pending_delete_bytes: 0,
        },
      ],
    })
    await pending
    expect(observed.mock.calls.at(-1)?.[0]).toBe(1)
  })
  it('clears only a denied connection while retaining unrelated readable connections', async () => {
    const store = state({
      listStorageConnections: vi.fn().mockResolvedValue({ items: [connection(1), connection(2)] }),
      checkStorageConnection: vi.fn().mockRejectedValue(new ApiError('denied', 403)),
    })
    await store.load()
    expect(await store.check(1)).toEqual({ status: 'error' })
    expect(store.connections.value.items.map((item) => item.id)).toEqual([2])
    expect(store.denied.value).toBe(false)
  })
  it('filters mixed lists to the explicit owner, without personal fallback for org scopes', async () => {
    const list = vi.fn().mockResolvedValue({
      items: [
        connection(),
        connection(2, 'user', 2),
        connection(3, 'org', 7),
        connection(4, 'site', 0),
      ],
    })
    const store = state({ listStorageConnections: list })
    await store.load({ kind: 'user' })
    expect(store.connections.value.items.map((item) => item.id)).toEqual([1])
    organizationRoles.value = { 7: 'admin' }
    await store.load({ kind: 'org', id: 7 })
    expect(store.connections.value.items.map((item) => item.id)).toEqual([3])
    expect(list.mock.lastCall?.[0]).toEqual({ kind: 'org', id: 7 })
  })

  it('does not request organization metadata for an unconfirmed member', async () => {
    const list = vi.fn()
    const store = state({ listStorageConnections: list })
    organizationRoles.value = { 7: 'member' }
    expect(await store.load({ kind: 'org', id: 7 })).toBe(false)
    expect(list).not.toHaveBeenCalled()
    expect(store.denied.value).toBe(true)
  })

  it('discards a late response after changing identity or service', async () => {
    const response = deferred<ApiSchemas['StorageConnectionList']>()
    const list = vi.fn().mockReturnValue(response.promise)
    const store = state({ listStorageConnections: list })
    const loading = store.load()
    changeSessionContext('/different-api', 2)
    expect(list.mock.calls[0]?.[1].signal.aborted).toBe(true)
    response.resolve({ items: [connection()] })
    expect(await loading).toBe(false)
    expect(store.connections.value.items).toEqual([])
  })

  it('clears organization metadata and rejects in-flight authorization after demotion', async () => {
    const auth = deferred<ApiSchemas['StorageConnection']>()
    const authorize = vi.fn().mockReturnValue(auth.promise)
    const store = state({
      listStorageConnections: vi.fn().mockResolvedValue({ items: [connection(3, 'org', 7)] }),
      authorizeStorage: authorize,
    })
    organizationRoles.value = { 7: 'admin' }
    await store.load({ kind: 'org', id: 7 })
    const writing = store.authorize(3, {
      access_key_id: 'id',
      secret_access_key: 'never-in-state',
      write_check: false,
    })
    organizationRoles.value = { 7: 'member' }
    invalidateOrganization(7)
    auth.resolve({ ...connection(3, 'org', 7), has_auth: true })
    expect(await writing).toEqual({ status: 'stale' })
    expect(store.connections.value.items).toEqual([])
    expect(store.canManage.value).toBe(false)
    expect(
      JSON.stringify(
        Object.fromEntries(
          Object.entries(store)
            .filter(([, value]) => isRef(value))
            .map(([key, value]) => [key, isRef(value) ? value.value : null]),
        ),
      ),
    ).not.toContain('never-in-state')
  })

  it('passes the connection management generation and prevents simultaneous management writes', async () => {
    const check = deferred<ApiSchemas['StorageConnection']>()
    const checking = vi.fn().mockReturnValue(check.promise),
      authorize = vi.fn()
    const store = state({
      listStorageConnections: vi.fn().mockResolvedValue({ items: [connection()] }),
      checkStorageConnection: checking,
      authorizeStorage: authorize,
    })
    await store.load()
    const pending = store.check(1)
    expect(checking.mock.calls[0]?.[1]).toEqual({ expected_generation: 4, write_check: false })
    expect(
      await store.authorize(1, {
        access_key_id: 'id',
        secret_access_key: 'secret',
        write_check: false,
      }),
    ).toEqual({ status: 'error' })
    expect(authorize).not.toHaveBeenCalled()
    check.resolve(connection())
    await pending
    expect(store.connections.value.stale).toBe(true)
  })

  it('preserves same-scope metadata after a transient failure, but clears it on access denial', async () => {
    const list = vi
      .fn()
      .mockResolvedValueOnce({ items: [connection()] })
      .mockRejectedValueOnce(new ApiError('upstream detail', 503))
      .mockRejectedValueOnce(new ApiError('denied', 403))
    const store = state({ listStorageConnections: list })
    await store.load()
    await store.load()
    expect(store.connections.value.items).toHaveLength(1)
    expect(store.connections.value.stale).toBe(true)
    expect(store.connections.value.error).not.toContain('upstream detail')
    await store.load()
    expect(store.connections.value.items).toEqual([])
    expect(store.denied.value).toBe(true)
  })

  it('allows metadata refresh during slow authorization without reopening ordinary writes', async () => {
    const result = deferred<ApiSchemas['StorageConnection']>()
    const list = vi.fn().mockResolvedValue({ items: [connection()] })
    const listSpaces = vi.fn().mockResolvedValue({ items: [] })
    const store = state({
      listStorageConnections: list,
      listStorageSpaces: listSpaces,
      authorizeStorage: vi.fn().mockReturnValue(result.promise),
    })
    await store.load()
    const pending = store.authorize(1, {
      access_key_id: 'id',
      secret_access_key: 'secret',
      write_check: false,
    })
    store.markStale()
    expect(store.connectionAllowed(1, 'revoke_auth')).toBe(true)
    expect(await store.load()).toBe(true)
    expect(await store.loadSpaces(1)).toBe(false)
    expect(list).toHaveBeenCalledTimes(2)
    expect(listSpaces).not.toHaveBeenCalled()
    result.resolve({ ...connection(), management_generation: 5, has_auth: true })
    await pending
    expect(store.ready.value).toBe(false)
    list.mockResolvedValue({
      items: [{ ...connection(), management_generation: 5, has_auth: true }],
    })
    expect(await store.load()).toBe(true)
    expect(store.connections.value.items[0]?.management_generation).toBe(5)
    expect(store.ready.value).toBe(true)
  })

  it('requires a fresh policy read after saving and never reads an in-flight policy snapshot', async () => {
    const original: ApiSchemas['StoragePolicy'] = {
      ...policyCapabilities(),
      mode: 'site_only',
      default_choice: 'site',
      generation: 2,
      logical_limit_bytes: 100,
      default_space_capacity_bytes: null,
    }
    const result = deferred<ApiSchemas['StoragePolicy']>()
    const read = vi.fn().mockResolvedValue(original)
    const save = vi.fn().mockReturnValue(result.promise)
    const store = state({ getStoragePolicy: read, setStoragePolicy: save }, true)
    store.setScope({ kind: 'site' })
    await store.loadPolicy()
    const pending = store.savePolicy(original)
    expect(await store.loadPolicy()).toBe(false)
    expect(read).toHaveBeenCalledTimes(1)
    result.resolve({ ...original, generation: 3 })
    await pending
    expect(store.policyStale.value).toBe(true)
    expect(await store.savePolicy(original)).toEqual({ status: 'error' })
    expect(save).toHaveBeenCalledTimes(1)
    read.mockResolvedValue({ ...original, generation: 3 })
    expect(await store.loadPolicy()).toBe(true)
    expect(store.policy.value?.generation).toBe(3)
    expect(store.policyStale.value).toBe(false)
  })

  it('retains an unknown create result and never resubmits a new connection automatically', async () => {
    const create = vi.fn().mockRejectedValue(new TypeError('network'))
    const store = state({
      listStorageConnections: vi.fn().mockResolvedValue({ items: [] }),
      createStorageConnection: create,
    })
    await store.load()
    const body = { name: 'new', endpoint: 'https://example.invalid', region: 'test' }
    expect(await store.createConnection(body)).toEqual({ status: 'unknown' })
    await store.createConnection(body)
    expect(create).toHaveBeenCalledTimes(1)
    expect(store.unknownWrites.value.create).toBe(true)
  })

  it('never reads admin policy for a personal scope, even for an admin identity', async () => {
    const policy = vi.fn().mockResolvedValue({
      ...policyCapabilities(),
      mode: 'site_only',
      default_choice: 'site',
      generation: 2,
      logical_limit_bytes: 100,
      default_space_capacity_bytes: null,
    })
    const store = state({ getStoragePolicy: policy }, true)
    expect(await store.loadPolicy()).toBe(false)
    expect(policy).not.toHaveBeenCalled()
    store.setScope({ kind: 'site' })
    expect(await store.loadPolicy()).toBe(true)
    expect(policy).toHaveBeenCalledTimes(1)
  })
})

describe('storage space summaries', () => {
  it('merges duplicate reads, reuses a fresh cache, and supports an explicit refresh', async () => {
    const result = deferred<ApiSchemas['StorageSpaceList']>()
    const read = vi.fn().mockReturnValueOnce(result.promise).mockResolvedValue({ items: [] })
    const store = state({
      listStorageConnections: vi.fn().mockResolvedValue({ items: [connection()] }),
      listStorageSpaces: read,
    })
    await store.load()
    expect(read).not.toHaveBeenCalled()
    const first = store.loadSpaces(1)
    const second = store.loadSpaces(1)
    expect(first).toBe(second)
    result.resolve({ items: [] })
    expect(await first).toBe(true)
    await second
    await store.loadSpaces(1)
    await store.loadSpaceSummaries()
    expect(read).toHaveBeenCalledTimes(1)
    expect(await store.loadSpaces(1, true)).toBe(true)
    expect(read).toHaveBeenCalledTimes(2)
  })

  it('limits a shared summary queue to four concurrent connections', async () => {
    const releases = new Map<number, () => void>()
    let active = 0
    let peak = 0
    const read = vi.fn().mockImplementation((id: number) => {
      active++
      peak = Math.max(peak, active)
      return new Promise<ApiSchemas['StorageSpaceList']>((resolve) => {
        releases.set(id, () => {
          active--
          resolve({ items: [] })
        })
      })
    })
    const store = state({
      listStorageConnections: vi.fn().mockResolvedValue({
        items: Array.from({ length: 10 }, (_, index) => connection(index + 1)),
      }),
      listStorageSpaces: read,
    })
    await store.load()
    const first = store.loadSpaceSummaries()
    const second = store.loadSpaceSummaries()
    expect(first).toBe(second)
    expect(read).toHaveBeenCalledTimes(4)
    for (let id = 1; id <= 10; id++) {
      await vi.waitFor(() => expect(releases.has(id)).toBe(true))
      releases.get(id)!()
    }
    await Promise.all([first, second])
    expect(read).toHaveBeenCalledTimes(10)
    expect(peak).toBe(4)
    expect(Object.values(store.spaces.value).every((value) => value.loaded)).toBe(true)
  })

  it('invalidates space caches on list refresh while retaining the last readable ledger', async () => {
    const read = vi.fn().mockResolvedValue({ items: [] })
    const refresh = deferred<ApiSchemas['StorageConnectionList']>()
    const list = vi
      .fn()
      .mockResolvedValueOnce({ items: [connection()] })
      .mockReturnValue(refresh.promise)
    const store = state({ listStorageConnections: list, listStorageSpaces: read })
    await store.load()
    await store.loadSpaceSummaries()
    const loading = store.load()
    expect(store.spaces.value[1]?.loaded).toBe(true)
    expect(store.spaces.value[1]?.stale).toBe(true)
    expect(await store.loadSpaces(1)).toBe(false)
    refresh.resolve({ items: [connection()] })
    await loading
    await store.loadSpaceSummaries()
    expect(read).toHaveBeenCalledTimes(2)
    expect(store.spaces.value[1]?.stale).toBe(false)
  })

  it('shares the four-request limit with an explicitly opened connection', async () => {
    const result = deferred<ApiSchemas['StorageSpaceList']>()
    const read = vi.fn().mockReturnValue(result.promise)
    const store = state({
      listStorageConnections: vi.fn().mockResolvedValue({
        items: Array.from({ length: 6 }, (_, index) => connection(index + 1)),
      }),
      listStorageSpaces: read,
    })
    await store.load()
    const summaries = store.loadSpaceSummaries()
    const detail = store.loadSpaces(6)
    expect(read).toHaveBeenCalledTimes(4)
    expect(store.spaces.value[6]?.loading).toBe(true)
    result.resolve({ items: [] })
    await Promise.all([summaries, detail])
    expect(read).toHaveBeenCalledTimes(6)
    expect(store.spaces.value[6]?.loaded).toBe(true)
  })

  it('does not let an old read or its cleanup satisfy a refreshed request', async () => {
    const old = deferred<ApiSchemas['StorageSpaceList']>()
    const fresh = deferred<ApiSchemas['StorageSpaceList']>()
    const read = vi.fn().mockReturnValueOnce(old.promise).mockReturnValueOnce(fresh.promise)
    const store = state({
      listStorageConnections: vi.fn().mockResolvedValue({ items: [connection()] }),
      listStorageSpaces: read,
    })
    await store.load()
    const prior = store.loadSpaces(1)
    await store.load()
    const current = store.loadSpaces(1)
    old.resolve({ items: [] })
    expect(await prior).toBe(false)
    expect(store.spaces.value[1]?.loading).toBe(true)
    expect(store.loadSpaces(1)).toBe(current)
    fresh.resolve({ items: [] })
    expect(await current).toBe(true)
    expect(read).toHaveBeenCalledTimes(2)
  })

  it('clears shared reads across sessions and stops the previous queue', async () => {
    const old = deferred<ApiSchemas['StorageSpaceList']>()
    const read = vi.fn().mockReturnValue(old.promise)
    const list = vi.fn().mockResolvedValue({
      items: Array.from({ length: 6 }, (_, index) => connection(index + 1)),
    })
    const store = state({ listStorageConnections: list, listStorageSpaces: read })
    await store.load()
    const prior = store.loadSpaceSummaries()
    expect(read).toHaveBeenCalledTimes(4)
    changeSessionContext('/second-api', 2)
    list.mockResolvedValue({ items: [connection(1, 'user', 2)] })
    read.mockResolvedValue({ items: [] })
    await store.load()
    await store.loadSpaceSummaries()
    expect(store.spaces.value[1]?.loaded).toBe(true)
    old.resolve({ items: [] })
    await prior
    expect(read).toHaveBeenCalledTimes(5)
    expect(Object.keys(store.spaces.value)).toEqual(['1'])
  })

  it('forgets only the denied connection and continues loading authorized summaries', async () => {
    const read = vi
      .fn()
      .mockImplementation((id: number) =>
        id === 1 ? Promise.reject(new ApiError('forbidden', 403)) : Promise.resolve({ items: [] }),
      )
    const store = state({
      listStorageConnections: vi.fn().mockResolvedValue({ items: [connection(1), connection(2)] }),
      listStorageSpaces: read,
    })
    await store.load()
    await store.loadSpaceSummaries()
    expect(store.connections.value.items.map((value) => value.id)).toEqual([2])
    expect(store.spaces.value[1]).toBeUndefined()
    expect(store.spaces.value[2]?.loaded).toBe(true)
    expect(await store.loadSpaces(1)).toBe(false)
    expect(store.denied.value).toBe(false)
  })

  it('stops queued organization reads and clears metadata after demotion', async () => {
    const result = deferred<ApiSchemas['StorageSpaceList']>()
    const read = vi.fn().mockReturnValue(result.promise)
    organizationRoles.value = { 7: 'admin' }
    const store = state({
      listStorageConnections: vi.fn().mockResolvedValue({
        items: Array.from({ length: 6 }, (_, index) => connection(index + 1, 'org', 7)),
      }),
      listStorageSpaces: read,
    })
    await store.load({ kind: 'org', id: 7 })
    const reading = store.loadSpaceSummaries()
    organizationRoles.value = { 7: 'member' }
    invalidateOrganization(7)
    result.resolve({ items: [] })
    await reading
    expect(read).toHaveBeenCalledTimes(4)
    expect(store.spaces.value).toEqual({})
    expect(store.connections.value.items).toEqual([])
  })
})

describe('space quota writes and unknown recovery', () => {
  async function quotaState(overrides: Partial<typeof api> = {}) {
    const read = vi.fn().mockResolvedValue({ items: [quotaSpace()] })
    const save = vi.fn().mockResolvedValue(quotaSpace(100, 4))
    const store = state({
      listStorageConnections: vi.fn().mockResolvedValue({ items: [connection()] }),
      listStorageSpaces: read,
      setStorageSpaceQuota: save,
      ...overrides,
    })
    await store.load()
    await store.loadSpaces(1)
    return { store, read, save }
  }
  it('allows lowering below all four accounts and retains the confirmed result against lower generations', async () => {
    const { store, save, read } = await quotaState()
    expect(
      await store.setSpaceQuota(1, 11, { capacity_bytes: 100, expected_generation: 3 }),
    ).toEqual({ status: 'success', value: quotaSpace(100, 4) })
    expect(save.mock.calls[0]?.slice(0, 2)).toEqual([
      11,
      { capacity_bytes: 100, expected_generation: 3 },
    ])
    expect(store.spaces.value[1]?.items[0]).toMatchObject({
      capacity_bytes: 100,
      live_bytes: 100,
      pending_delete_bytes: 10,
      available_bytes: 0,
    })
    await store.load()
    expect(await store.loadSpaces(1)).toBe(false)
    expect(store.spaces.value[1]?.items[0]?.management_generation).toBe(4)
    read.mockResolvedValue({ items: [quotaSpace(100, 4)] })
    expect(await store.loadSpaces(1, true)).toBe(true)
    expect(save).toHaveBeenCalledTimes(1)
  })
  it.each(['set_status', 'set_quota'] as const)(
    'keeps %s independent from the other space action',
    async (blocked) => {
      const space = quotaSpace()
      space.management_actions[blocked] = storageAction(false)
      const { store, save } = await quotaState({
        listStorageSpaces: vi.fn().mockResolvedValue({ items: [space] }),
      })
      expect(store.spaceAllowed(1, 11, blocked)).toBe(false)
      expect(store.spaceAllowed(1, 11, blocked === 'set_status' ? 'set_quota' : 'set_status')).toBe(
        true,
      )
      if (blocked === 'set_quota') {
        await store.setSpaceQuota(1, 11, { capacity_bytes: null, expected_generation: 3 })
        expect(save).not.toHaveBeenCalled()
      }
    },
  )
  it('rejects missing quota actions, incomplete ledgers, invalid quota values, and stale draft generations before transport', async () => {
    const { store, save } = await quotaState()
    for (const capacity of [undefined, 0, -1, NaN, Number.POSITIVE_INFINITY])
      await store.setSpaceQuota(1, 11, {
        capacity_bytes: capacity as number,
        expected_generation: 3,
      })
    await store.setSpaceQuota(1, 11, { capacity_bytes: null, expected_generation: 2 })
    delete (
      store.spaces.value[1]!.items[0]!.management_actions as Partial<
        ApiSchemas['StorageSpace']['management_actions']
      >
    ).set_quota
    expect(store.spaceAllowed(1, 11, 'set_quota')).toBe(false)
    await store.setSpaceQuota(1, 11, { capacity_bytes: null, expected_generation: 3 })
    store.spaces.value[1]!.items[0] = { ...quotaSpace(), available_bytes: 999 }
    expect(store.spaceAllowed(1, 11, 'set_quota')).toBe(false)
    expect(save).not.toHaveBeenCalled()
  })
  it('retains attempted quota and baseline, reads without unlocking, and requires the newest review revision', async () => {
    const save = vi.fn().mockRejectedValue(new TypeError('response lost'))
    const { store, read } = await quotaState({ setStorageSpaceQuota: save })
    expect(
      await store.setSpaceQuota(1, 11, { capacity_bytes: null, expected_generation: 3 }),
    ).toEqual({ status: 'unknown' })
    const pending = store.quotaRecoveries.value[11]!
    expect(pending).toMatchObject({
      baseline: { capacity_bytes: 200, management_generation: 3 },
      submitted: { capacity_bytes: null, expected_generation: 3 },
      state: 'needs_read',
    })
    expect(store.connectionAllowed(1, 'revoke_auth')).toBe(true)
    read.mockResolvedValue({ items: [quotaSpace(null, 4)] })
    await store.load()
    await store.loadSpaces(1)
    const oldRevision = store.quotaRecoveries.value[11]!.reviewRevision
    await store.loadSpaces(1, true)
    expect(store.unknownWrites.value['connection:1']).toBe(true)
    await store.setSpaceQuota(1, 11, { capacity_bytes: 100, expected_generation: 4 })
    expect(save).toHaveBeenCalledTimes(1)
    expect(store.acknowledgeSpaceQuotaUnknown(11, pending.attemptId, oldRevision)).toBeNull()
    expect(
      store.acknowledgeSpaceQuotaUnknown(
        11,
        pending.attemptId,
        store.quotaRecoveries.value[11]!.reviewRevision,
      ),
    ).toEqual(quotaSpace(null, 4))
    expect(store.unknownWrites.value['connection:1']).toBeUndefined()
    expect(store.spaceAllowed(1, 11, 'set_quota')).toBe(true)
  })
  it('keeps unknown locked after failed or incomplete reads and clears recovery on a service switch', async () => {
    const { store, read } = await quotaState({
      setStorageSpaceQuota: vi.fn().mockRejectedValue(new TypeError('lost')),
    })
    await store.setSpaceQuota(1, 11, { capacity_bytes: 100, expected_generation: 3 })
    await store.load()
    read.mockRejectedValue(new TypeError('read failed'))
    await store.loadSpaces(1)
    expect(store.quotaRecoveries.value[11]?.state).toBe('error')
    read.mockResolvedValue({ items: [{ ...quotaSpace(), available_bytes: undefined }] })
    await store.loadSpaces(1, true)
    const pending = store.quotaRecoveries.value[11]!
    expect(
      store.acknowledgeSpaceQuotaUnknown(11, pending.attemptId, pending.reviewRevision),
    ).toBeNull()
    changeSessionContext('/different-service', 1, true)
    expect(store.quotaRecoveries.value).toEqual({})
    expect(store.unknownWrites.value).toEqual({})
  })
  it('shares a connection write lock while preserving emergency revocation and discarding superseded quota responses', async () => {
    const result = deferred<ApiSchemas['StorageSpace']>()
    const status = vi.fn()
    const { store } = await quotaState({
      setStorageSpaceQuota: vi.fn().mockReturnValue(result.promise),
      setStorageSpaceState: status,
      revokeStorageAuthorization: vi
        .fn()
        .mockResolvedValue({ ...connection(), management_generation: 5 }),
    })
    const pending = store.setSpaceQuota(1, 11, { capacity_bytes: 100, expected_generation: 3 })
    await store.setSpaceState(1, 11, 'disabled')
    expect(status).not.toHaveBeenCalled()
    expect(store.connectionAllowed(1, 'revoke_auth')).toBe(true)
    expect((await store.revoke(1)).status).toBe('success')
    result.resolve(quotaSpace(100, 4))
    expect(await pending).toEqual({ status: 'unknown' })
    expect(store.quotaRecoveries.value[11]?.submitted.capacity_bytes).toBe(100)
    expect(store.connections.value.items[0]?.management_generation).toBe(5)
  })
  it('discards a quota response after changing organization and does not retain its pending attempt', async () => {
    const result = deferred<ApiSchemas['StorageSpace']>()
    const { store } = await quotaState({
      setStorageSpaceQuota: vi.fn().mockReturnValue(result.promise),
    })
    const pending = store.setSpaceQuota(1, 11, { capacity_bytes: 100, expected_generation: 3 })
    store.setScope({ kind: 'org', id: 7 })
    result.resolve(quotaSpace(100, 4))
    expect(await pending).toEqual({ status: 'stale' })
    expect(store.quotaRecoveries.value).toEqual({})
  })
  it('retains unknown evidence while quota permission is denied and only permits review after permission returns', async () => {
    const { store, read } = await quotaState({
      setStorageSpaceQuota: vi.fn().mockRejectedValue(new TypeError('lost')),
    })
    await store.setSpaceQuota(1, 11, { capacity_bytes: null, expected_generation: 3 })
    const denied = quotaSpace(null, 4)
    denied.management_actions.set_quota = storageAction(false)
    read.mockResolvedValue({ items: [denied] })
    await store.load()
    await store.loadSpaces(1)
    const pending = store.quotaRecoveries.value[11]!
    expect(
      store.acknowledgeSpaceQuotaUnknown(11, pending.attemptId, pending.reviewRevision),
    ).toBeNull()
    expect(store.quotaRecoveries.value[11]?.submitted.capacity_bytes).toBeNull()
    read.mockResolvedValue({ items: [quotaSpace(null, 4)] })
    await store.loadSpaces(1, true)
    expect(
      store.acknowledgeSpaceQuotaUnknown(
        11,
        pending.attemptId,
        store.quotaRecoveries.value[11]!.reviewRevision,
      ),
    ).toEqual(quotaSpace(null, 4))
  })
  it('keeps all five policy fields for unknown recovery and only adopts a reviewed complete GET', async () => {
    const baseline: ApiSchemas['StoragePolicy'] = {
      ...policyCapabilities(),
      mode: 'both',
      default_choice: 'user',
      generation: 2,
      logical_limit_bytes: 100,
      default_space_capacity_bytes: 200,
    }
    const read = vi.fn().mockResolvedValue(baseline),
      save = vi.fn().mockRejectedValue(new TypeError('lost'))
    const store = state({ getStoragePolicy: read, setStoragePolicy: save }, true)
    store.setScope({ kind: 'site' })
    await store.loadPolicy()
    await store.savePolicy({
      ...baseline,
      logical_limit_bytes: null,
      default_space_capacity_bytes: null,
    })
    expect(Object.keys(store.policyRecovery.value!.submitted).sort()).toEqual([
      'default_choice',
      'default_space_capacity_bytes',
      'generation',
      'logical_limit_bytes',
      'mode',
    ])
    const latest = {
      ...baseline,
      generation: 3,
      logical_limit_bytes: null,
      default_space_capacity_bytes: null,
    }
    read.mockResolvedValue(latest)
    await store.loadPolicy()
    expect(store.policy.value).toEqual(baseline)
    expect(store.unknownWrites.value.policy).toBe(true)
    const revision = store.policyRecovery.value!.reviewRevision
    await store.loadPolicy()
    const pending = store.policyRecovery.value!
    expect(store.acknowledgePolicyUnknown(pending.attemptId, revision)).toBeNull()
    expect(store.acknowledgePolicyUnknown(pending.attemptId, pending.reviewRevision)).toEqual(
      latest,
    )
    expect(store.policy.value).toEqual(latest)
    expect(store.policyRecovery.value).toBeNull()
    expect(save).toHaveBeenCalledTimes(1)
  })
})

describe('storage check history freshness', () => {
  const fact = (id: number) =>
    ({
      check_id: id,
      connection_id: 1,
      status: 'completed',
      authorization_activated: false,
      cleanup_status: 'complete',
    }) as ApiSchemas['StorageCheck']

  it('retains a fresh history snapshot while other detail sections are read', async () => {
    const read = vi.fn().mockResolvedValue({ items: [fact(1)] })
    const store = state({
      listStorageConnections: vi.fn().mockResolvedValue({ items: [connection()] }),
      listStorageChecks: read,
      listStorageSpaces: vi.fn().mockResolvedValue({ items: [] }),
    })
    await store.load()
    expect(read).not.toHaveBeenCalled()
    await store.loadChecks(1)
    await store.loadSpaces(1)
    await store.loadSpaceSummaries()
    expect(store.checks.value[1]).toMatchObject({
      items: [fact(1)],
      loaded: true,
      stale: false,
      loading: false,
    })
    expect(read).toHaveBeenCalledTimes(1)
  })

  it.each(['load', 'markStale'] as const)(
    'marks cached history stale on %s and reloads only when explicitly requested',
    async (refresh) => {
      const read = vi
        .fn()
        .mockResolvedValueOnce({ items: [fact(1)] })
        .mockResolvedValue({ items: [fact(2)] })
      const store = state({
        listStorageConnections: vi.fn().mockResolvedValue({ items: [connection()] }),
        listStorageChecks: read,
      })
      await store.load()
      await store.loadChecks(1)
      await store[refresh]()
      expect(store.checks.value[1]).toMatchObject({
        items: [fact(1)],
        loaded: true,
        stale: true,
        loading: false,
      })
      expect(read).toHaveBeenCalledTimes(1)
      await store.loadChecks(1)
      expect(store.checks.value[1]).toMatchObject({
        items: [fact(2)],
        loaded: true,
        stale: false,
        loading: false,
      })
      expect(read).toHaveBeenCalledTimes(2)
    },
  )

  it.each(['load', 'markStale'] as const)(
    'rejects a history response invalidated by %s without clearing the replacement loading state',
    async (refresh) => {
      const old = deferred<ApiSchemas['StorageCheckList']>()
      const fresh = deferred<ApiSchemas['StorageCheckList']>()
      const read = vi
        .fn()
        .mockResolvedValueOnce({ items: [fact(1)] })
        .mockReturnValueOnce(old.promise)
        .mockReturnValueOnce(fresh.promise)
      const store = state({
        listStorageConnections: vi.fn().mockResolvedValue({ items: [connection()] }),
        listStorageChecks: read,
      })
      await store.load()
      await store.loadChecks(1)
      const oldRead = store.loadChecks(1)
      expect(store.checks.value[1]?.loading).toBe(true)
      await store[refresh]()
      expect(store.checks.value[1]).toMatchObject({
        items: [fact(1)],
        loaded: true,
        stale: true,
        loading: false,
      })
      const freshRead = store.loadChecks(1)
      old.resolve({ items: [fact(2)] })
      expect(await oldRead).toBe(false)
      expect(store.checks.value[1]).toMatchObject({
        items: [fact(1)],
        stale: true,
        loading: true,
      })
      fresh.resolve({ items: [fact(3)] })
      expect(await freshRead).toBe(true)
      expect(store.checks.value[1]).toMatchObject({
        items: [fact(3)],
        stale: false,
        loading: false,
      })
    },
  )
})
