import { computed, onScopeDispose, shallowRef } from 'vue'
import type { ApiSchemas } from '@/api/client'
import { previewSourceUpdate, commitSourceUpdate, getStorageTask } from '@/api/storage'
import {
  safeStorageProblem,
  storageAdmissionBlocked,
  storageAccessDenied,
  storageNeedsRefresh,
  storageResultUnknown,
  type StorageProblem,
} from '@/api/storage-errors'
import { invalidateStorageSnapshots, subscribeStorageRefresh } from '@/utils/storage-snapshots'
import {
  captureSession,
  isSessionCurrent,
  onSessionChange,
  type SessionSnapshot,
} from '@/api/session-context'
import {
  getStorageContractGate,
  isSafeStorageId,
  isStorageGeneration,
  storageTaskCommitted,
} from '@/utils/storage-contract'

type Preview = ApiSchemas['SourceUpdatePreview']
type Task = ApiSchemas['StorageTask']
export type SourceUpdateState =
  | 'idle'
  | 'selected'
  | 'previewing'
  | 'preview_ready'
  | 'submitting'
  | 'tracking'
  | 'invalidated'
  | 'unknown'
  | 'failed'
  | 'completed'
  | 'cancelled'
  | 'expired'
  | 'read_only'
  | 'blocked'
export interface SourceUpdatePublication {
  taskId: number
  projectId: number
  resourceId: number
  revisionId: number
}
interface SourceUpdateDependencies {
  available: () => boolean
  beforeSavedContent: () => Promise<boolean>
  preview: typeof previewSourceUpdate
  commit: typeof commitSourceUpdate
  task: typeof getStorageTask
}
const validExpiry = (value: unknown): boolean =>
  value === null || (typeof value === 'string' && Number.isFinite(Date.parse(value)))
/** Missing fields are not zero; legacy tasks cannot manufacture a fixed plan. */
export const isSourceUpdatePreview = (value: Preview | undefined | null): value is Preview =>
  !!value &&
  isSafeStorageId(value.task_id) &&
  isStorageGeneration(value.source_generation) &&
  isStorageGeneration(value.translation_generation) &&
  validExpiry(value.expires_at) &&
  !!value.stats &&
  ['added', 'updated', 'deleted', 'unchanged'].every((key) =>
    isStorageGeneration(value.stats[key as keyof Preview['stats']]),
  )
const clonePreview = (value: Preview): Preview => ({ ...value, stats: { ...value.stats } })
const failureState = (error: unknown): SourceUpdateState => {
  if (storageAdmissionBlocked(error)) return 'blocked'
  const problem = safeStorageProblem(error)
  if (problem.error_code === 'source_revision_conflict') return 'invalidated'
  if (problem.error_code === 'storage_intent_expired') return 'expired'
  if (problem.error_code === 'storage_cancelled') return 'cancelled'
  if (problem.error_code === 'storage_operation_in_progress') return 'unknown'
  const status =
    typeof error === 'object' && error !== null && 'status' in error ? error.status : undefined
  if (status === 409 && !problem.error_code) return 'unknown'
  if (typeof status === 'number' && status >= 400 && status < 500 && status !== 408) return 'failed'
  return storageResultUnknown(error) ? 'unknown' : 'failed'
}

