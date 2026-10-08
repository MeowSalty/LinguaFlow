import { effectScope, nextTick, reactive, ref } from 'vue'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { ApiSchemas } from '@/api/client-core'
import { changeSessionContext } from '@/api/session-context'
import { ApiError } from '@/api/utils'
import { expandsRetention, useTaskRetention, validRetentionDays } from '../useTaskRetention'

type Policy = ApiSchemas['TaskRetentionPolicy']
type Preview = ApiSchemas['TaskRetentionPreview']
type Settings = ApiSchemas['SystemSettingsResponse']['settings']
const mocks = vi.hoisted(() => ({ preview: vi.fn(), status: vi.fn(), admin: null as unknown }))
vi.mock('@/api/task-history', () => ({
  fetchTaskRetentionPreview: mocks.preview,
  fetchTaskRetentionStatus: mocks.status,
  taskHistoryErrorMessage: () => 'safe error',
}))
vi.mock('@/stores/admin', () => ({ useAdminStore: () => mocks.admin }))
vi.mock('@/i18n', () => ({ t: (key: string) => key }))
const policy = (enabled = false, days = 30, revision = 1): Policy => ({
  enabled,
  retention_days: days,
  revision,
})
const preview = (revision = 1, partial = false): Preview => ({
  policy_revision: revision,
  retention_days: 7,
  as_of: '2026-10-06T00:00:00Z',
  cutoff: '2026-09-29T00:00:00Z',
  partial,
  incomplete_reasons: [],
  by_type: {} as Preview['by_type'],
})
let scope: ReturnType<typeof effectScope>
let admin: ReturnType<typeof createAdmin>
let doc: EventTarget & { visibilityState: string }
function createAdmin() {
  return reactive({
    settings: { registration_enabled: true, task_retention: policy() } as Settings | null,
    settingsLoading: false,
    settingsSaving: false,
    settingsError: null as string | null,
    settingsSaveError: null as string | null,
    settingsAccessDenied: false,
    clearSettings: vi.fn((denied = false) => {
      admin.settings = null
      admin.settingsAccessDenied = denied
    }),
    loadSettings: vi.fn(async () => true),
    acceptRetentionPolicy: vi.fn((value: Policy) => {
      if (admin.settings && value.revision >= admin.settings.task_retention.revision)
        admin.settings = { ...admin.settings, task_retention: value }
    }),
    saveTaskRetention: vi.fn(
      async (
        _patch: ApiSchemas['TaskRetentionPatch'],
      ): Promise<'saved' | 'conflict' | 'failed' | 'stale'> => 'saved',
    ),
  })
}
function setup() {
  scope = effectScope()
  const authorized = ref(true)
  const controller = scope.run(() => useTaskRetention(() => authorized.value))!
  return { controller, authorized }
}
beforeEach(() => {
  vi.useFakeTimers()
  changeSessionContext('/api/v1', 1, true)
  admin = createAdmin()
  mocks.admin = admin
  doc = Object.assign(new EventTarget(), { visibilityState: 'visible' })
  vi.stubGlobal('document', doc)
  mocks.preview.mockResolvedValue(preview())
  mocks.status.mockImplementation(async () => ({
    task_retention: admin.settings!.task_retention,
    policy_revision: admin.settings!.task_retention.revision,
    state: 'idle',
    reason_codes: [],
    last_scan: null,
    backlog: null,
  }))
})
afterEach(() => {
  scope?.stop()
  vi.useRealTimers()
  vi.unstubAllGlobals()
})

