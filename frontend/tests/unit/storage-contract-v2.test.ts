import { beforeEach, afterEach, describe, it, expect, vi } from 'vitest'
import { setApiBaseUrl, setLocalMode } from '@/api/client-core'
import { changeSessionContext } from '@/api/session-context'
import { organizationRoles } from '@/utils/organization-scope'
import {
  storageActionAllowed,
  storageProjectWritable,
  storageTaskCommitted,
  type StorageProjectAction,
} from '@/utils/storage-contract'
import {
  createStorageIntent,
  revokeStorageAuthorization,
  setStoragePolicy,
  getStorageOptions,
  getProjectStorageOptions,
  downloadLegacySourceSnapshot,
  downloadExportArtifact,
  getStorageTask,
  listExportArtifacts,
} from '@/api/storage'
import { StorageApiError, storageResultUnknown } from '@/api/storage-errors'
import { sanitizeUploadBatch } from '@/api/projects'
import type { ApiSchemas } from '@/api/client-core'
vi.mock('@/i18n', () => ({ t: (key: string) => key }))
const requests: Request[] = []
const fetchMock = vi.fn<typeof fetch>()
const json = (value: unknown, status = 200) =>
  new Response(JSON.stringify(value), { status, headers: { 'Content-Type': 'application/json' } })
beforeEach(() => {
  requests.length = 0
  fetchMock.mockReset().mockImplementation(async (input) => {
    requests.push(input as Request)
    return json({ items: [] })
  })
  vi.stubGlobal('fetch', fetchMock)
  setApiBaseUrl('https://contract.example/api/v1')
  changeSessionContext('https://contract.example/api/v1', 1, true)
  setLocalMode(false)
})
afterEach(() => {
  vi.unstubAllGlobals()
  organizationRoles.value = {}
})

