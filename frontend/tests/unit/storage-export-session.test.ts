import { beforeEach, describe, expect, it, vi } from 'vitest'
import * as api from '@/api/storage'
import type { ApiSchemas } from '@/api/client-core'
import { changeSessionContext } from '@/api/session-context'
import { createExportSession } from '@/components/storage/exportSession'
import { StorageApiError } from '@/api/storage-errors'

const task = (
  status: ApiSchemas['StorageTask']['status'] = 'running',
): ApiSchemas['StorageTask'] => ({
  id: 9,
  operation_id: 'op9',
  kind: 'export',
  status,
  phase: status === 'completed' ? 'committed' : 'render',
  cleanup_status: 'done',
  allowed_actions: [],
  result_artifact_id: 5,
  project_id: 1,
  resource_id: 2,
  created_at: '2026-10-04T00:00:00Z',
  updated_at: '2026-10-04T00:00:00Z',
  expires_at: null,
})
const artifact = (id = 5): ApiSchemas['ExportArtifact'] => ({
  id,
  source_revision_id: 2,
  status: 'ready',
  rebuildable: true,
  renderer_version: '1',
  filename: 'snapshot.txt',
  deletion_task_id: null,
})
beforeEach(() => changeSessionContext('/api/v1', 1, true))
const tick = () => new Promise<void>((resolve) => setTimeout(resolve, 0))