describe('task retention policy drafts and lifecycle', () => {
  it('validates integer days without accepting null or strings', () => {
    for (const value of [null, '', '7', 0, -1, 1.5, 3651, Infinity])
      expect(validRetentionDays(value)).toBe(false)
    expect(validRetentionDays(1)).toBe(true)
    expect(validRetentionDays(3650)).toBe(true)
    expect(expandsRetention({ enabled: false, retention_days: 7 }, policy(true))).toBe(false)
    expect(expandsRetention({ enabled: true, retention_days: 7 }, policy(true))).toBe(true)
  })
  it('never fabricates defaults after an initial read failure', async () => {
    admin.settings = null
    const { controller } = setup()
    expect(controller.draft.value).toBeNull()
    expect(controller.canSave.value).toBe(false)
    await controller.save()
    expect(admin.saveTaskRetention).not.toHaveBeenCalled()
  })
  it('preserves dirty retention drafts after unrelated registration saves', () => {
    const { controller } = setup()
    controller.updateDraft({ retention_days: 7 })
    admin.settings = { registration_enabled: false, task_retention: policy() }
    expect(controller.draft.value?.retention_days).toBe(7)
    expect(controller.conflict.value).toBe(false)
  })
  it('always re-previews enablement and binds the save to a frozen revision', async () => {
    const { controller } = setup()
    controller.updateDraft({ retention_days: 7, enabled: true })
    await controller.readPreview()
    await controller.save()
    expect(mocks.preview).toHaveBeenCalledTimes(2)
    expect(admin.saveTaskRetention).not.toHaveBeenCalled()
    expect(controller.confirmation.value?.patch).toEqual({
      enabled: true,
      retention_days: 7,
      expected_revision: 1,
    })
    admin.saveTaskRetention.mockImplementation(async (patch) => {
      admin.settings = {
        registration_enabled: true,
        task_retention: policy(patch.enabled, patch.retention_days, 2),
      }
      return 'saved'
    })
    expect(await controller.confirmSave()).toBe(true)
    expect(admin.saveTaskRetention).toHaveBeenCalledExactlyOnceWith({
      enabled: true,
      retention_days: 7,
      expected_revision: 1,
    })
    expect(controller.hasChanges.value).toBe(false)
  })
  it('requires explicit acknowledgement of a partial preview', async () => {
    mocks.preview.mockResolvedValue(preview(1, true))
    const { controller } = setup()
    controller.updateDraft({ retention_days: 7, enabled: true })
    await controller.save()
    expect(await controller.confirmSave()).toBe(false)
    expect(admin.saveTaskRetention).not.toHaveBeenCalled()
    controller.partialAccepted.value = true
    await controller.confirmSave()
    expect(admin.saveTaskRetention).toHaveBeenCalledTimes(1)
  })
  it('blocks dangerous saves after preview errors, while allowing disable and extension', async () => {
    mocks.preview.mockRejectedValue(new Error('private server detail'))
    const { controller } = setup()
    controller.updateDraft({ enabled: true, retention_days: 7 })
    await controller.save()
    expect(controller.confirmation.value).toBeNull()
    expect(controller.previewError.value).toBe('safe error')
    expect(admin.saveTaskRetention).not.toHaveBeenCalled()
    controller.resetDraft()
    admin.settings = { registration_enabled: true, task_retention: policy(true, 30, 2) }
    controller.updateDraft({ enabled: false })
    await controller.save()
    expect(admin.saveTaskRetention).toHaveBeenCalledWith({
      enabled: false,
      retention_days: 30,
      expected_revision: 2,
    })
    controller.resetDraft()
    controller.updateDraft({ retention_days: 90 })
    await controller.save()
    expect(admin.saveTaskRetention).toHaveBeenCalledWith({
      enabled: true,
      retention_days: 90,
      expected_revision: 2,
    })
    expect(mocks.preview).toHaveBeenCalledTimes(1)
  })
  it('retains draft on 409 and requires an explicit rebase before another submit', async () => {
    admin.saveTaskRetention.mockResolvedValueOnce('conflict')
    admin.loadSettings.mockImplementation(async () => {
      admin.settings = { registration_enabled: false, task_retention: policy(true, 10, 2) }
      return true
    })
    const { controller } = setup()
    controller.updateDraft({ retention_days: 7 })
    await controller.save()
    expect(controller.draft.value).toEqual({ enabled: false, retention_days: 7 })
    expect(controller.baseline.value?.revision).toBe(1)
    expect(controller.conflict.value).toBe(true)
    await controller.save()
    expect(admin.saveTaskRetention).toHaveBeenCalledTimes(1)
    controller.useLatest()
    await controller.save()
    expect(admin.saveTaskRetention).toHaveBeenLastCalledWith({
      enabled: false,
      retention_days: 7,
      expected_revision: 2,
    })
  })
  it('keeps conflict blocked when the new baseline cannot be read', async () => {
    admin.saveTaskRetention.mockResolvedValue('conflict')
    admin.loadSettings.mockResolvedValue(false)
    const { controller } = setup()
    controller.updateDraft({ retention_days: 7 })
    await controller.save()
    controller.useLatest()
    expect(controller.conflict.value).toBe(true)
    expect(controller.conflictReadFailed.value).toBe(true)
    expect(controller.canSave.value).toBe(false)
    expect(controller.draft.value?.retention_days).toBe(7)
  })
  it('revokes a confirmation when background status discovers a new policy', async () => {
    const { controller } = setup()
    controller.updateDraft({ enabled: true, retention_days: 7 })
    await controller.save()
    expect(controller.confirmation.value).not.toBeNull()
    mocks.status.mockResolvedValue({
      task_retention: policy(true, 15, 2),
      policy_revision: 2,
      state: 'idle',
      reason_codes: [],
      last_scan: null,
      backlog: null,
    })
    controller.start()
    await nextTick()
    expect(controller.confirmation.value).toBeNull()
    expect(controller.conflict.value).toBe(true)
    expect(controller.draft.value?.retention_days).toBe(7)
    expect(controller.baseline.value?.revision).toBe(1)
  })
  it('rejects late previews after a days edit and after session changes', async () => {
    let resolve!: (value: Preview) => void
    mocks.preview.mockImplementation(
      () =>
        new Promise<Preview>((yes) => {
          resolve = yes
        }),
    )
    const { controller } = setup()
    controller.updateDraft({ retention_days: 7 })
    const reading = controller.readPreview()
    controller.updateDraft({ retention_days: 15 })
    resolve(preview())
    await reading
    expect(controller.preview.value).toBeNull()
    controller.updateDraft({ retention_days: 7 })
    const stale = controller.readPreview()
    changeSessionContext('/api/v1', 2)
    resolve(preview())
    await stale
    expect(controller.preview.value).toBeNull()
    expect(controller.draft.value).toBeNull()
  })
  it('polls at 10/30 seconds only while visible and stops after disabling', async () => {
    admin.settings!.task_retention = policy(true)
    mocks.status.mockImplementation(async () => ({
      task_retention: admin.settings!.task_retention,
      policy_revision: 1,
      state: 'running',
      reason_codes: [],
      last_scan: null,
      backlog: null,
    }))
    const { controller } = setup()
    controller.start()
    await nextTick()
    await vi.advanceTimersByTimeAsync(10_000)
    expect(mocks.status).toHaveBeenCalledTimes(2)
    doc.visibilityState = 'hidden'
    doc.dispatchEvent(new Event('visibilitychange'))
    await vi.advanceTimersByTimeAsync(60_000)
    expect(mocks.status).toHaveBeenCalledTimes(2)
    doc.visibilityState = 'visible'
    mocks.status.mockResolvedValue({
      task_retention: policy(true),
      policy_revision: 1,
      state: 'idle',
      reason_codes: [],
      last_scan: null,
      backlog: null,
    })
    doc.dispatchEvent(new Event('visibilitychange'))
    await nextTick()
    await vi.advanceTimersByTimeAsync(29_999)
    expect(mocks.status).toHaveBeenCalledTimes(3)
    await vi.advanceTimersByTimeAsync(1)
    expect(mocks.status).toHaveBeenCalledTimes(4)
    admin.settings = { registration_enabled: true, task_retention: policy(false, 30, 2) }
    await nextTick()
    await vi.advanceTimersByTimeAsync(120_000)
    expect(mocks.status).toHaveBeenCalledTimes(4)
  })
  it('clears caches and stops reads on 403 and role loss', async () => {
    mocks.status.mockRejectedValue(new ApiError('secret', 403))
    const { controller, authorized } = setup()
    controller.updateDraft({ retention_days: 7 })
    controller.start()
    await nextTick()
    expect(admin.clearSettings).toHaveBeenCalled()
    expect(controller.draft.value).toBeNull()
    expect(controller.status.value).toBeNull()
    await vi.advanceTimersByTimeAsync(120_000)
    expect(mocks.status).toHaveBeenCalledTimes(1)
    authorized.value = false
    await controller.readPreview()
    expect(mocks.preview).not.toHaveBeenCalled()
  })
  it('does not request administrator endpoints for ordinary users or after settings access is denied', async () => {
    const { controller, authorized } = setup()
    authorized.value = false
    controller.start()
    await controller.readPreview()
    expect(mocks.status).not.toHaveBeenCalled()
    expect(mocks.preview).not.toHaveBeenCalled()
    expect(controller.draft.value).toBeNull()
  })
  it('can adopt a new confirmed settings read after access denial without restoring the old draft', async () => {
    const { controller } = setup()
    controller.updateDraft({ retention_days: 7 })
    admin.clearSettings(true)
    expect(controller.revoked.value).toBe(true)
    controller.start()
    expect(mocks.status).not.toHaveBeenCalled()
    admin.settings = { registration_enabled: true, task_retention: policy(false, 45, 2) }
    admin.settingsAccessDenied = false
    controller.resetDraft()
    controller.start()
    await nextTick()
    expect(controller.revoked.value).toBe(false)
    expect(controller.draft.value?.retention_days).toBe(45)
    expect(mocks.status).toHaveBeenCalledTimes(1)
  })
  it('rejects an in-flight status response after visibility changes', async () => {
    let resolve!: (value: ApiSchemas['TaskRetentionStatus']) => void
    mocks.status.mockImplementation(
      () =>
        new Promise<ApiSchemas['TaskRetentionStatus']>((yes) => {
          resolve = yes
        }),
    )
    const { controller } = setup()
    controller.start()
    doc.visibilityState = 'hidden'
    doc.dispatchEvent(new Event('visibilitychange'))
    resolve({
      task_retention: policy(true, 1, 10),
      policy_revision: 10,
      state: 'running',
      reason_codes: [],
      last_scan: null,
      backlog: null,
    })
    await nextTick()
    expect(controller.status.value).toBeNull()
    expect(admin.settings?.task_retention.revision).toBe(1)
    expect(controller.statusLoading.value).toBe(false)
  })
})
