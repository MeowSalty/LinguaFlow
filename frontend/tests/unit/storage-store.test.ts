import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { effectScope, isRef, watch, type EffectScope } from 'vue'
import * as api from '@/api/storage'
import type { ApiSchemas } from '@/api/client-core'
import { changeSessionContext } from '@/api/session-context'
import { ApiError } from '@/api/utils'
import { storageRequestError } from '@/api/storage-errors'
import { createStorageState } from '@/stores/storage'
import { invalidateOrganization, organizationRoles } from '@/utils/organization-scope'

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
      { ...api, listStorageChecks: vi.fn().mockResolvedValue({ items: [] }), ...overrides },
      () => admin,
    ),
  )!
beforeEach(() => {
  changeSessionContext('/api/v1', 1, true)
  scope = effectScope()
})
afterEach(() => scope.stop())

describe('storage scope and concurrency', () => {
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

  it('defers connection and space snapshots until the management write finishes', async () => {
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
    expect(await store.load()).toBe(false)
    expect(await store.loadSpaces(1)).toBe(false)
    expect(list).toHaveBeenCalledTimes(1)
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
      mode: 'site_only',
      default_choice: 'site',
      generation: 2,
      logical_limit_bytes: 100,
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
      mode: 'site_only',
      default_choice: 'site',
      generation: 2,
      logical_limit_bytes: 100,
    })
    const store = state({ getStoragePolicy: policy }, true)
    expect(await store.loadPolicy()).toBe(false)
    expect(policy).not.toHaveBeenCalled()
    store.setScope({ kind: 'site' })
    expect(await store.loadPolicy()).toBe(true)
    expect(policy).toHaveBeenCalledTimes(1)
  })
})
