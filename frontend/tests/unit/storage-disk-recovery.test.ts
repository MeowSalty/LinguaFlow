import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ApiSchemas } from '@/api/client-core'
import * as api from '@/api/storage'
import { changeSessionContext } from '@/api/session-context'
import { storageRequestError } from '@/api/storage-errors'
import { createSourceUpdateSession } from '@/composables/useSourceUpdateSession'
import { createRepairSession } from '@/components/storage/repairSession'
import { createMigrationSession } from '@/components/storage/migrationSession'
import { createExportSession } from '@/components/storage/exportSession'
import { invalidateStorageSnapshots } from '@/utils/storage-snapshots'

vi.mock('@/i18n', () => ({ t: (key: string) => key }))
const failures = [
  { status: 507, code: 'storage_disk_insufficient' },
  { status: 503, code: 'storage_disk_probe_failed' },
] as const
const project = {
  id: 1,
  owner_user_id: 1,
  owner_org_id: null,
  storage_generation: 0,
  storage_state: 'active',
} as ApiSchemas['Project']
const target: ApiSchemas['StorageOption'] = {
  space_id: 9,
  name: 'original target',
  scope: 'user',
  selectable: true,
  reason_codes: [],
}
const preview: ApiSchemas['SourceUpdatePreview'] = {
  task_id: 42,
  source_generation: 0,
  translation_generation: 9,
  stats: { added: 1, updated: 0, unchanged: 0, deleted: 0 },
  expires_at: '2030-01-01T00:00:00Z',
}
const task = (
  kind: ApiSchemas['StorageTask']['kind'],
  overrides: Partial<ApiSchemas['StorageTask']> = {},
): ApiSchemas['StorageTask'] => ({
  id: 42,
  operation_id: 'original-operation',
  project_id: 1,
  resource_id: 2,
  source_revision_id: 3,
  kind,
  status: 'running',
  phase: kind === 'source_update' ? 'prepared' : 'accepted',
  cleanup_status: 'pending',
  allowed_actions: [],
  created_at: '2026-10-07T00:00:00Z',
  updated_at: '2026-10-07T00:00:00Z',
  expires_at: preview.expires_at,
  target_space_id: 9,
  expected_storage_generation: 0,
  expected_location_generation: 0,
  expected_source_generation: 0,
  expected_translation_generation: 9,
  input_size: 3,
  ...(kind === 'source_update' ? { source_preview: preview } : {}),
  ...overrides,
})
const file = () => new File(['abc'], 'candidate.json')
const disposals: Array<() => void> = []
const focusRefresh = async () => {
  invalidateStorageSnapshots({ projectId: 1 })
  await vi.advanceTimersByTimeAsync(30)
}
const diskError = (failure: (typeof failures)[number], taskId?: number) =>
  storageRequestError(
    { status: failure.status },
    {
      error_code: failure.code,
      ...(taskId ? { task_id: taskId, operation_id: 'original-operation' } : {}),
    },
  )
beforeEach(() => {
  changeSessionContext('/api/v1', 1, true)
  vi.useFakeTimers()
})
afterEach(() => {
  for (const dispose of disposals.splice(0)) dispose()
  vi.clearAllTimers()
  vi.useRealTimers()
})

function source() {
  const deps = {
    available: vi.fn(() => true),
    beforeSavedContent: vi.fn(async () => true),
    preview: vi.fn(async () => preview),
    commit: vi.fn(async () => task('source_update')),
    task: vi.fn(async () => task('source_update', { allowed_actions: ['commit'] })),
  }
  const session = createSourceUpdateSession(deps)
  disposals.push(session.reset)
  session.select(1, 2, file())
  return { session, deps }
}

