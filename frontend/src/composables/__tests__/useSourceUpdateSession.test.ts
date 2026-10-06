import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createSourceUpdateSession } from '../useSourceUpdateSession'
import { changeSessionContext } from '@/api/session-context'
import type { ApiSchemas } from '@/api/client'
import { StorageApiError } from '@/api/storage-errors'

vi.mock('@/api/storage', () => ({
  previewSourceUpdate: vi.fn(),
  commitSourceUpdate: vi.fn(),
  getStorageTask: vi.fn(),
}))
vi.mock('@/i18n', () => ({ t: (key: string) => key }))
const preview: ApiSchemas['SourceUpdatePreview'] = {
  task_id: 42,
  source_generation: 0,
  translation_generation: 9,
  stats: { added: 2, updated: 1, unchanged: 3, deleted: 4 },
  expires_at: null,
}
const task: ApiSchemas['StorageTask'] = {
  id: 42,
  operation_id: 'operation',
  kind: 'source_update',
  status: 'completed',
  phase: 'prepared',
  cleanup_status: 'done',
  allowed_actions: [],
  project_id: 1,
  resource_id: 2,
  created_at: '2026-10-04T00:00:00Z',
  updated_at: '2026-10-04T00:00:00Z',
  expires_at: null,
}
const file = () => new File(['source'], 'source.json')
const deferred = <T>() => {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((yes) => {
    resolve = yes
  })
  return { promise, resolve }
}
const setup = () => {
  const deps = {
    available: vi.fn(() => true),
    beforeSavedContent: vi.fn(async () => true),
    preview: vi.fn(async () => preview),
    commit: vi.fn(async () => task),
    task: vi.fn(async () => task),
  }
  return { deps, session: createSourceUpdateSession(deps) }
}

