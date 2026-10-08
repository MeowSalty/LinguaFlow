import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ApiSchemas } from '@/api/client-core'
import { changeSessionContext } from '@/api/session-context'
import {
  createRepairSession,
  repairContextAllowed,
  repairProjectSnapshotMatches,
  type RepairContext,
} from '../repairSession'
import { organizationRoles } from '@/utils/organization-scope'
import { createMigrationSession } from '../migrationSession'
import { StorageApiError } from '@/api/storage-errors'
import { invalidateStorageSnapshots } from '@/utils/storage-snapshots'

vi.mock('@/i18n', () => ({ t: (key: string) => key }))
const project = {
  id: 1,
  owner_user_id: 1,
  owner_org_id: null,
  storage_generation: 0,
  storage_state: 'active',
} as ApiSchemas['Project']
const target: ApiSchemas['StorageOption'] = {
  space_id: 9,
  name: 'target',
  scope: 'user',
  selectable: true,
  reason_codes: [],
}
const version: ApiSchemas['SourceVersion'] = {
  id: 3,
  current: false,
  format: 'json',
  parser_version: '1',
  verification_state: 'verified',
  location_generation: 0,
  health: 'missing',
}
const task = (overrides: Partial<ApiSchemas['StorageTask']> = {}): ApiSchemas['StorageTask'] => ({
  id: 4,
  project_id: 1,
  resource_id: 2,
  source_revision_id: 3,
  operation_id: 'op',
  kind: 'repair',
  status: 'pending',
  phase: 'accepted',
  cleanup_status: 'done',
  allowed_actions: ['upload_content'],
  created_at: '',
  updated_at: '',
  expires_at: null,
  target_space_id: 9,
  expected_storage_generation: 0,
  expected_location_generation: 0,
  input_size: 3,
  ...overrides,
})
const file = () => new File(['abc'], 'source.json')
const disposals: (() => void)[] = []
afterEach(() => {
  for (const dispose of disposals.splice(0)) dispose()
})
function repair() {
  let context: RepairContext = { project: { ...project }, resourceId: 2, version: { ...version } }
  const deps = {
    createStorageIntent: vi.fn(async () => task()),
    getStorageTask: vi.fn(async () => task()),
    receiveStorageContent: vi.fn(async () =>
      task({ status: 'completed', phase: 'committed', result_revision_id: 3 }),
    ),
  }
  const available = vi.fn(() => true),
    changed = vi.fn()
  const beforeMutation = vi.fn(async (_context: RepairContext) => true)
  const session = createRepairSession(
    { context: () => context, available, changed, beforeMutation },
    deps,
  )
  disposals.push(session.dispose)
  session.selectFile(file())
  return {
    session,
    deps,
    available,
    changed,
    beforeMutation,
    context,
    replaceContext: (c: RepairContext) => {
      context = c
    },
  }
}
beforeEach(() => changeSessionContext('/api/v1', 1, true))
describe('repair original identity and content recovery', () => {
  it.each([
    'storage_deployment_disabled',
    'byos_disabled',
    'storage_maintenance',
    'storage_policy_violation',
  ])(
    'retains repair input and the original key, target and deadline after %s without content replay',
    async (error_code) => {
      const { session, deps } = repair()
      const expires_at = '2026-10-06T12:00:00Z'
      deps.createStorageIntent.mockResolvedValue(task({ expires_at }))
      await session.start(target, 0)
      const candidate = session.file.value
      const original = deps.createStorageIntent.mock.calls[0]?.[1]
      deps.getStorageTask.mockResolvedValue(task({ expires_at }))
      deps.receiveStorageContent.mockRejectedValueOnce(
        new StorageApiError('blocked', 409, { error_code }),
      )
      expect(await session.upload()).toBe(false)
      deps.getStorageTask.mockResolvedValue(
        task({
          status: 'needs_action',
          error_code,
          expires_at,
          allowed_actions: ['cancel'],
        }),
      )
      await session.recover()
      expect(session.file.value).toBe(candidate)
      expect(session.task.value).toMatchObject({
        id: 4,
        target_space_id: 9,
        expires_at,
        status: 'needs_action',
      })
      expect(session.canUpload.value).toBe(false)
      expect(await session.start({ ...target, space_id: 10 }, 0)).toBe(false)
      expect(await session.upload()).toBe(false)
      expect(deps.createStorageIntent).toHaveBeenCalledTimes(1)
      expect(deps.createStorageIntent.mock.calls[0]?.[1]).toBe(original)
      expect(deps.receiveStorageContent).toHaveBeenCalledTimes(1)
      deps.getStorageTask.mockResolvedValue(task({ expires_at }))
      await session.recover()
      expect(session.canUpload.value).toBe(true)
      expect(deps.receiveStorageContent).toHaveBeenCalledTimes(1)
    },
  )
  it('does not upload if task actions were invalidated while permission preflight was in progress', async () => {
    const { session, deps, beforeMutation } = repair()
    await session.start(target, 0)
    beforeMutation.mockImplementation(async () => {
      invalidateStorageSnapshots({ projectId: 1 })
      return true
    })
    expect(await session.upload()).toBe(false)
    expect(deps.receiveStorageContent).not.toHaveBeenCalled()
    expect(session.file.value).not.toBeNull()
    expect(session.task.value?.id).toBe(4)
  })
  it('does not create an intent or consume the candidate when fresh permission preflight refuses', async () => {
    const { session, deps, beforeMutation } = repair()
    beforeMutation.mockResolvedValue(false)
    expect(await session.start(target, 0)).toBe(false)
    expect(deps.createStorageIntent).not.toHaveBeenCalled()
    expect(session.hasOperation.value).toBe(false)
    beforeMutation.mockResolvedValue(true)
    expect(await session.start(target, 0)).toBe(true)
  })
  it('rechecks fresh project state after latest task actions and stops content on maintenance transition', async () => {
    const { session, deps, beforeMutation } = repair()
    await session.start(target, 0)
    beforeMutation.mockImplementation(async (context) =>
      repairProjectSnapshotMatches(context.project, {
        ...context.project,
        storage_state: 'draining',
      }),
    )
    expect(await session.upload()).toBe(false)
    expect(deps.getStorageTask).toHaveBeenCalled()
    expect(deps.receiveStorageContent).not.toHaveBeenCalled()
  })
  it('allows unchanged maintenance repair while rejecting new maintenance, stale generation and organization downgrade', () => {
    expect(repairProjectSnapshotMatches(project, { ...project, storage_state: 'draining' })).toBe(
      false,
    )
    expect(repairProjectSnapshotMatches(project, { ...project, storage_generation: 1 })).toBe(false)
    for (const state of ['draining', 'migrating']) {
      const expected = { ...project, storage_state: state }
      expect(repairProjectSnapshotMatches(expected, { ...expected })).toBe(true)
    }
    const orgProject = { ...project, owner_user_id: null, owner_org_id: 8 }
    organizationRoles.value = { 8: 'admin' }
    expect(repairProjectSnapshotMatches(orgProject, orgProject)).toBe(true)
    organizationRoles.value = { 8: 'member' }
    expect(repairProjectSnapshotMatches(orgProject, orgProject)).toBe(false)
  })
  it('does not transmit after a service change during mutation preflight', async () => {
    const { session, deps, beforeMutation } = repair()
    beforeMutation.mockImplementation(async () => {
      changeSessionContext('/other', 2, true)
      return true
    })
    expect(await session.start(target, 0)).toBe(false)
    expect(deps.createStorageIntent).not.toHaveBeenCalled()
  })
  it('uses the live contract matrix for trusted maintenance repair and blocks legacy or unknown states', () => {
    const context = { project: { ...project }, resourceId: 2, version: { ...version } }
    for (const state of ['active', 'draining', 'migrating']) {
      context.project.storage_state = state
      expect(repairContextAllowed(context)).toBe(true)
    }
    for (const state of ['legacy_migration', 'legacy_rollback', 'future_state']) {
      context.project.storage_state = state
      expect(repairContextAllowed(context)).toBe(false)
    }
    context.project.storage_state = 'active'
    context.version.verification_state = 'legacy_unverified'
    expect(repairContextAllowed(context)).toBe(false)
  })
  it('creates a strict intent for the selected historical version with zero generations', async () => {
    const { session, deps } = repair()
    await session.start(target, 0)
    expect(deps.createStorageIntent.mock.calls[0]?.[1]).toEqual({
      kind: 'repair',
      idempotency_key: expect.any(String),
      size: 3,
      storage_generation: 0,
      resource_id: 2,
      source_revision_id: 3,
      location_generation: 0,
      target_space_id: 9,
    })
    await session.upload()
    expect(deps.receiveStorageContent).toHaveBeenCalledWith(
      1,
      4,
      expect.any(File),
      expect.any(Object),
    )
    expect(session.completed.value).toBe(true)
  })
  it('rechecks permission after reading latest task, before transmitting content', async () => {
    const { session, deps, available } = repair()
    await session.start(target, 0)
    deps.getStorageTask.mockImplementation(async () => {
      available.mockReturnValue(false)
      return task()
    })
    expect(await session.upload()).toBe(false)
    expect(deps.receiveStorageContent).not.toHaveBeenCalled()
  })
  it('refuses a stale location generation or missing server action', async () => {
    const { session, deps } = repair()
    await session.start(target, 0)
    deps.getStorageTask.mockResolvedValue(task({ expected_location_generation: 1 }))
    expect(await session.upload()).toBe(false)
    deps.getStorageTask.mockResolvedValue(task({ allowed_actions: [] }))
    expect(await session.upload()).toBe(false)
    expect(deps.receiveStorageContent).not.toHaveBeenCalled()
  })
  it('restores an original task and requires a freshly selected file without inventing a new intent', async () => {
    const { session, deps } = repair()
    session.selectFile(null)
    expect(await session.recover(4)).toBe(true)
    expect(await session.upload()).toBe(false)
    session.selectFile(file())
    expect(await session.upload()).toBe(true)
    expect(deps.createStorageIntent).not.toHaveBeenCalled()
  })
  it('replays a lost intent response with the same request and key', async () => {
    const { session, deps } = repair()
    deps.createStorageIntent.mockRejectedValueOnce(new Error('network'))
    await session.start(target, 0)
    await session.recover()
    expect(deps.createStorageIntent.mock.calls[1]?.[1]).toEqual(
      deps.createStorageIntent.mock.calls[0]?.[1],
    )
  })
  it('keeps backend content mismatch as a failure and never reports completion', async () => {
    const { session, deps, changed } = repair()
    await session.start(target, 0)
    deps.receiveStorageContent.mockRejectedValueOnce(
      new StorageApiError('mismatch', 409, { error_code: 'repair_content_mismatch' }),
    )
    expect(await session.upload()).toBe(false)
    expect(session.completed.value).toBe(false)
    expect(changed).not.toHaveBeenCalled()
  })
  it('does not treat a prepared task or another result version as repaired', async () => {
    const { session, deps, changed } = repair()
    deps.getStorageTask.mockResolvedValue(
      task({ status: 'completed', phase: 'prepared', result_revision_id: 3 }),
    )
    await session.recover(4)
    expect(session.completed.value).toBe(false)
    deps.getStorageTask.mockResolvedValue(
      task({ status: 'completed', phase: 'committed', result_revision_id: 8 }),
    )
    await session.recover()
    expect(changed).not.toHaveBeenCalled()
  })
  it('ignores a response from a prior service session', async () => {
    const { session, deps } = repair()
    deps.createStorageIntent.mockImplementation(async () => {
      changeSessionContext('/other', 2, true)
      return task()
    })
    expect(await session.start(target, 0)).toBe(false)
    expect(session.task.value).toBeNull()
  })
})
describe('migration reconciliation', () => {
  function setup() {
    const current = { ...project }
    const deps = {
      migrateProjectStorage: vi.fn(async () => task({ kind: 'migration', phase: 'copy' })),
      getStorageTask: vi.fn(async () =>
        task({ kind: 'migration', phase: 'cutover', allowed_actions: [] }),
      ),
    }
    const available = vi.fn(() => true)
    const session = createMigrationSession({ project: () => current, available }, deps)
    disposals.push(session.dispose)
    return {
      current,
      deps,
      available,
      session,
    }
  }
  it.each(['storage_deployment_disabled', 'byos_disabled', 'storage_maintenance'])(
    'reconciles a migration refused by %s with its original task, target and cleanup facts',
    async (error_code) => {
      const { session, deps, current, available } = setup()
      deps.migrateProjectStorage.mockRejectedValueOnce(
        new StorageApiError('blocked', 409, { error_code, task_id: 4 }),
      )
      await session.start(target, 0)
      const original = deps.migrateProjectStorage.mock.calls[0]?.[1]
      available.mockReturnValue(false)
      current.storage_state = 'migrating'
      deps.getStorageTask.mockResolvedValue(
        task({
          kind: 'migration',
          phase: 'cutover',
          status: 'needs_action',
          cleanup_status: 'blocked',
          error_code,
          allowed_actions: [],
          expires_at: '2026-10-06T12:00:00Z',
        }),
      )
      expect(await session.recover()).toBe(true)
      expect(session.task.value).toMatchObject({
        id: 4,
        target_space_id: 9,
        phase: 'cutover',
        status: 'needs_action',
        cleanup_status: 'blocked',
        expires_at: '2026-10-06T12:00:00Z',
        allowed_actions: [],
      })
      expect(session.startNew()).toBe(false)
      expect(await session.start({ ...target, space_id: 10 }, 0)).toBe(false)
      expect(deps.migrateProjectStorage).toHaveBeenCalledTimes(1)
      expect(deps.migrateProjectStorage.mock.calls[0]?.[1]).toBe(original)
      deps.getStorageTask.mockResolvedValue(
        task({ kind: 'migration', phase: 'cutover', allowed_actions: ['retry'] }),
      )
      await session.recover()
      expect(session.task.value?.allowed_actions).toEqual(['retry'])
      expect(deps.migrateProjectStorage).toHaveBeenCalledTimes(1)
    },
  )
  it('rejects absent generations and target discovery refusals', async () => {
    const { session, deps } = setup()
    expect(await session.start(target, Number.NaN)).toBe(false)
    expect(await session.start({ ...target, selectable: false }, 0)).toBe(false)
    expect(deps.migrateProjectStorage).not.toHaveBeenCalled()
  })
  it('retains request identity after a lost response rather than switching target', async () => {
    const { session, deps } = setup()
    deps.migrateProjectStorage.mockRejectedValueOnce(new Error('network'))
    await session.start(target, 0)
    expect(await session.start({ ...target, space_id: 10 }, 0)).toBe(false)
    await session.recover()
    expect(deps.migrateProjectStorage.mock.calls[1]?.[1]).toEqual(
      deps.migrateProjectStorage.mock.calls[0]?.[1],
    )
  })
  it('restores cutover from its task id without creating another migration', async () => {
    const { session, deps, current, available } = setup()
    current.storage_state = 'migrating'
    available.mockReturnValue(false)
    expect(await session.recover(4)).toBe(true)
    expect(session.task.value?.phase).toBe('cutover')
    expect(deps.migrateProjectStorage).not.toHaveBeenCalled()
  })
  it('requires a confirmed terminal task before starting another migration with a fresh key', async () => {
    const { session, deps } = setup()
    await session.start(target, 0)
    const original = deps.migrateProjectStorage.mock.calls[0]?.[1]
    expect(session.startNew()).toBe(false)
    deps.getStorageTask.mockResolvedValue(
      task({ kind: 'migration', status: 'completed', phase: 'committed', allowed_actions: [] }),
    )
    await session.recover()
    expect(session.startNew()).toBe(true)
    await session.start(target, 0)
    expect(deps.migrateProjectStorage.mock.calls[1]?.[1]).not.toEqual(original)
  })
})