describe.each(failures)('disk recovery $status / $code', (failure) => {
  it('keeps a known source task and key after commit fails; refreshed allowed_actions control confirmation', async () => {
    const { session, deps } = source()
    await session.prepare()
    await session.refresh()
    const key = session.key.value,
      original = session.task.value,
      candidate = session.file.value
    deps.commit.mockRejectedValueOnce(diskError(failure, 42))
    expect(await session.confirm()).toBe(false)
    expect(session.state.value).toBe('unknown')
    expect(session.task.value).toBe(original)
    expect(original?.status).toBe('running')
    expect(session.operationId.value).toBe('original-operation')
    expect(session.key.value).toBe(key)
    expect(session.file.value).toBe(candidate)
    expect(await session.confirm()).toBe(false)
    expect(session.newPreview()).toBe(false)
    deps.task.mockResolvedValue(
      task('source_update', {
        status: 'needs_action',
        error_code: failure.code,
        allowed_actions: ['cancel'],
      }),
    )
    await session.recover()
    expect(session.task.value?.status).toBe('needs_action')
    expect(session.state.value).toBe('read_only')
    expect(await session.confirm()).toBe(false)
    deps.task.mockResolvedValue(task('source_update', { allowed_actions: ['commit'] }))
    await session.refresh()
    expect(session.state.value).toBe('preview_ready')
    expect(session.taskId.value).toBe(42)
    expect(session.key.value).toBe(key)
    expect(deps.preview).toHaveBeenCalledTimes(1)
    expect(deps.commit).toHaveBeenCalledTimes(1)
    expect(deps.task).toHaveBeenLastCalledWith(1, 42, expect.anything())
  })

  it('keeps an unknown source preview input until explicit recovery reuses its original key', async () => {
    const { session, deps } = source()
    const key = session.key.value,
      candidate = session.file.value
    deps.preview.mockRejectedValueOnce(diskError(failure))
    await session.prepare()
    expect(session.state.value).toBe('unknown')
    expect(session.taskId.value).toBeNull()
    expect(session.newPreview()).toBe(false)
    expect(await session.prepare()).toBe(false)
    expect(await session.refresh()).toBe(false)
    await vi.advanceTimersByTimeAsync(1000)
    expect(deps.preview).toHaveBeenCalledTimes(1)
    expect(deps.task).not.toHaveBeenCalled()
    await session.recover()
    expect(deps.preview).toHaveBeenCalledTimes(2)
    expect(deps.preview).toHaveBeenLastCalledWith(1, 2, candidate, key, expect.anything())
    expect(deps.task).toHaveBeenLastCalledWith(1, 42, expect.anything())
    expect(session.operationId.value).toBe('original-operation')
    expect(session.key.value).toBe(key)
    expect(deps.commit).not.toHaveBeenCalled()
  })

  it('keeps repair bytes and identity, and background refresh only reads the original task', async () => {
    const original = task('repair', { allowed_actions: ['upload_content'] })
    const deps = {
      createStorageIntent: vi.fn<typeof api.createStorageIntent>(async () => original),
      getStorageTask: vi.fn<typeof api.getStorageTask>(async () => original),
      receiveStorageContent: vi.fn<typeof api.receiveStorageContent>(async () => {
        throw diskError(failure, 42)
      }),
    }
    const session = createRepairSession(
      {
        context: () => ({
          project,
          resourceId: 2,
          version: {
            id: 3,
            current: false,
            format: 'json',
            parser_version: '1',
            verification_state: 'verified',
            location_generation: 0,
            health: 'missing',
          },
        }),
        available: () => true,
      },
      deps,
    )
    disposals.push(session.dispose)
    const candidate = file()
    session.selectFile(candidate)
    await session.start(target, 0)
    const request = deps.createStorageIntent.mock.calls[0]![1]
    expect(await session.upload()).toBe(false)
    expect(session.unknown.value).toBe(true)
    expect(session.task.value).toBe(original)
    expect(original.status).toBe('running')
    deps.getStorageTask.mockResolvedValue(
      task('repair', {
        status: 'needs_action',
        error_code: failure.code,
        allowed_actions: ['cancel'],
      }),
    )
    await focusRefresh()
    expect(session.task.value).toMatchObject({
      id: 42,
      operation_id: 'original-operation',
      status: 'needs_action',
      target_space_id: 9,
    })
    expect(session.file.value).toBe(candidate)
    expect(session.canUpload.value).toBe(false)
    expect(await session.upload()).toBe(false)
    expect(await session.start({ ...target, space_id: 10 }, 0)).toBe(false)
    expect(deps.createStorageIntent).toHaveBeenCalledTimes(1)
    expect(deps.createStorageIntent.mock.calls[0]![1]).toBe(request)
    expect(deps.receiveStorageContent).toHaveBeenCalledTimes(1)
    expect(deps.getStorageTask).toHaveBeenLastCalledWith(1, 42, expect.anything())
  })

  it.each([true, false])(
    'preserves migration target/key with task identity supplied=%s',
    async (known) => {
      const latest = task('migration', {
        status: 'needs_action',
        error_code: failure.code,
        allowed_actions: ['cancel'],
      })
      const deps = {
        migrateProjectStorage: vi
          .fn<typeof api.migrateProjectStorage>()
          .mockRejectedValueOnce(diskError(failure, known ? 42 : undefined))
          .mockResolvedValue(latest),
        getStorageTask: vi.fn(async () => latest),
      }
      const session = createMigrationSession(
        { project: () => project, available: () => true },
        deps,
      )
      disposals.push(session.dispose)
      expect(await session.start(target, 0)).toBe(false)
      const request = deps.migrateProjectStorage.mock.calls[0]![1]
      expect(session.unknown.value).toBe(true)
      expect(session.startNew()).toBe(false)
      expect(await session.start({ ...target, space_id: 10 }, 0)).toBe(false)
      await focusRefresh()
      expect(deps.migrateProjectStorage).toHaveBeenCalledTimes(1)
      if (!known) {
        expect(deps.getStorageTask).not.toHaveBeenCalled()
        expect(session.task.value).toBeNull()
        await session.recover()
        expect(deps.migrateProjectStorage.mock.calls[1]![1]).toBe(request)
      }
      await session.recover()
      expect(deps.getStorageTask).toHaveBeenLastCalledWith(1, 42, expect.anything())
      expect(session.task.value).toMatchObject({
        id: 42,
        operation_id: 'original-operation',
        status: 'needs_action',
        target_space_id: 9,
      })
      expect(session.canStartNew.value).toBe(false)
      expect(latest.status).toBe('needs_action')
      expect(deps.migrateProjectStorage).toHaveBeenCalledTimes(known ? 1 : 2)
    },
  )

  it.each([true, false])(
    'preserves export key with task identity supplied=%s; refresh never creates another export',
    async (known) => {
      const latest = task('export', {
        status: 'needs_action',
        error_code: failure.code,
        allowed_actions: ['cancel'],
      })
      const create = vi
        .fn<typeof api.createExportArtifact>()
        .mockRejectedValueOnce(diskError(failure, known ? 42 : undefined))
        .mockResolvedValue(latest)
      const get = vi.fn(async () => latest)
      const session = createExportSession(
        { projectId: 1, resourceId: 2, subscribe: vi.fn(() => vi.fn()) },
        {
          ...api,
          createExportArtifact: create,
          getStorageTask: get,
          listExportArtifacts: vi.fn(async () => ({ items: [] })),
        },
        () => true,
      )
      disposals.push(session.dispose)
      expect(await session.start()).toBe(false)
      const key = session.key.value
      expect(session.unknown.value).toBe(true)
      expect(await session.start()).toBe(false)
      await focusRefresh()
      expect(create).toHaveBeenCalledTimes(1)
      if (!known) {
        expect(get).not.toHaveBeenCalled()
        expect(session.task.value).toBeNull()
        await session.recover()
        expect(create.mock.calls[1]![2]).toBe(key)
      }
      await session.recover()
      expect(get).toHaveBeenLastCalledWith(1, 42, expect.anything())
      expect(session.key.value).toBe(key)
      expect(session.task.value).toBe(latest)
      expect(latest.status).toBe('needs_action')
      expect(session.published.value).toBe(false)
      expect(await session.start()).toBe(false)
      expect(create).toHaveBeenCalledTimes(known ? 1 : 2)
    },
  )

  it('keeps an expired source candidate after disk recovery and mints a key only for an explicit new preview', async () => {
    const { session, deps } = source()
    const key = session.key.value,
      candidate = session.file.value
    await session.prepare()
    deps.commit.mockRejectedValueOnce(diskError(failure, 42))
    await session.confirm()
    const expired = task('source_update', {
      status: 'failed',
      error_code: 'storage_intent_expired',
      allowed_actions: [],
      expires_at: '2026-10-01T00:00:00Z',
    })
    deps.task.mockResolvedValue(expired)
    await session.recover()
    expect(session.state.value).toBe('expired')
    expect(session.expiresAt.value).toBe(expired.expires_at)
    expect(session.key.value).toBe(key)
    expect(session.file.value).toBe(candidate)
    expect(await session.confirm()).toBe(false)
    expect(deps.commit).toHaveBeenCalledTimes(1)
    expect(deps.preview).toHaveBeenCalledTimes(1)
    expect(session.newPreview()).toBe(true)
    expect(session.key.value).not.toBe(key)
    expect(deps.preview).toHaveBeenCalledTimes(1)
  })
})
