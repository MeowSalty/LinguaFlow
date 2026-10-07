import { computed, onScopeDispose, shallowRef, watch } from 'vue'
import type { ApiSchemas } from '@/api/client-core'
import { captureSession, isSessionCurrent, onSessionChange } from '@/api/session-context'
import {
  fetchTaskRetentionPreview,
  fetchTaskRetentionStatus,
  taskHistoryErrorMessage,
} from '@/api/task-history'
import { ApiError } from '@/api/utils'
import { useAdminStore } from '@/stores/admin'
import { t } from '@/i18n'

type Policy = ApiSchemas['TaskRetentionPolicy']
type Preview = ApiSchemas['TaskRetentionPreview']
type Status = ApiSchemas['TaskRetentionStatus']
type Patch = ApiSchemas['TaskRetentionPatch']
export type RetentionDraft = { enabled: boolean; retention_days: number | null }
export const validRetentionDays = (days: unknown): days is number =>
  typeof days === 'number' && Number.isInteger(days) && days >= 1 && days <= 3650
export const expandsRetention = (draft: RetentionDraft, baseline: Policy): boolean =>
  draft.enabled &&
  (!baseline.enabled || (draft.retention_days ?? Infinity) < baseline.retention_days)

/** Page-scoped policy draft and read lifecycle. Confirmations always bind an immutable revision. */
export function useTaskRetention(authorized: () => boolean) {
  const admin = useAdminStore()
  const draft = shallowRef<RetentionDraft | null>(null)
  const baseline = shallowRef<Policy | null>(null)
  const preview = shallowRef<Preview | null>(null)
  const previewLoading = shallowRef(false)
  const previewError = shallowRef<string | null>(null)
  const status = shallowRef<Status | null>(null)
  const statusLoading = shallowRef(false)
  const statusError = shallowRef<string | null>(null)
  const conflict = shallowRef(false)
  const conflictReadFailed = shallowRef(false)
  const preparing = shallowRef(false)
  const submitting = shallowRef(false)
  const confirmation = shallowRef<{ patch: Patch; baseline: Policy; preview: Preview } | null>(null)
  const partialAccepted = shallowRef(false)
  const notice = shallowRef<string | null>(null)
  const revoked = shallowRef(false)
  const active = shallowRef(false)
  let alive = true
  let previewGeneration = 0
  let statusGeneration = 0
  let editGeneration = 0
  let previewController: AbortController | null = null
  let statusController: AbortController | null = null
  let timer: ReturnType<typeof setTimeout> | null = null
  let failures = 0
  const policy = computed(() => admin.settings?.task_retention ?? null)
  const hasChanges = computed(
    () =>
      !!draft.value &&
      !!baseline.value &&
      (draft.value.enabled !== baseline.value.enabled ||
        draft.value.retention_days !== baseline.value.retention_days),
  )
  const valid = computed(() => !!draft.value && validRetentionDays(draft.value.retention_days))
  const busy = computed(
    () => admin.settingsLoading || admin.settingsSaving || preparing.value || submitting.value,
  )
  const visible = () => typeof document === 'undefined' || document.visibilityState !== 'hidden'
  const allowed = () => alive && authorized() && !revoked.value
  const canSave = computed(
    () => allowed() && valid.value && hasChanges.value && !busy.value && !conflict.value,
  )

  function cancelConfirmation(showNotice = false) {
    if (showNotice && confirmation.value) notice.value = t('taskRetention.confirmationInvalid')
    confirmation.value = null
    partialAccepted.value = false
    ++editGeneration
    preparing.value = false
  }
  function invalidatePreview() {
    ++previewGeneration
    previewController?.abort()
    previewController = null
    preview.value = null
    previewLoading.value = false
    previewError.value = null
  }
  function stopStatus() {
    if (timer !== null) clearTimeout(timer)
    timer = null
    ++statusGeneration
    statusController?.abort()
    statusController = null
    statusLoading.value = false
  }
  function resetDraft() {
    if (!authorized() || admin.settingsAccessDenied) return
    revoked.value = false
    cancelConfirmation()
    invalidatePreview()
    baseline.value = policy.value ? { ...policy.value } : null
    draft.value = policy.value
      ? { enabled: policy.value.enabled, retention_days: policy.value.retention_days }
      : null
    conflict.value = false
    conflictReadFailed.value = false
    notice.value = null
  }
  function clear() {
    stopStatus()
    cancelConfirmation()
    invalidatePreview()
    draft.value = null
    baseline.value = null
    status.value = null
    statusError.value = null
    conflict.value = false
    conflictReadFailed.value = false
    notice.value = null
    submitting.value = false
  }
  function deny(error: unknown) {
    if (!(error instanceof ApiError) || ![401, 403].includes(error.status ?? 0)) return false
    revoked.value = true
    clear()
    admin.clearSettings(true)
    return true
  }

  watch(
    policy,
    (next) => {
      if (!next) {
        clear()
        return
      }
      if (!allowed()) return
      if (!baseline.value || !draft.value) {
        resetDraft()
        return
      }
      if (next.revision === baseline.value.revision) return
      if (next.revision < baseline.value.revision) return
      cancelConfirmation(true)
      invalidatePreview()
      if (hasChanges.value) conflict.value = true
      else resetDraft()
    },
    { immediate: true, flush: 'sync' },
  )

  function updateDraft(patch: Partial<RetentionDraft>) {
    if (!draft.value || busy.value) return
    const previous = draft.value
    draft.value = { ...previous, ...patch }
    cancelConfirmation(true)
    if (draft.value.retention_days !== previous.retention_days) invalidatePreview()
    notice.value = null
  }
  async function reloadConflict() {
    if (!allowed() || admin.settingsLoading || admin.settingsSaving) return false
    const session = captureSession()
    const loaded = await admin.loadSettings()
    if (!alive || !isSessionCurrent(session)) return false
    if (!admin.settings) {
      clear()
      return false
    }
    conflictReadFailed.value = !loaded
    return loaded
  }
  function useLatest() {
    if (!policy.value || conflictReadFailed.value || busy.value) return
    baseline.value = { ...policy.value }
    conflict.value = false
    cancelConfirmation()
    invalidatePreview()
  }
  async function readPreview(): Promise<Preview | null> {
    const days = draft.value?.retention_days
    const revision = baseline.value?.revision
    if (!allowed() || !validRetentionDays(days) || !revision || conflict.value) return null
    invalidatePreview()
    const request = previewGeneration
    const session = captureSession()
    previewController = new AbortController()
    previewLoading.value = true
    const current = () =>
      allowed() &&
      request === previewGeneration &&
      isSessionCurrent(session) &&
      draft.value?.retention_days === days &&
      baseline.value?.revision === revision
    try {
      const result = await fetchTaskRetentionPreview(days, { signal: previewController.signal })
      if (!current()) return null
      if (result.policy_revision !== revision) {
        conflict.value = true
        cancelConfirmation(true)
        await reloadConflict()
        return null
      }
      if (result.retention_days !== days) throw new Error('Invalid preview days')
      preview.value = result
      return result
    } catch (error) {
      if (!current()) return null
      if (!deny(error)) previewError.value = taskHistoryErrorMessage(error)
      return null
    } finally {
      if (current()) previewLoading.value = false
    }
  }
  function scheduleStatus() {
    if (timer !== null) clearTimeout(timer)
    timer = null
    if (!allowed() || !active.value || !visible() || !policy.value?.enabled) return
    const interval = status.value?.state === 'running' ? 10_000 : 30_000
    timer = setTimeout(
      () => {
        timer = null
        void refreshStatus()
      },
      Math.min(interval * 2 ** failures, 120_000),
    )
  }
  async function refreshStatus(force = false) {
    if (!allowed() || !active.value || !visible() || (statusLoading.value && !force)) return
    stopStatus()
    const request = statusGeneration
    const session = captureSession()
    statusController = new AbortController()
    statusLoading.value = true
    const current = () =>
      allowed() &&
      active.value &&
      visible() &&
      request === statusGeneration &&
      isSessionCurrent(session)
    try {
      const result = await fetchTaskRetentionStatus({ signal: statusController.signal })
      if (!current()) return
      if (policy.value && result.task_retention.revision < policy.value.revision) return
      admin.acceptRetentionPolicy(result.task_retention)
      status.value = result
      statusError.value = null
      failures = 0
    } catch (error) {
      if (!current()) return
      if (!deny(error)) {
        statusError.value = t('taskRetention.statusFailed')
        failures++
      }
    } finally {
      if (current()) {
        statusLoading.value = false
        scheduleStatus()
      }
    }
  }
  async function submit(patch: Patch) {
    if (
      !allowed() ||
      submitting.value ||
      admin.settingsSaving ||
      admin.settingsLoading ||
      conflict.value ||
      baseline.value?.revision !== patch.expected_revision
    )
      return false
    const session = captureSession()
    submitting.value = true
    const hadPreview = preview.value !== null
    const result = await admin.saveTaskRetention(patch)
    if (!alive || !isSessionCurrent(session)) return false
    submitting.value = false
    confirmation.value = null
    partialAccepted.value = false
    if (result === 'saved') {
      resetDraft()
      void refreshStatus(true)
      if (hadPreview) void readPreview()
      return true
    }
    if (result === 'conflict') {
      conflict.value = true
      await reloadConflict()
    }
    if (!admin.settings) clear()
    return false
  }
  async function save(): Promise<boolean> {
    if (
      !canSave.value ||
      !draft.value ||
      !baseline.value ||
      !validRetentionDays(draft.value.retention_days)
    )
      return false
    const patch: Patch = {
      enabled: draft.value.enabled,
      retention_days: draft.value.retention_days,
      expected_revision: baseline.value.revision,
    }
    const original = { ...baseline.value }
    if (!expandsRetention(draft.value, original)) return submit(patch)
    preparing.value = true
    const generation = ++editGeneration
    const result = await readPreview()
    if (generation !== editGeneration || !allowed()) return false
    preparing.value = false
    if (result && !conflict.value)
      confirmation.value = { patch, baseline: original, preview: result }
    return false
  }
  async function confirmSave(): Promise<boolean> {
    const value = confirmation.value
    if (!value || (value.preview.partial && !partialAccepted.value)) return false
    if (
      value.patch.expected_revision !== policy.value?.revision ||
      draft.value?.enabled !== value.patch.enabled ||
      draft.value?.retention_days !== value.patch.retention_days
    ) {
      cancelConfirmation(true)
      return false
    }
    return submit({ ...value.patch })
  }
  function onVisibilityChange() {
    if (!visible()) {
      stopStatus()
      invalidatePreview()
      cancelConfirmation(true)
    } else void refreshStatus()
  }
  function start() {
    active.value = true
    void refreshStatus(true)
  }
  watch(
    authorized,
    (value) => {
      if (!value) {
        revoked.value = true
        clear()
        admin.clearSettings()
      }
    },
    { flush: 'sync' },
  )
  watch(
    () => admin.settingsAccessDenied,
    (value) => {
      if (value) {
        revoked.value = true
        clear()
      }
    },
    { immediate: true, flush: 'sync' },
  )
  watch(() => policy.value?.enabled, scheduleStatus)
  const stopSession = onSessionChange(() => {
    revoked.value = true
    clear()
  })
  if (typeof document !== 'undefined')
    document.addEventListener('visibilitychange', onVisibilityChange)
  onScopeDispose(() => {
    alive = false
    clear()
    stopSession()
    if (typeof document !== 'undefined')
      document.removeEventListener('visibilitychange', onVisibilityChange)
  })
  return {
    draft,
    baseline,
    policy,
    preview,
    previewLoading,
    previewError,
    status,
    statusLoading,
    statusError,
    conflict,
    conflictReadFailed,
    preparing,
    submitting,
    confirmation,
    partialAccepted,
    notice,
    revoked,
    hasChanges,
    valid,
    busy,
    canSave,
    resetDraft,
    updateDraft,
    reloadConflict,
    useLatest,
    readPreview,
    refreshStatus,
    save,
    confirmSave,
    cancelConfirmation,
    start,
  }
}

export type TaskRetentionController = ReturnType<typeof useTaskRetention>
