import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { setApiBaseUrl, setLocalMode, type ApiSchemas } from '@/api/client-core'
import { changeSessionContext } from '@/api/session-context'
import { createStorageSpace, setStoragePolicy, setStorageSpaceQuota } from '@/api/storage'
import {
  storageRequestError,
  storageResultUnknown,
  storageTaskErrorMessage,
} from '@/api/storage-errors'
import { resolveQuota, quotaDraft } from '@/utils/storage-quota'
import { hasSpaceManagementActions, hasStorageSpaceQuota } from '@/utils/storage-availability'

vi.mock('@/i18n', () => ({ t: (key: string) => key }))
const requests: Request[] = []
beforeEach(() => {
  requests.length = 0
  vi.stubGlobal(
    'fetch',
    vi.fn(async (request: Request) => {
      requests.push(request)
      return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } })
    }),
  )
  setApiBaseUrl('https://storage.example/api/v1')
  setLocalMode(true)
  changeSessionContext('https://storage.example/api/v1', 1, true)
})
afterEach(() => vi.unstubAllGlobals())

describe('QF nullable quota boundaries', () => {
  it.each([null, 1, 512, Number.MAX_SAFE_INTEGER])(
    'sends an explicit quota %s with exact field sets',
    async (value) => {
      await createStorageSpace(7, { name: 'Space', capacity_bytes: value })
      const policy = {
        mode: 'site_only',
        default_choice: 'site',
        generation: 4,
        logical_limit_bytes: value,
        default_space_capacity_bytes: null,
      } as const
      await setStoragePolicy({ ...policy, secret: 'must not cross boundary' } as typeof policy)
      await setStorageSpaceQuota(9, { capacity_bytes: value, expected_generation: 6 })
      expect(await requests[0]!.json()).toEqual({ name: 'Space', capacity_bytes: value })
      expect(await requests[1]!.json()).toEqual(policy)
      expect(await requests[2]!.json()).toEqual({ capacity_bytes: value, expected_generation: 6 })
      expect(new URL(requests[2]!.url).pathname).toBe('/api/v1/storage/spaces/9/quota')
    },
  )
  it.each([undefined, 0, -1, 0.5, NaN, Infinity, Number.MAX_SAFE_INTEGER + 1])(
    'rejects invalid or missing quota %s before transport',
    (value) => {
      expect(() =>
        createStorageSpace(7, {
          name: 'Space',
          capacity_bytes: value,
        } as ApiSchemas['StorageSpaceRequest']),
      ).toThrow()
      expect(() =>
        setStorageSpaceQuota(9, {
          capacity_bytes: value,
          expected_generation: 0,
        } as ApiSchemas['StorageSpaceQuotaRequest']),
      ).toThrow()
      expect(() =>
        setStoragePolicy({
          mode: 'site_only',
          default_choice: 'site',
          generation: 0,
          logical_limit_bytes: null,
          default_space_capacity_bytes: value,
        } as ApiSchemas['StoragePolicyRequest']),
      ).toThrow()
      expect(requests).toHaveLength(0)
    },
  )
  it('does not confuse invalid finite input with an explicit unlimited selection', () => {
    expect(resolveQuota({ mode: 'unlimited' })).toEqual({ ok: true, value: null })
    expect(resolveQuota({ mode: 'unselected' })).toEqual({ ok: false, reason: 'required' })
    for (const input of ['', '0', '-1', '1e3', 'NaN', 'Infinity', '0.5'])
      expect(resolveQuota({ mode: 'limited', input, unit: 'B' })).toEqual({
        ok: false,
        reason: 'invalid',
      })
    expect(resolveQuota({ mode: 'limited', input: '0.5', unit: 'KiB' })).toEqual({
      ok: true,
      value: 512,
    })
    expect(resolveQuota(quotaDraft(Number.MAX_SAFE_INTEGER))).toEqual({
      ok: true,
      value: Number.MAX_SAFE_INTEGER,
    })
    expect(quotaDraft(undefined)).toEqual({ mode: 'unselected' })
  })
  it('validates each action independently and requires the returned quota balance', () => {
    const action = { allowed: true, reason_codes: [] }
    const space = {
      capacity_bytes: 100,
      available_bytes: 0,
      reserved_bytes: 0,
      candidate_bytes: 0,
      live_bytes: 128,
      pending_delete_bytes: 0,
      management_actions: { set_status: action },
    }
    expect(hasSpaceManagementActions(space, 'set_status')).toBe(true)
    expect(hasSpaceManagementActions(space, 'set_quota')).toBe(false)
    expect(hasStorageSpaceQuota(space)).toBe(true)
    expect(hasStorageSpaceQuota({ ...space, available_bytes: undefined })).toBe(false)
    expect(hasStorageSpaceQuota({ ...space, capacity_bytes: null, available_bytes: null })).toBe(
      true,
    )
    expect(hasStorageSpaceQuota({ ...space, available_bytes: null })).toBe(false)
  })
  it.each([
    ['storage_disk_insufficient', 507, 'diskInsufficient'],
    ['storage_disk_probe_failed', 503, 'diskProbeFailed'],
  ] as const)('retains uncertainty and identity for %s', (code, status, key) => {
    const error = storageRequestError(
      { status },
      { error_code: code, task_id: 12, operation_id: 'op-12', detail: 'private path' },
    )
    expect(error.message).toBe(`storageErrors.${key}`)
    expect(storageTaskErrorMessage(code)).toBe(error.message)
    expect(storageResultUnknown(error)).toBe(true)
    expect(error).toMatchObject({ task_id: 12, operation_id: 'op-12' })
    expect(JSON.stringify(error)).not.toContain('private path')
  })
})