describe('fixed export session', () => {
  it('does not invent an unknown export operation when the preflight read fails before submission', async () => {
    const create = vi.fn()
    const session = createExportSession(
      {
        projectId: 1,
        resourceId: 2,
        subscribe: vi.fn(),
        beforeMutation: vi.fn().mockRejectedValue(new TypeError('read interrupted')),
      },
      { ...api, createExportArtifact: create },
      () => true,
    )
    expect(await session.start()).toBe(false)
    expect(create).not.toHaveBeenCalled()
    expect(session.unknown.value).toBe(false)
    expect(session.key.value).toBeNull()
    expect(session.error.value).toBeTruthy()
    session.dispose()
  })
  it.each(['create', 'rebuild', 'delete'] as const)(
    'blocks %s after fresh mutation validation detects changed permission or maintenance',
    async (action) => {
      const create = vi.fn(),
        rebuild = vi.fn(),
        remove = vi.fn(),
        validate = vi.fn().mockResolvedValue(false)
      const session = createExportSession(
        { projectId: 1, resourceId: 2, subscribe: vi.fn(), beforeMutation: validate },
        {
          ...api,
          listExportArtifacts: vi.fn().mockResolvedValue({ items: [artifact()] }),
          createExportArtifact: create,
          rebuildExportArtifact: rebuild,
          deleteExportArtifact: remove,
        },
        () => true,
      )
      await session.refresh()
      expect(
        await (action === 'delete'
          ? session.remove(5)
          : session.start(action === 'rebuild' ? 5 : undefined)),
      ).toBe(false)
      expect(validate).toHaveBeenCalledTimes(1)
      expect(create).not.toHaveBeenCalled()
      expect(rebuild).not.toHaveBeenCalled()
      expect(remove).not.toHaveBeenCalled()
      expect(session.deletions.value).toEqual({})
      session.dispose()
    },
  )
  it('validates fresh mutation permission before replaying an unknown key', async () => {
    const validate = vi.fn().mockResolvedValueOnce(true).mockResolvedValue(false)
    const create = vi.fn().mockRejectedValue(new TypeError('response lost'))
    const session = createExportSession(
      { projectId: 1, resourceId: 2, subscribe: vi.fn(), beforeMutation: validate },
      { ...api, createExportArtifact: create },
      () => true,
    )
    await session.start()
    const key = session.key.value
    expect(await session.recover()).toBe(false)
    expect(create).toHaveBeenCalledTimes(1)
    expect(session.key.value).toBe(key)
    expect(session.unknown.value).toBe(true)
    session.dispose()
  })
  it('reattaches a deletion subscription after a failed read when history is refreshed', async () => {
    const callbacks: Array<(value: ApiSchemas['StorageTask']) => void> = []
    const failures: Array<(error: unknown) => void> = []
    const stop = vi.fn()
    const session = createExportSession(
      {
        projectId: 1,
        resourceId: 2,
        subscribe: (_project, _id, receive, fail) => {
          callbacks.push(receive)
          failures.push(fail)
          return stop
        },
      },
      {
        ...api,
        listExportArtifacts: vi.fn().mockResolvedValue({
          items: [{ ...artifact(), status: 'deleted', deletion_task_id: 19 }],
        }),
      },
      () => true,
    )
    await session.refresh()
    failures[0]!(new TypeError('connection interrupted'))
    expect(session.deletions.value[5]?.status).toBe('unknown')
    await session.refresh()
    expect(stop).toHaveBeenCalledTimes(1)
    expect(callbacks).toHaveLength(2)
    callbacks[1]!({ ...task('completed'), id: 19, kind: 'export_delete' })
    expect(session.deletions.value[5]?.status).toBe('done')
    failures[0]!(new TypeError('late old failure'))
    expect(session.deletions.value[5]?.status).toBe('done')
    session.dispose()
  })
  it('clears private artifacts and operation keys on denied recovery', async () => {
    const session = createExportSession(
      { projectId: 1, resourceId: 2, subscribe: vi.fn() },
      {
        ...api,
        listExportArtifacts: vi.fn().mockResolvedValue({ items: [artifact()] }),
        createExportArtifact: vi
          .fn()
          .mockRejectedValueOnce(new TypeError('lost'))
          .mockRejectedValueOnce(new StorageApiError('denied', 403, {})),
      },
      () => true,
    )
    await session.refresh()
    await session.start()
    await session.recover()
    expect(session.items.value).toEqual([])
    expect(session.key.value).toBeNull()
    expect(session.canDownload(artifact())).toBe(false)
    session.dispose()
  })
  it.each(['create', 'rebuild'] as const)(
    'recovers a lost %s response by replaying only its original key and input',
    async (kind) => {
      const submit = vi
        .fn()
        .mockRejectedValueOnce(new TypeError('response lost'))
        .mockResolvedValueOnce({ ...task('completed'), result_artifact_id: 6 })
      const before = vi.fn().mockResolvedValue(true)
      const changed = vi.fn()
      const session = createExportSession(
        { projectId: 1, resourceId: 2, beforeSavedContent: before, changed, subscribe: vi.fn() },
        {
          ...api,
          createExportArtifact: submit,
          rebuildExportArtifact: submit,
          listExportArtifacts: vi.fn().mockResolvedValue({ items: [artifact(), artifact(6)] }),
        },
        () => true,
      )
      await session.refresh()
      await session.start(kind === 'rebuild' ? 5 : undefined)
      const originalKey = session.key.value
      expect(session.unknown.value).toBe(true)
      expect(await session.start()).toBe(false)
      expect(await session.recover()).toBe(true)
      expect(submit).toHaveBeenCalledTimes(2)
      expect(submit.mock.calls[1]).toEqual(submit.mock.calls[0])
      expect(session.key.value).toBe(originalKey)
      expect(before).toHaveBeenCalledTimes(kind === 'create' ? 1 : 0)
      expect(session.published.value).toBe(true)
      expect(changed).toHaveBeenCalledTimes(1)
      session.dispose()
    },
  )

  it('recovers the exact safe task ID from an in-progress Problem without repeating POST', async () => {
    const submit = vi.fn().mockRejectedValue(
      new StorageApiError('pending', 409, {
        error_code: 'storage_operation_in_progress',
        task_id: 9,
      }),
    )
    const get = vi.fn().mockResolvedValue(task('completed'))
    const session = createExportSession(
      { projectId: 1, resourceId: 2, subscribe: vi.fn() },
      {
        ...api,
        createExportArtifact: submit,
        getStorageTask: get,
        listExportArtifacts: vi.fn().mockResolvedValue({ items: [artifact()] }),
      },
      () => true,
    )
    await session.start()
    expect(session.unknown.value).toBe(true)
    await session.recover()
    expect(get).toHaveBeenCalledWith(
      1,
      9,
      expect.objectContaining({ signal: expect.any(AbortSignal) }),
    )
    expect(submit).toHaveBeenCalledTimes(1)
    expect(session.published.value).toBe(true)
    session.dispose()
  })

  it('does not publish completed prepared tasks or match another project result', async () => {
    let receive!: (value: ApiSchemas['StorageTask']) => void
    const list = vi.fn().mockResolvedValue({ items: [artifact()] })
    const changed = vi.fn()
    const session = createExportSession(
      {
        projectId: 1,
        resourceId: 2,
        changed,
        subscribe: (_project, _id, callback) => {
          receive = callback
          return vi.fn()
        },
      },
      {
        ...api,
        createExportArtifact: vi.fn().mockResolvedValue(task()),
        listExportArtifacts: list,
      },
      () => true,
    )
    await session.start()
    receive({ ...task('completed'), phase: 'prepared' })
    await tick()
    expect(session.unknown.value).toBe(true)
    expect(await session.start()).toBe(false)
    receive({ ...task('completed'), project_id: 99 })
    await tick()
    expect(list).not.toHaveBeenCalled()
    expect(changed).not.toHaveBeenCalled()
    expect(session.published.value).toBe(false)
    session.dispose()
  })

  it('tracks tombstone deletion tasks from blocked to done without upload cancellation or retry', async () => {
    let receive!: (value: ApiSchemas['StorageTask']) => void
    const deleted = { ...artifact(), status: 'deleted' as const, deletion_task_id: 19 }
    const list = vi
      .fn()
      .mockResolvedValueOnce({ items: [artifact()] })
      .mockResolvedValue({ items: [deleted] })
    const remove = vi.fn().mockResolvedValue(undefined),
      cancel = vi.fn(),
      retry = vi.fn()
    const session = createExportSession(
      {
        projectId: 1,
        resourceId: 2,
        subscribe: (_project, id, callback) => {
          expect(id).toBe(19)
          receive = callback
          return vi.fn()
        },
      },
      {
        ...api,
        listExportArtifacts: list,
        deleteExportArtifact: remove,
        cancelStorageTask: cancel,
        retryStorageTask: retry,
      },
      () => true,
    )
    await session.refresh()
    expect(await session.remove(5)).toBe(true)
    expect(list.mock.calls.every((call) => call[2]?.includeDeleted === true)).toBe(true)
    expect(session.deletions.value[5]?.status).toBe('pending')
    const deletionTask = {
      ...task('completed'),
      id: 19,
      kind: 'export_delete',
      cleanup_status: 'blocked' as const,
    }
    receive(deletionTask)
    expect(session.deletions.value[5]?.status).toBe('blocked')
    expect(session.canDownload(artifact())).toBe(false)
    receive({ ...deletionTask, cleanup_status: 'done' })
    expect(session.deletions.value[5]?.status).toBe('done')
    expect(cancel).not.toHaveBeenCalled()
    expect(retry).not.toHaveBeenCalled()
    session.dispose()
  })

  it('recovers lost DELETE through tombstones without resending the delete', async () => {
    const remove = vi.fn().mockRejectedValue(new TypeError('lost delete response'))
    const session = createExportSession(
      { projectId: 1, resourceId: 2, subscribe: () => vi.fn() },
      {
        ...api,
        deleteExportArtifact: remove,
        listExportArtifacts: vi
          .fn()
          .mockResolvedValueOnce({ items: [artifact()] })
          .mockResolvedValue({
            items: [{ ...artifact(), status: 'deleted', deletion_task_id: 19 }],
          }),
      },
      () => true,
    )
    await session.refresh()
    await session.remove(5)
    await session.recover()
    expect(session.deletions.value[5]?.taskId).toBe(19)
    expect(await session.remove(5)).toBe(false)
    expect(remove).toHaveBeenCalledTimes(1)
    session.dispose()
  })

  it('rechecks permission after a draft decision and blocks unknown-key replay after losing write access', async () => {
    let allowed = true
    const submit = vi.fn().mockRejectedValue(new TypeError('response lost'))
    const session = createExportSession(
      { projectId: 1, resourceId: 2, canMutate: () => allowed, subscribe: vi.fn() },
      {
        ...api,
        createExportArtifact: submit,
      },
      () => true,
    )
    await session.start()
    allowed = false
    expect(await session.recover()).toBe(false)
    expect(submit).toHaveBeenCalledTimes(1)
    session.revokeAccess()
    expect(session.key.value).toBeNull()
    expect(session.items.value).toEqual([])
    expect(session.canDownload(artifact())).toBe(false)
    session.dispose()
  })
  it('keeps new ready artifacts unavailable until their own task confirms them, while historical ready artifacts stay downloadable', async () => {
    let receive!: (value: ApiSchemas['StorageTask']) => void
    const list = vi
      .fn()
      .mockResolvedValueOnce({ items: [artifact(1)] })
      .mockResolvedValue({ items: [artifact(1), artifact(5)] })
    const session = createExportSession(
      {
        projectId: 1,
        resourceId: 2,
        subscribe: (_project, _id, callback) => {
          receive = callback
          return vi.fn()
        },
      },
      {
        ...api,
        listExportArtifacts: list,
        createExportArtifact: vi.fn().mockResolvedValue(task()),
      },
      () => true,
    )
    await session.refresh()
    expect(session.canDownload(artifact(1))).toBe(true)
    await session.start()
    await session.refresh()
    expect(session.canDownload(artifact(1))).toBe(true)
    expect(session.canDownload(artifact(5))).toBe(false)
    receive(task('completed'))
    await tick()
    expect(session.canDownload(artifact(5))).toBe(true)
    session.dispose()
  })

  it('blocks a new operation until the completed task result is confirmed ready', async () => {
    let receive!: (value: ApiSchemas['StorageTask']) => void
    let resolve!: (value: ApiSchemas['ExportArtifactList']) => void
    const changed = vi.fn()
    const session = createExportSession(
      {
        projectId: 1,
        resourceId: 2,
        changed,
        subscribe: (_project, _id, callback) => {
          receive = callback
          return vi.fn()
        },
      },
      {
        ...api,
        createExportArtifact: vi
          .fn()
          .mockResolvedValueOnce(task())
          .mockResolvedValueOnce({ ...task(), id: 10 }),
        listExportArtifacts: vi.fn().mockReturnValue(new Promise((done) => (resolve = done))),
      },
      () => true,
    )
    await session.start()
    receive(task('completed'))
    expect(await session.start()).toBe(false)
    resolve({ items: [artifact()] })
    await tick()
    expect(session.task.value?.id).toBe(9)
    expect(changed).toHaveBeenCalledTimes(1)
    expect(session.canDownload(artifact())).toBe(true)
    session.dispose()
  })
  it('detaches and fences old callbacks while the next creation response is still pending', async () => {
    const callbacks: Array<(value: ApiSchemas['StorageTask']) => void> = []
    const failures: Array<(error: unknown) => void> = []
    const stop = vi.fn(),
      changed = vi.fn(),
      list = vi.fn().mockResolvedValue({ items: [artifact()] })
    let resolveNext!: (value: ApiSchemas['StorageTask']) => void
    const session = createExportSession(
      {
        projectId: 1,
        resourceId: 2,
        changed,
        subscribe: (_project, _id, callback, fail) => {
          callbacks.push(callback)
          failures.push(fail)
          return stop
        },
      },
      {
        ...api,
        listExportArtifacts: list,
        createExportArtifact: vi
          .fn()
          .mockResolvedValueOnce(task())
          .mockImplementationOnce(
            () =>
              new Promise<ApiSchemas['StorageTask']>((resolve) => {
                resolveNext = resolve
              }),
          ),
      },
      () => true,
    )
    await session.start()
    callbacks[0]!(task('failed'))
    const pending = session.start()
    expect(stop).toHaveBeenCalledTimes(1)
    expect(session.task.value).toBeNull()
    callbacks[0]!(task('completed'))
    failures[0]!(new Error('late old failure'))
    await tick()
    expect(session.task.value).toBeNull()
    expect(session.error.value).toBeNull()
    expect(list).not.toHaveBeenCalled()
    expect(changed).not.toHaveBeenCalled()
    resolveNext({ ...task(), id: 10 })
    await pending
    expect(session.task.value?.id).toBe(10)
    session.dispose()
  })
  it('does not promote a previous unconfirmed ready result into trusted history on another start', async () => {
    const callbacks: Array<(value: ApiSchemas['StorageTask']) => void> = []
    const list = vi
      .fn()
      .mockResolvedValueOnce({ items: [artifact(1)] })
      .mockResolvedValue({ items: [artifact(1), artifact(5), artifact(6)] })
    const session = createExportSession(
      {
        projectId: 1,
        resourceId: 2,
        subscribe: (_project, _id, callback) => {
          callbacks.push(callback)
          return vi.fn()
        },
      },
      {
        ...api,
        listExportArtifacts: list,
        createExportArtifact: vi
          .fn()
          .mockResolvedValueOnce(task())
          .mockResolvedValueOnce({ ...task(), id: 10, result_artifact_id: 6 }),
      },
      () => true,
    )
    await session.refresh()
    await session.start()
    await session.refresh()
    callbacks[0]!(task('failed'))
    expect(session.canDownload(artifact(5))).toBe(false)
    await session.start()
    expect(session.canDownload(artifact(1))).toBe(true)
    expect(session.canDownload(artifact(5))).toBe(false)
    callbacks[1]!({ ...task('completed'), id: 10, result_artifact_id: 6 })
    await tick()
    expect(session.canDownload(artifact(6))).toBe(true)
    expect(session.canDownload(artifact(5))).toBe(false)
    session.dispose()
  })
  it('retains individually confirmed downloads when the next operation starts', async () => {
    let receive!: (value: ApiSchemas['StorageTask']) => void
    const session = createExportSession(
      {
        projectId: 1,
        resourceId: 2,
        subscribe: (_project, _id, callback) => {
          receive = callback
          return vi.fn()
        },
      },
      {
        ...api,
        listExportArtifacts: vi.fn().mockResolvedValue({ items: [artifact()] }),
        createExportArtifact: vi
          .fn()
          .mockResolvedValueOnce(task())
          .mockResolvedValueOnce({ ...task(), id: 10 }),
      },
      () => true,
    )
    await session.start()
    receive(task('completed'))
    await tick()
    expect(session.canDownload(artifact())).toBe(true)
    await session.start()
    expect(session.canDownload(artifact())).toBe(true)
    session.dispose()
  })
  it('does not submit when the draft save fails', async () => {
    const create = vi.fn(),
      before = vi.fn().mockResolvedValue(false)
    const session = createExportSession(
      { projectId: 1, resourceId: 2, beforeSavedContent: before, subscribe: vi.fn() },
      { ...api, createExportArtifact: create },
      () => true,
    )
    expect(await session.start()).toBe(false)
    expect(before).toHaveBeenCalledTimes(1)
    expect(create).not.toHaveBeenCalled()
    expect(session.key.value).toBeNull()
    session.dispose()
  })

  it('keeps the same operation key after response loss and prevents duplicate creation', async () => {
    const create = vi.fn().mockRejectedValue(new TypeError('lost response'))
    const session = createExportSession(
      { projectId: 1, resourceId: 2, subscribe: vi.fn() },
      { ...api, createExportArtifact: create },
      () => true,
    )
    await session.start()
    const key = session.key.value
    expect(key).toBeTruthy()
    expect(session.unknown.value).toBe(true)
    await session.start()
    expect(session.key.value).toBe(key)
    expect(create).toHaveBeenCalledTimes(1)
    session.dispose()
  })

  it('requires task result and a ready artifact before reporting publication', async () => {
    let receive!: (task: ApiSchemas['StorageTask']) => void
    const changed = vi.fn()
    const list = vi
      .fn()
      .mockResolvedValueOnce({ items: [{ ...artifact(), status: 'pending' }] })
      .mockResolvedValueOnce({ items: [artifact()] })
    const session = createExportSession(
      {
        projectId: 1,
        resourceId: 2,
        changed,
        subscribe: (_project, _id, callback) => {
          receive = callback
          return vi.fn()
        },
      },
      {
        ...api,
        createExportArtifact: vi.fn().mockResolvedValue(task()),
        listExportArtifacts: list,
      },
      () => true,
    )
    await session.start()
    receive(task('completed'))
    await tick()
    expect(changed).not.toHaveBeenCalled()
    receive(task('completed'))
    await tick()
    expect(changed).toHaveBeenCalledTimes(1)
    session.dispose()
  })

  it('does not rebuild by saving current drafts and rejects reused artifact identity', async () => {
    let receive!: (task: ApiSchemas['StorageTask']) => void
    const before = vi.fn(),
      changed = vi.fn(),
      rebuild = vi.fn().mockResolvedValue(task())
    const session = createExportSession(
      {
        projectId: 1,
        resourceId: 2,
        beforeSavedContent: before,
        changed,
        subscribe: (_project, _id, callback) => {
          receive = callback
          return vi.fn()
        },
      },
      {
        ...api,
        rebuildExportArtifact: rebuild,
        listExportArtifacts: vi.fn().mockResolvedValue({ items: [artifact()] }),
      },
      () => true,
    )
    await session.refresh()
    await session.start(5)
    expect(before).not.toHaveBeenCalled()
    expect(rebuild.mock.calls[0]?.slice(0, 2)).toEqual([1, 5])
    receive(task('completed'))
    await tick()
    expect(changed).not.toHaveBeenCalled()
    session.dispose()
  })

  it('does not submit a write when current contract gates are unresolved', async () => {
    const create = vi.fn(),
      before = vi.fn()
    const session = createExportSession(
      { projectId: 1, resourceId: 2, beforeSavedContent: before, subscribe: vi.fn() },
      { ...api, createExportArtifact: create },
      () => false,
    )
    expect(await session.start()).toBe(false)
    expect(before).not.toHaveBeenCalled()
    expect(create).not.toHaveBeenCalled()
    session.dispose()
  })

  it('prevents a delayed draft decision from writing into a changed session', async () => {
    let finish!: (value: boolean) => void
    const create = vi.fn()
    const session = createExportSession(
      {
        projectId: 1,
        resourceId: 2,
        beforeSavedContent: () => new Promise((resolve) => (finish = resolve)),
        subscribe: vi.fn(),
      },
      { ...api, createExportArtifact: create },
      () => true,
    )
    const pending = session.start()
    changeSessionContext('/api/v1', 2)
    finish(true)
    expect(await pending).toBe(false)
    expect(create).not.toHaveBeenCalled()
    session.dispose()
  })
})