export function createSourceUpdateSession(deps: SourceUpdateDependencies) {
  const state = shallowRef<SourceUpdateState>('idle')
  const file = shallowRef<File | null>(null)
  const preview = shallowRef<Preview | null>(null)
  const task = shallowRef<Task | null>(null)
  const key = shallowRef<string | null>(null)
  const taskId = shallowRef<number | null>(null)
  const operationId = shallowRef<string | null>(null)
  const projectId = shallowRef<number | null>(null)
  const resourceId = shallowRef<number | null>(null)
  const published = shallowRef<SourceUpdatePublication | null>(null)
  const lastError = shallowRef<StorageProblem | null>(null)
  const refreshError = shallowRef(false)
  const recovering = shallowRef(false)
  const taskSnapshotReady = shallowRef(false)
  const expiresAt = computed(() =>
    task.value ? task.value.expires_at : (preview.value?.expires_at ?? null),
  )
  let round = 0
  let refreshRound = 0
  let ownerSession: SessionSnapshot | null = null
  let controller = new AbortController()
  let confirmationInvalidated = false
  let previewAttempted = false

  const reset = () => {
    round++
    refreshRound++
    controller.abort()
    controller = new AbortController()
    file.value = null
    preview.value = null
    task.value = null
    key.value = null
    taskId.value = null
    operationId.value = null
    projectId.value = null
    resourceId.value = null
    published.value = null
    lastError.value = null
    refreshError.value = false
    recovering.value = false
    taskSnapshotReady.value = false
    confirmationInvalidated = false
    previewAttempted = false
    ownerSession = null
    state.value = 'idle'
  }
  const ownsCurrentSession = (): boolean => {
    if (ownerSession && isSessionCurrent(ownerSession)) return true
    reset()
    return false
  }
  const resolveDrafts = async (): Promise<boolean> => {
    try {
      return await deps.beforeSavedContent()
    } catch {
      return false
    }
  }
  const recordFailure = (error: unknown) => {
    if (storageAccessDenied(error)) {
      reset()
      return
    }
    const problem = safeStorageProblem(error)
    lastError.value = problem
    // A failure cannot replace an already established operation identity.
    if (problem.task_id && !taskId.value) taskId.value = problem.task_id
    if (problem.operation_id && !operationId.value) operationId.value = problem.operation_id
    state.value = failureState(error)
    if (storageNeedsRefresh(error) && projectId.value) {
      taskSnapshotReady.value = false
      invalidateStorageSnapshots({ projectId: projectId.value })
    }
    if (state.value === 'invalidated') confirmationInvalidated = true
  }
  const ownsTask = (value: Task): boolean =>
    isSafeStorageId(value.id) &&
    value.kind === 'source_update' &&
    value.project_id === projectId.value &&
    value.resource_id === resourceId.value &&
    (taskId.value === null || value.id === taskId.value) &&
    (operationId.value === null || value.operation_id === operationId.value)
  const acceptTask = (value: Task): boolean => {
    if (!ownsTask(value)) {
      refreshError.value = true
      return false
    }
    if (
      published.value &&
      (!storageTaskCommitted(value) ||
        value.result_resource_id !== published.value.resourceId ||
        value.result_revision_id !== published.value.revisionId)
    ) {
      refreshError.value = true
      return false
    }
    task.value = { ...value }
    taskSnapshotReady.value = true
    taskId.value = value.id
    operationId.value = value.operation_id
    refreshError.value = false
    lastError.value = value.error_code
      ? { error_code: value.error_code }
      : storageAdmissionBlocked(lastError.value) &&
          !storageTaskCommitted(value) &&
          !value.allowed_actions.includes('commit')
        ? lastError.value
        : null
    if (storageTaskCommitted(value)) {
      if (
        value.result_resource_id === resourceId.value &&
        isSafeStorageId(value.result_revision_id)
      ) {
        published.value ??= {
          taskId: value.id,
          projectId: projectId.value!,
          resourceId: resourceId.value!,
          revisionId: value.result_revision_id,
        }
        state.value = 'completed'
      } else state.value = 'read_only'
      return true
    }
    if (value.status === 'cancelled' || value.error_code === 'storage_cancelled') {
      state.value = 'cancelled'
      return true
    }
    if (value.error_code === 'storage_intent_expired') {
      state.value = 'expired'
      return true
    }
    if (value.error_code === 'source_revision_conflict') {
      confirmationInvalidated = true
      state.value = 'invalidated'
      return true
    }
    if (storageAdmissionBlocked(lastError.value) && !value.allowed_actions.includes('commit')) {
      if (isSourceUpdatePreview(value.source_preview) && value.source_preview.task_id === value.id)
        preview.value = clonePreview(value.source_preview)
      state.value = 'blocked'
      return true
    }
    if (value.status === 'failed') {
      state.value = 'failed'
      return true
    }
    // completed/prepared is not publication proof or a writable confirmation.
    if (value.status === 'completed') {
      state.value = 'read_only'
      return true
    }
    const frozen = value.source_preview
    if (value.phase === 'prepared') {
      if (
        isSourceUpdatePreview(frozen) &&
        frozen.task_id === value.id &&
        isStorageGeneration(value.expected_storage_generation) &&
        frozen.source_generation === value.expected_source_generation &&
        frozen.translation_generation === value.expected_translation_generation &&
        validExpiry(value.expires_at)
      ) {
        preview.value = clonePreview(frozen)
        state.value = confirmationInvalidated
          ? 'invalidated'
          : value.allowed_actions.includes('commit')
            ? 'preview_ready'
            : 'read_only'
      } else state.value = 'read_only'
    } else state.value = 'tracking'
    return true
  }
  const select = (project: number, resource: number, candidate: File): boolean => {
    if (!isSafeStorageId(project) || !isSafeStorageId(resource)) return false
    if (ownerSession && !isSessionCurrent(ownerSession)) reset()
    if (recovering.value || ['submitting', 'tracking', 'unknown', 'blocked'].includes(state.value))
      return false
    reset()
    projectId.value = project
    resourceId.value = resource
    ownerSession = captureSession()
    file.value = candidate
    key.value = crypto.randomUUID()
    state.value = 'selected'
    return true
  }
  const newPreview = (): boolean => {
    if (!ownsCurrentSession() || !file.value || !projectId.value || !resourceId.value) return false
    return select(projectId.value, resourceId.value, file.value)
  }
  const invalidate = (): void => {
    confirmationInvalidated = true
    if (state.value === 'preview_ready') state.value = 'invalidated'
  }
  const requestPreview = async (replay: boolean): Promise<boolean> => {
    if (
      !ownsCurrentSession() ||
      !deps.available() ||
      !file.value ||
      !key.value ||
      !projectId.value ||
      !resourceId.value
    )
      return false
    const version = round
    const session = captureSession()
    const previousState = state.value
    state.value = 'previewing'
    const current = () => version === round && isSessionCurrent(session)
    // Recovery replays the frozen input; saving drafts cannot change that plan.
    if (!replay && !(await resolveDrafts())) {
      if (current()) state.value = previousState
      return false
    }
    if (!current()) return false
    if (!deps.available()) {
      state.value = previousState
      return false
    }
    if (!replay) confirmationInvalidated = false
    previewAttempted = true
    lastError.value = null
    try {
      const result = await deps.preview(
        projectId.value!,
        resourceId.value!,
        file.value!,
        key.value!,
        {
          signal: controller.signal,
        },
      )
      if (!current()) return false
      if (
        !isSourceUpdatePreview(result) ||
        (taskId.value !== null && taskId.value !== result.task_id)
      ) {
        if (isSafeStorageId(result.task_id) && !taskId.value) taskId.value = result.task_id
        state.value = 'read_only'
        return false
      }
      taskId.value = result.task_id
      preview.value = clonePreview(result)
      taskSnapshotReady.value = true
      state.value = confirmationInvalidated ? 'invalidated' : 'preview_ready'
      return !confirmationInvalidated
    } catch (error) {
      if (current()) recordFailure(error)
      return false
    }
  }
  const prepare = async (): Promise<boolean> => {
    if (
      !ownsCurrentSession() ||
      recovering.value ||
      !['selected', 'failed', 'invalidated', 'cancelled', 'expired'].includes(state.value)
    )
      return false
    // Only recover() may reuse a key after a request was sent.
    if (previewAttempted && !newPreview()) return false
    return requestPreview(false)
  }
  const confirm = async (): Promise<boolean> => {
    if (
      !ownsCurrentSession() ||
      !deps.available() ||
      recovering.value ||
      state.value !== 'preview_ready' ||
      !taskSnapshotReady.value ||
      (task.value && !task.value.allowed_actions.includes('commit')) ||
      !preview.value ||
      !projectId.value ||
      !resourceId.value
    )
      return false
    const version = round
    const session = captureSession()
    const expected = clonePreview(preview.value)
    state.value = 'submitting'
    if (
      !(await resolveDrafts()) ||
      version !== round ||
      !isSessionCurrent(session) ||
      !deps.available() ||
      !taskSnapshotReady.value ||
      confirmationInvalidated
    ) {
      if (version === round && isSessionCurrent(session))
        state.value = confirmationInvalidated ? 'invalidated' : 'preview_ready'
      return false
    }
    const payload: ApiSchemas['SourceUpdateCommit'] = {
      task_id: expected.task_id,
      expected_source_generation: expected.source_generation,
      expected_translation_generation: expected.translation_generation,
    }
    refreshRound++
    lastError.value = null
    try {
      const result = await deps.commit(projectId.value!, resourceId.value!, payload, {
        signal: controller.signal,
      })
      if (version !== round || !isSessionCurrent(session)) return false
      if (!acceptTask(result)) {
        state.value = 'unknown'
        return false
      }
      return true
    } catch (error) {
      if (version === round && isSessionCurrent(session)) recordFailure(error)
      return false
    }
  }
  const refresh = async (): Promise<boolean> => {
    if (!ownsCurrentSession() || state.value === 'submitting' || !projectId.value || !taskId.value)
      return false
    const version = round
    const request = ++refreshRound
    const session = captureSession()
    taskSnapshotReady.value = false
    try {
      const result = await deps.task(projectId.value, taskId.value, { signal: controller.signal })
      if (version !== round || request !== refreshRound || !isSessionCurrent(session)) return false
      return acceptTask(result)
    } catch (error) {
      if (version === round && request === refreshRound && isSessionCurrent(session)) {
        refreshError.value = true
        lastError.value = safeStorageProblem(error)
      }
      return false
    }
  }
  const recover = async (): Promise<boolean> => {
    if (
      !ownsCurrentSession() ||
      recovering.value ||
      ['previewing', 'submitting'].includes(state.value)
    )
      return false
    recovering.value = true
    const version = round
    try {
      if (taskId.value) return await refresh()
      if (!['unknown', 'blocked'].includes(state.value)) return false
      const prepared = await requestPreview(true)
      if (version !== round || !ownsCurrentSession()) return false
      // Replay responses can describe an already committed original plan.
      if (taskId.value) {
        state.value = 'unknown'
        return await refresh()
      }
      return prepared
    } finally {
      if (version === round) recovering.value = false
    }
  }
  /** Restore server context, never fabricate the key or a missing fixed plan. */
  const restore = (project: number, resource: number, value: Task): boolean => {
    if (
      !isSafeStorageId(project) ||
      !isSafeStorageId(resource) ||
      value.project_id !== project ||
      value.resource_id !== resource ||
      value.kind !== 'source_update' ||
      !isSafeStorageId(value.id)
    )
      return false
    if (ownerSession && !isSessionCurrent(ownerSession)) reset()
    if (
      ['previewing', 'submitting', 'tracking', 'unknown'].includes(state.value) ||
      recovering.value
    )
      return false
    reset()
    projectId.value = project
    resourceId.value = resource
    ownerSession = captureSession()
    return acceptTask(value)
  }
  return {
    state,
    file,
    preview,
    task,
    key,
    taskId,
    operationId,
    projectId,
    resourceId,
    published,
    lastError,
    refreshError,
    recovering,
    taskSnapshotReady,
    invalidateTaskSnapshot: () => {
      ++refreshRound
      taskSnapshotReady.value = false
    },
    expiresAt,
    select,
    newPreview,
    prepare,
    confirm,
    invalidate,
    refresh,
    recover,
    restore,
    reset,
  }
}
export function useSourceUpdateSession(
  beforeSavedContent: () => Promise<boolean>,
  available: () => boolean = () => getStorageContractGate('sourceUpdate').available,
) {
  const session = createSourceUpdateSession({
    available,
    beforeSavedContent,
    preview: previewSourceUpdate,
    commit: commitSourceUpdate,
    task: getStorageTask,
  })
  onScopeDispose(session.reset)
  onScopeDispose(onSessionChange(session.reset))
  onScopeDispose(
    subscribeStorageRefresh({
      scope: () => ({ projectId: session.projectId.value ?? undefined }),
      invalidate: session.invalidateTaskSnapshot,
      refresh: () => (session.taskId.value ? session.refresh() : undefined),
    }),
  )
  return session
}
