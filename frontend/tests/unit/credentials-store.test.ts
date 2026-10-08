import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'
import { effectScope } from 'vue'
import * as api from '@/api/credentials'
import { createCredentialState } from '@/stores/credentials'
import { changeSessionContext } from '@/api/session-context'
import { ApiError } from '@/api/utils'
import { invalidateOrganization, organizationRoles } from '@/utils/organization-scope'
vi.mock('@/i18n', () => ({ t: (key: string) => key }))
const credential = (id = 1, scope: 'user' | 'org' = 'user', owner = 1): api.Credential => ({
  id,
  scope,
  owner_id: owner,
  provider: 'openai',
  endpoint: 'https://api.test/v1',
  current_version: 1,
})
const versions = [{ version: 1, revoked: false, created_at: '2026-10-01T00:00:00Z' }]
const scopes: ReturnType<typeof effectScope>[] = []
function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (cause: unknown) => void
  const promise = new Promise<T>((yes, no) => {
    resolve = yes
    reject = no
  })
  return { promise, resolve, reject }
}
function setup() {
  const transport = {
    ...api,
    fetchCredentials: vi.fn(async () => ({ items: [credential()] })),
    fetchCredentialVersions: vi.fn(async () => ({ items: versions })),
    createCredential: vi.fn(async () => credential()),
    rotateCredential: vi.fn(async () => ({ id: 1, version: 2 })),
    revokeCredentialVersion: vi.fn(async () => {}),
    collectCredentialVersions: vi.fn(async () => ({ deleted_versions: 0 })),
  }
  const scope = effectScope()
  scopes.push(scope)
  return { state: scope.run(() => createCredentialState(transport))!, transport }
}
beforeEach(() => {
  changeSessionContext('https://service.test/api/v1', 1, true)
  organizationRoles.value = { 7: 'owner', 8: 'admin', 9: 'member' }
})
afterEach(() => {
  for (const scope of scopes.splice(0)) scope.stop()
})
describe('credential state isolation and permissions', () => {
  it('never fetches unknown or member organization credentials', async () => {
    const { state, transport } = setup()
    await state.load(9)
    await state.load(11)
    expect(transport.fetchCredentials).not.toHaveBeenCalled()
    await state.load(7)
    await state.load(8)
    expect(transport.fetchCredentials).toHaveBeenCalledTimes(2)
  })
  it('filters foreign ownership and strips unexpected response fields', async () => {
    const { state, transport } = setup()
    transport.fetchCredentials.mockResolvedValue({
      items: [
        { ...credential(), secret: 'not-in-state' } as api.Credential,
        credential(2, 'org', 7),
        credential(3, 'user', 8),
      ],
    })
    await state.load()
    expect(state.items.value).toEqual([credential()])
    expect(JSON.stringify(state.items.value)).not.toContain('not-in-state')
  })
  it('keeps stale data on refresh failure but clears it when access is denied', async () => {
    const { state, transport } = setup()
    await state.load()
    transport.fetchCredentials.mockRejectedValueOnce(new ApiError('private error', 503))
    expect(await state.load()).toBe(false)
    expect(state.items.value).toHaveLength(1)
    expect(state.stale.value).toBe(true)
    expect(state.ready.value).toBe(false)
    expect(state.error.value).not.toContain('private')
    transport.fetchCredentials.mockRejectedValueOnce(new ApiError('denied', 403))
    await state.load()
    expect(state.items.value).toEqual([])
    expect(state.versions.value).toEqual({})
  })
  it('rejects late reads and finally after an A to B to A scope change', async () => {
    const { state, transport } = setup(),
      first = deferred<{ items: api.Credential[] }>(),
      last = deferred<{ items: api.Credential[] }>()
    transport.fetchCredentials.mockReturnValueOnce(first.promise).mockReturnValueOnce(last.promise)
    const old = state.load(7)
    const signal = transport.fetchCredentials.mock.calls[0]?.[1] as AbortSignal | undefined
    state.setOrganization(8)
    const fresh = state.load(7)
    first.resolve({ items: [credential(71, 'org', 7)] })
    await old
    expect(signal?.aborted).toBe(true)
    expect(state.loading.value).toBe(true)
    expect(state.items.value).toEqual([])
    last.resolve({ items: [credential(72, 'org', 7)] })
    await fresh
    expect(state.items.value.map((item) => item.id)).toEqual([72])
    expect(state.loading.value).toBe(false)
  })
  it('clears versions and ignores pending writes on service change or role loss', async () => {
    const { state, transport } = setup()
    await state.load()
    await state.loadVersions(1)
    const pending = deferred<{ id: number; version: number }>()
    transport.rotateCredential.mockReturnValueOnce(pending.promise)
    const write = state.rotate(1, { secret: 'ephemeral' })
    changeSessionContext('https://other.test/api/v1', 2)
    pending.resolve({ id: 1, version: 2 })
    expect(await write).toEqual({ status: 'stale' })
    expect(state.items.value).toEqual([])
    expect(state.versions.value).toEqual({})
    transport.fetchCredentials.mockResolvedValue({ items: [credential(7, 'org', 7)] })
    await state.load(7)
    organizationRoles.value = { 7: 'member' }
    invalidateOrganization(7)
    expect(state.items.value).toEqual([])
    expect(state.canManage.value).toBe(false)
  })
})
describe('credential write outcomes', () => {
  it('serializes each credential and separates committed writes from read failures', async () => {
    const { state, transport } = setup()
    await state.load()
    await state.loadVersions(1)
    const pending = deferred<{ id: number; version: number }>()
    transport.rotateCredential.mockReturnValueOnce(pending.promise)
    const rotation = state.rotate(1, { secret: 'ephemeral' })
    expect(await state.collect(1)).toEqual({ status: 'error' })
    expect(transport.collectCredentialVersions).not.toHaveBeenCalled()
    pending.resolve({ id: 1, version: 2 })
    expect(await rotation).toEqual({ status: 'success', value: { id: 1, version: 2 } })
    expect(state.versions.value[1]?.stale).toBe(true)
    transport.fetchCredentials.mockRejectedValueOnce(new ApiError('offline', 503))
    expect(await state.load()).toBe(false)
    expect(state.writes.value[1]?.error).toBeNull()
    expect(transport.rotateCredential).toHaveBeenCalledTimes(1)
    expect(await state.collect(1)).toEqual({ status: 'error' })
  })
  it('keeps unknown status across refresh and requires explicit new-write intent', async () => {
    const { state, transport } = setup()
    await state.load()
    transport.createCredential.mockRejectedValueOnce(new TypeError('secret should not appear'))
    expect(await state.create({ provider: 'openai', secret: 'private-value' })).toEqual({
      status: 'unknown',
    })
    expect(state.creation.value.error).toBe('configurationCredentials.unknown')
    await state.load()
    expect(state.creation.value.unknown).toBe(true)
    expect(await state.create({ provider: 'openai', secret: 'private-value' })).toEqual({
      status: 'error',
    })
    expect(transport.createCredential).toHaveBeenCalledTimes(1)
    expect((await state.create({ provider: 'openai', secret: 'private-value' }, true)).status).toBe(
      'success',
    )
    expect(
      JSON.stringify({
        items: state.items.value,
        writes: state.writes.value,
        creation: state.creation.value,
      }),
    ).not.toContain('private-value')
  })
  it('treats zero collected versions as a successful result', async () => {
    const { state } = setup()
    await state.load()
    await state.loadVersions(1)
    expect(await state.collect(1)).toEqual({ status: 'success', value: { deleted_versions: 0 } })
  })
})
