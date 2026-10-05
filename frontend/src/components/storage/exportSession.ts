import { computed, ref, shallowRef } from 'vue'
import type { ApiSchemas } from '@/api/client-core'
import * as api from '@/api/storage'
import { captureSession, isSessionCurrent } from '@/api/session-context'
import { isAccessDenied } from '@/api/utils'
import { safeStorageProblem, storageErrorMessage, storageResultUnknown } from '@/api/storage-errors'
import {
  getStorageContractGate,
  isSafeStorageId,
  storageTaskCommitted,
} from '@/utils/storage-contract'
import { t } from '@/i18n'

type Task = ApiSchemas['StorageTask']
export type ExportDeletion = {
  artifactId: number
  taskId: number | null
  task: Task | null
  status: 'awaiting_tombstone' | 'pending' | 'blocked' | 'done' | 'unknown'
  error: string | null
}
type Subscribe = (
  projectId: number,
  taskId: number,
  receive: (task: Task) => void,
  fail: (error: unknown) => void,
) => () => void
export function createExportSession(
  options: {
    projectId: number
    resourceId: number
    beforeSavedContent?: () => Promise<boolean>
    beforeMutation?: () => Promise<boolean>
    subscribe: Subscribe
    changed?: () => void
    canMutate?: () => boolean
  },
  transport = api,
  contractReady: () => boolean = () => getStorageContractGate('exportMutation').available,
) {
  const items = shallowRef<ApiSchemas['ExportArtifact'][]>([])
  const loading = ref(false),
    loaded = ref(false),
    busy = ref(false),
    unknown = ref(false)
  const error = ref<string | null>(null)
  const task = shallowRef<Task | null>(null)
  const key = ref<string | null>(null)
  const deletions = ref<Record<number, ExportDeletion>>({})
  const published = computed(() => confirmedArtifactId.value !== undefined)
  let epoch = 0,
    readSequence = 0
  let sourceArtifactId: number | undefined
  const confirmedArtifactId = ref<number | undefined>(undefined)
  let operationSequence = 0
  const trustedArtifacts = shallowRef<ReadonlySet<number> | null>(null)
  let unsubscribe: (() => void) | undefined
  const deletionSubscriptions = new Map<number, () => void>()
  const deletionVersions = new Map<number, symbol>()
  let recoveryTaskId: number | null = null
  const canMutate = () =>
    !controller.signal.aborted && contractReady() && (options.canMutate?.() ?? true)
  const controller = new AbortController()
  const context = () => {
    const session = captureSession(),
      version = epoch
    return () => version === epoch && !controller.signal.aborted && isSessionCurrent(session)
  }
  async function refresh() {
    if (controller.signal.aborted) return false
    const contextCurrent = context(),
      sequence = ++readSequence
    const current = () => contextCurrent() && sequence === readSequence
    loading.value = true
    error.value = null
    try {
      const result = await transport.listExportArtifacts(options.projectId, options.resourceId, {
        signal: controller.signal,
        includeDeleted: true,
      })
      if (current()) {
        items.value = result.items
        loaded.value = true
        for (const item of result.items) {
          if (item.status !== 'deleted') continue
          deletions.value[item.id] ??= {
            artifactId: item.id,
            taskId: null,
            task: null,
            status: 'awaiting_tombstone',
            error: null,
          }
          const deletion = deletions.value[item.id]!
          if (!isSafeStorageId(item.deletion_task_id)) continue
          deletion.taskId = item.deletion_task_id
          if (deletion.status === 'unknown') {
            deletionSubscriptions.get(item.deletion_task_id)?.()
            deletionSubscriptions.delete(item.deletion_task_id)
          }
          if (deletionSubscriptions.has(item.deletion_task_id)) continue
          deletion.status = 'pending'
          const taskId = item.deletion_task_id
          const version = Symbol()
          deletionVersions.set(taskId, version)
          deletionSubscriptions.set(
            taskId,
            options.subscribe(
              options.projectId,
              taskId,
              (value) => {
                if (
                  !contextCurrent() ||
                  deletionVersions.get(taskId) !== version ||
                  value.id !== taskId ||
                  value.project_id !== options.projectId ||
                  value.kind !== 'export_delete' ||
                  value.result_artifact_id !== item.id
                )
                  return
                deletion.task = value
                deletion.status =
                  value.cleanup_status === 'done' && storageTaskCommitted(value)
                    ? 'done'
                    : value.cleanup_status === 'blocked'
                      ? 'blocked'
                      : 'pending'
                deletion.error = null
              },
              (cause) => {
                if (!contextCurrent() || deletionVersions.get(taskId) !== version) return
                if (isAccessDenied(cause)) {
                  revokeAccess()
                  return
                }
                deletion.error = storageErrorMessage(cause)
                deletion.status = 'unknown'
              },
            ),
          )
        }
      }
      return current()
    } catch (cause) {
      if (current()) {
        if (isAccessDenied(cause)) revokeAccess()
        error.value = storageErrorMessage(cause)
      }
      return false
    } finally {
      if (current()) loading.value = false
    }
  }
  async function receive(value: Task, operation: number) {
    if (operation !== operationSequence || !task.value || task.value.id !== value.id) return
    if (
      value.project_id !== options.projectId ||
      value.kind !== 'export' ||
      (value.resource_id != null && value.resource_id !== options.resourceId)
    ) {
      unknown.value = true
      error.value = t('storageManagement.exportIdentityMismatch')
      return
    }
    task.value = value
    if (!storageTaskCommitted(value)) {
      if (value.status === 'completed') {
        unknown.value = true
        error.value = t('storageManagement.exportNotCommitted')
      }
      return
    }
    unknown.value = true
    const current = context()
    const refreshed = await refresh()
    if (!current() || !refreshed || operation !== operationSequence || task.value?.id !== value.id)
      return
    // A newly produced artifact needs both this task's result identity and ready metadata.
    const artifact = items.value.find((item) => item.id === value.result_artifact_id)
    if (
      !isSafeStorageId(value.result_artifact_id) ||
      !artifact ||
      artifact.status !== 'ready' ||
      artifact.id === sourceArtifactId
    ) {
      error.value = t('storage.resultNotReady')
      return
    }
    unknown.value = false
    if (confirmedArtifactId.value !== artifact.id) {
      confirmedArtifactId.value = artifact.id
      trustedArtifacts.value = new Set([...(trustedArtifacts.value ?? []), artifact.id])
      options.changed?.()
    }
  }
  async function start(artifactId?: number): Promise<boolean> {
    if (
      !canMutate() ||
      busy.value ||
      unknown.value ||
      (task.value && !['completed', 'failed', 'cancelled'].includes(task.value.status))
    )
      return false
    if (
      artifactId !== undefined &&
      !items.value.some(
        (item) => item.id === artifactId && item.rebuildable && item.status !== 'deleted',
      )
    )
      return false
    const current = context()
    busy.value = true
    error.value = null
    let submitted = false
    try {
      if (
        artifactId === undefined &&
        options.beforeSavedContent &&
        !(await options.beforeSavedContent())
      )
        return false
      if (!current() || !canMutate()) return false
      if (options.beforeMutation && !(await options.beforeMutation())) return false
      if (!current() || !canMutate()) return false
      key.value = crypto.randomUUID()
      const operation = ++operationSequence
      // Fence the previous task before the new POST can wait or lose its response.
      unsubscribe?.()
      unsubscribe = undefined
      ++readSequence
      loading.value = false
      task.value = null
      recoveryTaskId = null
      // Only the original history and individually confirmed results are trusted.
      // Starting another task must not promote an unconfirmed ready item to history.
      trustedArtifacts.value ??= new Set(items.value.map((item) => item.id))
      sourceArtifactId = artifactId
      confirmedArtifactId.value = undefined
      submitted = true
      const value = await replay()
      if (!current()) return false
      if (!isSafeStorageId(value.id)) {
        unknown.value = true
        error.value = t('storage.resultUnknown')
        return false
      }
      task.value = value
      recoveryTaskId = value.id
      unsubscribe = options.subscribe(
        options.projectId,
        value.id,
        (result) => {
          if (current() && operation === operationSequence) void receive(result, operation)
        },
        (cause) => {
          if (current() && operation === operationSequence) {
            if (isAccessDenied(cause)) {
              revokeAccess()
              return
            }
            unknown.value = true
            error.value = storageErrorMessage(cause)
          }
        },
      )
      void receive(value, operation)
      return true
    } catch (cause) {
      if (current()) {
        if (isAccessDenied(cause)) {
          revokeAccess()
          error.value = storageErrorMessage(cause)
          return false
        }
        if (!submitted) {
          error.value = storageErrorMessage(cause)
          return false
        }
        recoveryTaskId = safeStorageProblem(cause).task_id ?? null
        unknown.value = storageResultUnknown(cause) || recoveryTaskId !== null
        error.value = storageErrorMessage(cause)
      }
      return false
    } finally {
      if (current()) busy.value = false
    }
  }
  function replay() {
    // Replay the exact original input snapshot. Never save drafts or mint another key here.
    if (!key.value) throw new Error('Missing export operation key')
    return sourceArtifactId === undefined
      ? transport.createExportArtifact(options.projectId, options.resourceId, key.value, {
          signal: controller.signal,
        })
      : transport.rebuildExportArtifact(options.projectId, sourceArtifactId, key.value, {
          signal: controller.signal,
        })
  }
  async function recover(): Promise<boolean> {
    if (busy.value) return false
    if (!key.value) return refresh()
    const current = context(),
      operation = operationSequence
    if (!current() || (!recoveryTaskId && !canMutate())) return false
    busy.value = true
    error.value = null
    try {
      if (!recoveryTaskId && options.beforeMutation && !(await options.beforeMutation()))
        return false
      if (!current() || (!recoveryTaskId && !canMutate())) return false
      const value = recoveryTaskId
        ? await transport.getStorageTask(options.projectId, recoveryTaskId, {
            signal: controller.signal,
          })
        : await replay()
      if (!current() || operation !== operationSequence) return false
      if (!isSafeStorageId(value.id) || (recoveryTaskId !== null && recoveryTaskId !== value.id)) {
        unknown.value = true
        error.value = t('storageManagement.exportIdentityMismatch')
        return false
      }
      recoveryTaskId = value.id
      task.value = value
      unknown.value = false
      unsubscribe?.()
      unsubscribe = options.subscribe(
        options.projectId,
        value.id,
        (result) => {
          if (current() && operation === operationSequence) void receive(result, operation)
        },
        (cause) => {
          if (current() && operation === operationSequence) {
            if (isAccessDenied(cause)) {
              revokeAccess()
              return
            }
            unknown.value = true
            error.value = storageErrorMessage(cause)
          }
        },
      )
      await receive(value, operation)
      return current() && !unknown.value
    } catch (cause) {
      if (current()) {
        if (isAccessDenied(cause)) {
          revokeAccess()
          error.value = storageErrorMessage(cause)
          return false
        }
        recoveryTaskId ??= safeStorageProblem(cause).task_id ?? null
        unknown.value = true
        error.value = storageErrorMessage(cause)
      }
      return false
    } finally {
      if (current()) busy.value = false
    }
  }
  async function remove(id: number): Promise<boolean> {
    if (
      !canMutate() ||
      busy.value ||
      unknown.value ||
      deletions.value[id] !== undefined ||
      !items.value.some((item) => item.id === id && item.status !== 'deleted')
    )
      return false
    const current = context()
    busy.value = true
    error.value = null
    try {
      if (options.beforeMutation && !(await options.beforeMutation())) return false
      if (!current() || !canMutate()) return false
      deletions.value[id] = {
        artifactId: id,
        taskId: null,
        task: null,
        status: 'awaiting_tombstone',
        error: null,
      }
      await transport.deleteExportArtifact(options.projectId, id, { signal: controller.signal })
      if (!current()) return false
      await refresh()
      if (current()) options.changed?.()
      return current()
    } catch (cause) {
      if (current()) {
        if (isAccessDenied(cause)) {
          revokeAccess()
          error.value = storageErrorMessage(cause)
          return false
        }
        error.value = storageErrorMessage(cause)
        if (storageResultUnknown(cause) && deletions.value[id]) {
          deletions.value[id]!.status = 'unknown'
          deletions.value[id]!.error = error.value
          await refresh()
        } else delete deletions.value[id]
      }
      return false
    } finally {
      if (current()) busy.value = false
    }
  }
  function dispose() {
    ++epoch
    controller.abort()
    unsubscribe?.()
    for (const stop of deletionSubscriptions.values()) stop()
    deletionSubscriptions.clear()
    deletionVersions.clear()
    items.value = []
    task.value = null
    key.value = null
    deletions.value = {}
    recoveryTaskId = null
  }
  function revokeAccess() {
    dispose()
    busy.value = loading.value = unknown.value = false
    loaded.value = false
    confirmedArtifactId.value = undefined
    trustedArtifacts.value = null
  }
  function canDownload(item: ApiSchemas['ExportArtifact']): boolean {
    return (
      item.status === 'ready' &&
      !controller.signal.aborted &&
      !deletions.value[item.id] &&
      (trustedArtifacts.value === null || trustedArtifacts.value.has(item.id))
    )
  }
  return {
    items,
    loading,
    loaded,
    busy,
    unknown,
    error,
    task,
    key,
    published,
    deletions,
    refresh,
    start,
    recover,
    remove,
    dispose,
    revokeAccess,
    canDownload,
  }
}
