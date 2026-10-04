import { computed, ref, shallowRef } from 'vue'
import type { ApiSchemas } from '@/api/client-core'
import * as api from '@/api/storage'
import { captureSession, isSessionCurrent } from '@/api/session-context'
import { safeStorageProblem, storageErrorMessage, storageResultUnknown } from '@/api/storage-errors'
import {
  isSafeStorageId,
  isStorageGeneration,
  storageActionAllowed,
  storageProjectWritable,
  storageTaskCommitted,
} from '@/utils/storage-contract'

export function createMigrationSession(
  options: { project: () => ApiSchemas['Project']; available?: () => boolean },
  transport: Pick<typeof api, 'migrateProjectStorage' | 'getStorageTask'> = api,
) {
  const task = shallowRef<ApiSchemas['StorageTask'] | null>(null)
  const busy = ref(false),
    unknown = ref(false),
    error = ref<string | null>(null),
    hasOperation = ref(false)
  let request: ApiSchemas['StorageMigrationRequest'] | null = null,
    taskId: number | null = null,
    disposed = false
  const controller = new AbortController()
  const projectId = options.project().id
  const allowed = () =>
    !disposed &&
    projectId === options.project().id &&
    (options.available?.() ?? storageActionAllowed(options.project(), 'migration'))
  const canStartNew = computed(
    () =>
      allowed() &&
      !busy.value &&
      !unknown.value &&
      !!task.value &&
      (storageTaskCommitted(task.value) || task.value.status === 'cancelled'),
  )
  function startNew() {
    if (!canStartNew.value) return false
    request = null
    taskId = null
    task.value = null
    error.value = null
    hasOperation.value = false
    return true
  }
  async function run(operation: () => Promise<ApiSchemas['StorageTask']>) {
    if (busy.value || disposed) return false
    const session = captureSession()
    const current = () =>
      !disposed && isSessionCurrent(session) && options.project().id === projectId
    busy.value = true
    error.value = null
    try {
      const result = await operation()
      if (!current()) return false
      if (
        result.project_id !== projectId ||
        result.kind !== 'migration' ||
        !isSafeStorageId(result.id) ||
        (taskId !== null && taskId !== result.id) ||
        (request && result.target_space_id !== request.space_id)
      ) {
        unknown.value = true
        return false
      }
      task.value = result
      hasOperation.value = true
      taskId = result.id
      unknown.value = false
      return true
    } catch (cause) {
      if (current()) {
        error.value = storageErrorMessage(cause)
        unknown.value = storageResultUnknown(cause)
        taskId ??= safeStorageProblem(cause).task_id ?? null
      }
      return false
    } finally {
      if (current()) busy.value = false
    }
  }
  async function start(target: ApiSchemas['StorageOption'], generation: number) {
    if (
      !allowed() ||
      request ||
      taskId ||
      !target.selectable ||
      !isSafeStorageId(target.space_id) ||
      !isStorageGeneration(generation) ||
      generation !== options.project().storage_generation
    )
      return false
    request = {
      space_id: target.space_id,
      expected_generation: generation,
      idempotency_key: crypto.randomUUID(),
    }
    hasOperation.value = true
    return run(() =>
      transport.migrateProjectStorage(projectId, request!, { signal: controller.signal }),
    )
  }
  async function recover(id?: number) {
    if (id !== undefined) {
      if (!isSafeStorageId(id) || (taskId !== null && taskId !== id)) return false
      taskId = id
    }
    if (taskId)
      return run(() => transport.getStorageTask(projectId, taskId!, { signal: controller.signal }))
    // Reconcile with the original generation/key even after its own maintenance transition.
    if (
      request &&
      !disposed &&
      (options.available?.() ?? storageProjectWritable(options.project()))
    )
      return run(() =>
        transport.migrateProjectStorage(projectId, request!, { signal: controller.signal }),
      )
    return false
  }
  function dispose() {
    disposed = true
    controller.abort()
    busy.value = false
  }
  return {
    task,
    busy,
    unknown,
    error,
    hasOperation,
    canStartNew,
    startNew,
    start,
    recover,
    dispose,
  }
}