describe('source update confirmation session', () => {
  beforeEach(() => changeSessionContext('/api/v1', 1, true))
  it('recovers a lost preview with the original key before inspecting its original task', async () => {
    const { deps, session } = setup()
    deps.preview.mockRejectedValueOnce(new Error('lost response'))
    deps.task.mockResolvedValue({
      ...task,
      phase: 'committed',
      result_resource_id: 2,
      result_revision_id: 7,
    })
    const candidate = file()
    session.select(1, 2, candidate)
    const key = session.key.value
    await session.prepare()
    expect(session.newPreview()).toBe(false)
    await session.recover()
    expect(deps.preview).toHaveBeenLastCalledWith(1, 2, candidate, key, expect.anything())
    expect(session.state.value).toBe('completed')
    expect(session.published.value?.revisionId).toBe(7)
  })
  it('uses a fresh key for a new preview after a source conflict, even for the same File', async () => {
    const { deps, session } = setup()
    deps.commit.mockRejectedValue(
      new StorageApiError('conflict', 409, { error_code: 'source_revision_conflict' }),
    )
    session.select(1, 2, file())
    const original = session.key.value
    await session.prepare()
    await session.confirm()
    await session.prepare()
    expect(session.key.value).not.toBe(original)
    expect(deps.preview).toHaveBeenCalledTimes(2)
  })
  it('preserves the original published revision when a later read reports another result', async () => {
    const { deps, session } = setup()
    deps.commit.mockResolvedValue({
      ...task,
      phase: 'committed',
      result_resource_id: 2,
      result_revision_id: 7,
    })
    session.select(1, 2, file())
    await session.prepare()
    await session.confirm()
    deps.task.mockResolvedValue({
      ...task,
      phase: 'committed',
      result_resource_id: 2,
      result_revision_id: 8,
    })
    expect(await session.refresh()).toBe(false)
    expect(session.published.value?.revisionId).toBe(7)
    expect(session.refreshError.value).toBe(true)
  })
  it('only restores complete frozen plans whose generations match their task', () => {
    const { session } = setup()
    session.restore(1, 2, {
      ...task,
      status: 'running',
      allowed_actions: ['commit'],
      source_preview: preview,
      expected_source_generation: 0,
      expected_translation_generation: 9,
      expected_storage_generation: 0,
    })
    expect(session.state.value).toBe('preview_ready')
    session.reset()
    session.restore(1, 2, { ...task, status: 'running', allowed_actions: ['commit'] })
    expect(session.state.value).toBe('read_only')
  })
  it('blocks all writes until the contract is available', async () => {
    const { deps, session } = setup()
    deps.available.mockReturnValue(false)
    session.select(1, 2, file())
    expect(await session.prepare()).toBe(false)
    expect(deps.beforeSavedContent).not.toHaveBeenCalled()
    expect(deps.preview).not.toHaveBeenCalled()
    expect(await session.confirm()).toBe(false)
    expect(deps.commit).not.toHaveBeenCalled()
  })
  it('failed draft saving never uploads a candidate', async () => {
    const { deps, session } = setup()
    deps.beforeSavedContent.mockResolvedValue(false)
    session.select(1, 2, file())
    await session.prepare()
    expect(deps.preview).not.toHaveBeenCalled()
    expect(session.state.value).toBe('selected')
  })
  it('a thrown draft exception leaves the candidate usable without a request', async () => {
    const { deps, session } = setup()
    deps.beforeSavedContent.mockRejectedValue(new Error('save failed'))
    session.select(1, 2, file())
    expect(await session.prepare()).toBe(false)
    expect(deps.preview).not.toHaveBeenCalled()
    expect(session.state.value).toBe('selected')
    deps.beforeSavedContent.mockResolvedValue(true)
    await session.prepare()
    deps.beforeSavedContent.mockRejectedValue(new Error('save failed'))
    expect(await session.confirm()).toBe(false)
    expect(deps.commit).not.toHaveBeenCalled()
    expect(session.state.value).toBe('preview_ready')
  })
  it('commits the exact preview generations including zero', async () => {
    const { deps, session } = setup()
    session.select(1, 2, file())
    await session.prepare()
    await session.confirm()
    expect(deps.commit).toHaveBeenCalledWith(
      1,
      2,
      { task_id: 42, expected_source_generation: 0, expected_translation_generation: 9 },
      expect.anything(),
    )
    expect(session.state.value).toBe('read_only')
    // completed/prepared is not a publication signal.
    expect(session.task.value?.phase).toBe('prepared')
  })
  it('does not let a late candidate A replace selected candidate B', async () => {
    const { deps, session } = setup()
    const response = deferred<ApiSchemas['SourceUpdatePreview']>()
    deps.preview.mockReturnValue(response.promise)
    session.select(1, 2, file())
    const pending = session.prepare()
    await Promise.resolve()
    const candidateB = file()
    session.select(1, 3, candidateB)
    response.resolve(preview)
    await pending
    expect(session.resourceId.value).toBe(3)
    expect(session.file.value).toBe(candidateB)
    expect(session.preview.value).toBeNull()
    expect(session.state.value).toBe('selected')
  })
  it('ignores preview responses from a replaced login session', async () => {
    const { deps, session } = setup()
    const response = deferred<ApiSchemas['SourceUpdatePreview']>()
    deps.preview.mockReturnValue(response.promise)
    session.select(1, 2, file())
    const pending = session.prepare()
    await Promise.resolve()
    changeSessionContext('/api/v1', 2, true)
    response.resolve(preview)
    await pending
    expect(session.preview.value).toBeNull()
  })
  it('preserves identity after commit response loss and does not replay commit', async () => {
    const { deps, session } = setup()
    deps.commit.mockRejectedValue(new Error('connection lost'))
    session.select(1, 2, file())
    const key = session.key.value
    await session.prepare()
    await session.confirm()
    expect(session.state.value).toBe('unknown')
    expect(session.select(1, 2, file())).toBe(false)
    await session.confirm()
    await session.refresh()
    expect(session.key.value).toBe(key)
    expect(deps.commit).toHaveBeenCalledTimes(1)
    expect(deps.task).toHaveBeenCalledWith(1, 42, expect.anything())
  })
  it('never submits or refreshes a preview selected by another login', async () => {
    const { deps, session } = setup()
    session.select(1, 2, file())
    await session.prepare()
    changeSessionContext('/api/v1', 2, true)
    expect(await session.confirm()).toBe(false)
    await session.refresh()
    expect(deps.commit).not.toHaveBeenCalled()
    expect(deps.task).not.toHaveBeenCalled()
    expect(session.file.value).toBeNull()
    expect(session.preview.value).toBeNull()
  })
  it('does not upload a selected candidate after switching API servers', async () => {
    const { deps, session } = setup()
    session.select(1, 2, file())
    changeSessionContext('https://other.example/api/v1', 1, true)
    expect(await session.prepare()).toBe(false)
    expect(deps.preview).not.toHaveBeenCalled()
  })
  it('pauses confirmation while a fresh task read is in flight', async () => {
    const { deps, session } = setup()
    const response = deferred<ApiSchemas['StorageTask']>()
    deps.task.mockReturnValue(response.promise)
    deps.commit.mockResolvedValue({ ...task, phase: 'committed' })
    session.select(1, 2, file())
    await session.prepare()
    const reading = session.refresh()
    expect(await session.confirm()).toBe(false)
    response.resolve(task)
    await reading
    expect(deps.commit).not.toHaveBeenCalled()
    expect(session.task.value?.phase).toBe('prepared')
  })
  it('invalidates confirmation when saving before submit changes content', async () => {
    const { deps, session } = setup()
    session.select(1, 2, file())
    await session.prepare()
    deps.beforeSavedContent.mockImplementation(async () => {
      session.invalidate()
      return true
    })
    expect(await session.confirm()).toBe(false)
    expect(deps.commit).not.toHaveBeenCalled()
    expect(session.state.value).toBe('invalidated')
  })
  it('rejects unsafe generation values rather than defaulting them', async () => {
    const { deps, session } = setup()
    deps.preview.mockResolvedValue({
      ...preview,
      translation_generation: Number.MAX_SAFE_INTEGER + 1,
    })
    session.select(1, 2, file())
    expect(await session.prepare()).toBe(false)
    await session.confirm()
    expect(deps.commit).not.toHaveBeenCalled()
  })
  it('deduplicates simultaneous confirmation gestures', async () => {
    const { deps, session } = setup()
    session.select(1, 2, file())
    await session.prepare()
    await Promise.all([session.confirm(), session.confirm()])
    expect(deps.commit).toHaveBeenCalledTimes(1)
  })
  it('a structured conflict invalidates confirmation without retrying', async () => {
    const { deps, session } = setup()
    deps.commit.mockRejectedValue(
      new StorageApiError('conflict', 409, { error_code: 'source_revision_conflict' }),
    )
    session.select(1, 2, file())
    await session.prepare()
    await session.confirm()
    expect(session.state.value).toBe('invalidated')
    expect(deps.commit).toHaveBeenCalledTimes(1)
  })
  it.each([403, 413, 422])(
    'a definite HTTP %i preview failure remains recoverable',
    async (status) => {
      const { deps, session } = setup()
      deps.preview.mockRejectedValue({ status })
      session.select(1, 2, file())
      const key = session.key.value
      await session.prepare()
      expect(session.state.value).toBe('failed')
      expect(session.key.value).toBe(key)
      expect(session.select(1, 2, file())).toBe(true)
    },
  )
  it('does not lock a definite rejected commit into result unknown', async () => {
    const { deps, session } = setup()
    deps.commit.mockRejectedValue({ status: 422 })
    session.select(1, 2, file())
    await session.prepare()
    await session.confirm()
    expect(session.state.value).toBe('failed')
    expect(session.preview.value?.task_id).toBe(42)
  })
  it.each([
    'storage_deployment_disabled',
    'byos_disabled',
    'storage_maintenance',
    'storage_policy_violation',
  ])(
    'preserves candidate, key, expiry and original task after %s without inventing a task status',
    async (error_code) => {
      const { deps, session } = setup()
      const candidate = file()
      deps.commit.mockRejectedValue(new StorageApiError('blocked', 403, { error_code }))
      session.select(1, 2, candidate)
      const key = session.key.value
      await session.prepare()
      await session.confirm()
      expect(session.state.value).toBe('blocked')
      expect(session.file.value).toBe(candidate)
      expect(session.key.value).toBe(key)
      expect(session.taskId.value).toBe(42)
      expect(session.preview.value).toEqual(preview)
      expect(session.task.value).toBeNull()
      expect(await session.prepare()).toBe(false)
      deps.task.mockResolvedValue({
        ...task,
        status: 'pending',
        allowed_actions: [],
        source_preview: preview,
        expected_source_generation: 0,
        expected_translation_generation: 9,
        expected_storage_generation: 0,
      })
      await session.recover()
      expect(await session.confirm()).toBe(false)
      expect(session.state.value).toBe('blocked')
      expect(session.select(1, 2, file())).toBe(false)
      expect(await session.prepare()).toBe(false)
      expect(session.lastError.value?.error_code).toBe(error_code)
      expect(deps.commit).toHaveBeenCalledTimes(1)
      expect(deps.preview).toHaveBeenCalledTimes(1)
    },
  )
  it.each(['storage_deployment_disabled', 'byos_disabled'])(
    'keeps a server-reported failed task with %s attached to its original candidate until recovery allows commit',
    async (error_code) => {
      const { deps, session } = setup()
      const candidate = file()
      session.select(1, 2, candidate)
      await session.prepare()
      const key = session.key.value
      deps.task.mockResolvedValue({ ...task, status: 'failed', error_code, allowed_actions: [] })
      await session.recover()
      expect(session.state.value).toBe('blocked')
      expect(session.file.value).toBe(candidate)
      expect(session.key.value).toBe(key)
      expect(session.preview.value).toEqual(preview)
      expect(session.newPreview()).toBe(false)
      deps.task.mockResolvedValue({
        ...task,
        status: 'pending',
        allowed_actions: ['commit'],
        source_preview: preview,
        expected_source_generation: 0,
        expected_translation_generation: 9,
        expected_storage_generation: 0,
      })
      await session.recover()
      expect(session.state.value).toBe('preview_ready')
      expect(deps.commit).not.toHaveBeenCalled()
      expect(deps.preview).toHaveBeenCalledTimes(1)
    },
  )
  it('does not let an in-flight old task read undo synchronous snapshot invalidation', async () => {
    const { deps, session } = setup()
    session.select(1, 2, file())
    await session.prepare()
    const pending = deferred<ApiSchemas['StorageTask']>()
    deps.task.mockReturnValue(pending.promise)
    const reading = session.refresh()
    session.invalidateTaskSnapshot()
    pending.resolve({
      ...task,
      status: 'pending',
      allowed_actions: ['commit'],
      source_preview: preview,
      expected_source_generation: 0,
      expected_translation_generation: 9,
      expected_storage_generation: 0,
    })
    expect(await reading).toBe(false)
    expect(session.taskSnapshotReady.value).toBe(false)
    expect(await session.confirm()).toBe(false)
    expect(deps.commit).not.toHaveBeenCalled()
  })
})