describe('C01-C09 synchronized transport contract', () => {
  it.each([
    'upload',
    'delete',
    'bind',
    'sourceUpdate',
    'repair',
    'migration',
    'exportMutation',
  ] as StorageProjectAction[])(
    'applies real gates, role and maintenance checks to %s',
    (action) => {
      const project = {
        id: 1,
        owner_user_id: 1,
        storage_generation: 0,
        storage_state: 'active',
      } as ApiSchemas['Project']
      expect(storageActionAllowed(project, action)).toBe(true)
      expect(storageActionAllowed({ ...project, owner_user_id: 2 }, action)).toBe(false)
      expect(storageActionAllowed({ ...project, storage_generation: NaN }, action)).toBe(false)
      expect(storageActionAllowed({ ...project, storage_state: 'migrating' }, action)).toBe(
        action === 'repair',
      )
      expect(storageActionAllowed({ ...project, storage_state: 'legacy_migration' }, action)).toBe(
        false,
      )
      organizationRoles.value = { 7: 'member' }
      expect(storageActionAllowed({ ...project, owner_org_id: 7 }, action)).toBe(false)
      organizationRoles.value = { 7: 'admin' }
      expect(storageActionAllowed({ ...project, owner_org_id: 7 }, action)).toBe(true)
    },
  )
  it('constructs each intent from only its own fields and keeps generation zero', async () => {
    const common = { idempotency_key: 'original', size: 0, storage_generation: 0 }
    await createStorageIntent(1, {
      ...common,
      kind: 'upload',
      path: 'a.txt',
      resource_id: 99,
    } as ApiSchemas['StorageIntent'])
    await createStorageIntent(1, {
      ...common,
      kind: 'source_update',
      resource_id: 2,
      source_generation: 0,
      translation_generation: 4,
      path: 'forbidden',
    } as ApiSchemas['StorageIntent'])
    await createStorageIntent(1, {
      ...common,
      kind: 'repair',
      resource_id: 2,
      source_revision_id: 3,
      location_generation: 1,
      target_space_id: 8,
      source_generation: 99,
    } as ApiSchemas['StorageIntent'])
    expect(await requests[0]!.json()).toEqual({ ...common, kind: 'upload', path: 'a.txt' })
    expect(await requests[1]!.json()).toEqual({
      ...common,
      kind: 'source_update',
      resource_id: 2,
      source_generation: 0,
      translation_generation: 4,
    })
    expect(await requests[2]!.json()).toEqual({
      ...common,
      kind: 'repair',
      resource_id: 2,
      source_revision_id: 3,
      location_generation: 1,
      target_space_id: 8,
    })
    expect(() =>
      createStorageIntent(1, {
        ...common,
        kind: 'repair',
        resource_id: 2,
      } as ApiSchemas['StorageIntent']),
    ).toThrow()
    expect(requests).toHaveLength(3)
  })
  it('does not forward response-only policy fields or a status when revoking', async () => {
    await setStoragePolicy({
      mode: 'both',
      default_choice: 'site',
      generation: 0,
      logical_limit_bytes: 100,
      default_space_capacity_bytes: null,
      configuration_needs_update: true,
    })
    await revokeStorageAuthorization(4, {
      expected_generation: 0,
      status: 'disabled',
    } as ApiSchemas['StorageRevokeRequest'])
    expect(await requests[0]!.json()).toEqual({
      mode: 'both',
      default_choice: 'site',
      generation: 0,
      logical_limit_bytes: 100,
      default_space_capacity_bytes: null,
    })
    expect(await requests[1]!.json()).toEqual({ expected_generation: 0 })
  })
  it('uses discovery scopes and purpose-specific queries without admin metadata endpoints', async () => {
    await getStorageOptions({ kind: 'user' })
    await getStorageOptions({ kind: 'org', id: 7 })
    await getProjectStorageOptions(1, { purpose: 'repair', source_revision_id: 3 })
    await listExportArtifacts(1, 2, { includeDeleted: true })
    expect(new URL(requests[0]!.url).search).toBe('?scope=user')
    expect(new URL(requests[1]!.url).searchParams.get('organization_id')).toBe('7')
    expect(new URL(requests[2]!.url).searchParams.get('source_revision_id')).toBe('3')
    expect(new URL(requests[3]!.url).searchParams.get('include_deleted')).toBe('true')
    expect(() => getStorageOptions({ kind: 'org', id: 0 })).toThrow()
  })
  it.each([
    'source_revision_conflict',
    'storage_generation_conflict',
    'storage_idempotency_conflict',
    'storage_operation_in_progress',
  ])('preserves safe identity for %s without exposing provider details', async (code) => {
    fetchMock.mockResolvedValueOnce(
      json(
        {
          error_code: code,
          task_id: 9,
          operation_id: 'op-original',
          check_id: 2,
          detail: 'private/object?secret',
          endpoint: 'private',
        },
        409,
      ),
    )
    const error = await getStorageTask(1, 9).catch((value: unknown) => value)
    expect(error).toBeInstanceOf(StorageApiError)
    expect(error).toMatchObject({
      error_code: code,
      task_id: 9,
      operation_id: 'op-original',
      check_id: 2,
      status: 409,
    })
    expect(JSON.stringify(error)).not.toContain('private')
    expect(storageResultUnknown(error)).toBe(code === 'storage_operation_in_progress')
  })
  it('accepts JSON attachments exclusively for the legacy snapshot endpoint', async () => {
    const response = () =>
      new Response('{"segments":[]}', {
        headers: {
          'Content-Type': 'application/json',
          'Content-Disposition': 'attachment; filename="legacy.json"',
        },
      })
    fetchMock.mockResolvedValueOnce(response())
    expect(await (await downloadLegacySourceSnapshot(1, 9)).blob.text()).toBe('{"segments":[]}')
    fetchMock.mockResolvedValueOnce(response())
    await expect(downloadExportArtifact(1, 9)).rejects.toThrow('storageErrors.invalidDownload')
    fetchMock.mockResolvedValueOnce(
      json({ error_code: 'storage_intent_expired', task_id: 9, detail: 'private' }, 409),
    )
    await expect(downloadLegacySourceSnapshot(1, 9)).rejects.toMatchObject({
      error_code: 'storage_intent_expired',
      task_id: 9,
    })
  })
  it('preserves batch operation identity and domain failures but never provider text', () => {
    const batch = sanitizeUploadBatch(
      {
        operation_id: 'original-batch',
        items: [
          {
            path: 'a.txt',
            action: 'failed',
            error_code: 'storage_quota_exceeded',
            error: 'private endpoint',
            secret: 'credential',
          },
        ],
      },
      1,
    )
    expect(batch.operation_id).toBe('original-batch')
    expect(batch.items[0]).toMatchObject({
      error_code: 'storage_quota_exceeded',
      error: 'storageErrors.quotaExceeded',
    })
    expect(JSON.stringify(batch)).not.toMatch(/private|credential/)
    expect(sanitizeUploadBatch({ items: [] }, 0)).not.toHaveProperty('operation_id')
  })
  it('uses project ownership and organization roles, never platform admin bypass', () => {
    const project = { id: 1, owner_user_id: 1 } as ApiSchemas['Project']
    expect(storageProjectWritable(project)).toBe(true)
    expect(storageProjectWritable({ ...project, owner_user_id: 2 })).toBe(false)
    organizationRoles.value = { 7: 'member' }
    expect(storageProjectWritable({ ...project, owner_org_id: 7 })).toBe(false)
    organizationRoles.value = { 7: 'admin' }
    expect(storageProjectWritable({ ...project, owner_org_id: 7 })).toBe(true)
    changeSessionContext('https://contract.example/api/v1', 2, true)
    expect(storageProjectWritable({ ...project, owner_org_id: 7 })).toBe(false)
  })
  it('does not treat prepared or progress completion as committed', () => {
    expect(
      storageTaskCommitted({ status: 'completed', phase: 'prepared' } as ApiSchemas['StorageTask']),
    ).toBe(false)
    expect(
      storageTaskCommitted({ status: 'running', phase: 'committed' } as ApiSchemas['StorageTask']),
    ).toBe(false)
    expect(
      storageTaskCommitted({
        status: 'completed',
        phase: 'committed',
      } as ApiSchemas['StorageTask']),
    ).toBe(true)
  })
})
