import { computed, ref, shallowRef } from 'vue'
import type { ApiSchemas } from '@/api/client-core'
import * as api from '@/api/storage'
import { captureSession, isSessionCurrent } from '@/api/session-context'
import {
  safeStorageProblem,
  storageAccessDenied,
  storageErrorMessage,
  storageNeedsRefresh,
  storageResultUnknown,
} from '@/api/storage-errors'
import { subscribeStorageRefresh, invalidateStorageSnapshots } from '@/utils/storage-snapshots'
import {
  isSafeStorageId,
  isStorageGeneration,
  storageActionAllowed,
  storageTaskCommitted,
} from '@/utils/storage-contract'

type Task = ApiSchemas['StorageTask']
export type RepairContext = {
  project: ApiSchemas['Project']
  resourceId: number
  version: ApiSchemas['SourceVersion']
}
export function repairContextAllowed(context: RepairContext | null) {
  return (
    !!context &&
    storageActionAllowed(context.project, 'repair') &&
    isSafeStorageId(context.resourceId) &&
    isSafeStorageId(context.version.id) &&
    context.version.verification_state === 'verified' &&
    isStorageGeneration(context.version.location_generation)
  )
}
export function repairProjectSnapshotMatches(
  expected: ApiSchemas['Project'],
  latest: ApiSchemas['Project'],
) {
  return (
    latest.id === expected.id &&
    latest.owner_user_id === expected.owner_user_id &&
    latest.owner_org_id === expected.owner_org_id &&
    latest.storage_generation === expected.storage_generation &&
    latest.storage_state === expected.storage_state &&
    storageActionAllowed(latest, 'repair')
  )
}
export function createRepairSession(
  options: {
    context: () => RepairContext | null
    available?: () => boolean
    changed?: () => void
    beforeMutation?: (context: RepairContext) => Promise<boolean>
  },
  transport: Pick<
    typeof api,
    'createStorageIntent' | 'getStorageTask' | 'receiveStorageContent'
  > = api,
) {
  const task = shallowRef<Task | null>(null),
    file = shallowRef<File | null>(null)
  const busy = ref(false),
    unknown = ref(false),
    error = ref<string | null>(null),
    hasOperation = ref(false)
  const taskReady = ref(false)
  let intent: ApiSchemas['StorageRepairIntent'] | null = null
  let recoveryId: number | null = null
  let disposed = false,
    epoch = 0,
    readSequence = 0,
    refreshPending = false,
    notified = false
  const controller = new AbortController()
  const identity = () => {
    const c = options.context()
    return c ? `${c.project.id}:${c.resourceId}:${c.version.id}` : ''
  }
  const allowed = () =>
    !disposed && (options.available?.() ?? repairContextAllowed(options.context()))
  const completed = computed(
    () =>
      !!task.value &&
      storageTaskCommitted(task.value) &&
      task.value.result_revision_id === options.context()?.version.id,
  )
  const canUpload = computed(
    () =>
      allowed() &&
      taskReady.value &&
      !!file.value &&
      !!task.value?.allowed_actions.includes('upload_content') &&
      !busy.value &&
      !completed.value,
  )
  function currentGuard() {
    const session = captureSession(),
      key = identity(),
      version = epoch
    return () => !disposed && version === epoch && isSessionCurrent(session) && identity() === key
  }
  function accept(value: Task) {
    const c = options.context()
    if (
      !c ||
      value.kind !== 'repair' ||
      value.project_id !== c.project.id ||
      value.resource_id !== c.resourceId ||
      value.source_revision_id !== c.version.id ||
      !isSafeStorageId(value.id) ||
      (recoveryId !== null && value.id !== recoveryId)
    )
      return false
    task.value = value
    taskReady.value = true
    recoveryId = value.id
    unknown.value = false
    if (completed.value && !notified) {
      notified = true
      options.changed?.()
    }
    return true
  }
  function selectFile(value: File | null) {
    if (busy.value || (intent && !task.value)) return
    file.value = value
  }
  async function run(
    operation: () => Promise<Task>,
    mutationContext?: RepairContext,
    writing = false,
    admission: () => boolean = () => true,
  ) {
    if (busy.value || disposed) return false
    const owns = currentGuard(),
      request = readSequence
    const current = () => owns() && (!!mutationContext || writing || request === readSequence)
    busy.value = true
    error.value = null
    try {
      if (mutationContext) {
        const snapshot = {
          ...mutationContext,
          project: { ...mutationContext.project },
          version: { ...mutationContext.version },
        }
        if (options.beforeMutation && !(await options.beforeMutation(snapshot))) return false
        const latestContext = options.context()
        if (
          !current() ||
          !allowed() ||
          !latestContext ||
          latestContext.project.storage_generation !== snapshot.project.storage_generation ||
          latestContext.project.storage_state !== snapshot.project.storage_state ||
          latestContext.version.location_generation !== snapshot.version.location_generation
        )
          return false
      }
      if (!admission()) return false
      const value = await operation()
      if (!current()) return false
      if (!accept(value)) {
        unknown.value = true
        return false
      }
      return true
    } catch (cause) {
      if (current()) {
        if (storageAccessDenied(cause)) {
          dispose()
          task.value = null
          return false
        }
        error.value = storageErrorMessage(cause)
        unknown.value = storageResultUnknown(cause)
        const id = safeStorageProblem(cause).task_id
        if (id && recoveryId === null) recoveryId = id
        if ((mutationContext || writing) && storageNeedsRefresh(cause))
          invalidateStorageSnapshots({ projectId: options.context()?.project.id })
      }
      return false
    } finally {
      if (owns()) {
        busy.value = false
        if (refreshPending && recoveryId !== null) {
          refreshPending = false
          void recover()
        }
      }
    }
  }
  async function start(target: ApiSchemas['StorageOption'], generation: number) {
    const c = options.context(),
      bytes = file.value
    if (
      !c ||
      !allowed() ||
      busy.value ||
      intent ||
      task.value ||
      !bytes ||
      !target.selectable ||
      !isSafeStorageId(target.space_id) ||
      !isStorageGeneration(generation) ||
      generation !== c.project.storage_generation
    )
      return false
    const candidate: ApiSchemas['StorageRepairIntent'] = {
      kind: 'repair',
      idempotency_key: crypto.randomUUID(),
      size: bytes.size,
      storage_generation: generation,
      resource_id: c.resourceId,
      source_revision_id: c.version.id,
      location_generation: c.version.location_generation,
      target_space_id: target.space_id,
    }
    return run(() => {
      intent = candidate
      hasOperation.value = true
      return transport.createStorageIntent(c.project.id, candidate, { signal: controller.signal })
    }, c)
  }
  async function recover(id?: number) {
    const c = options.context()
    if (!c || disposed || (id !== undefined && !isSafeStorageId(id))) return false
    if (id !== undefined) {
      if (recoveryId !== null && recoveryId !== id) return false
      recoveryId = id
    }
    if (recoveryId !== null)
      return run(() =>
        transport.getStorageTask(c.project.id, recoveryId!, { signal: controller.signal }),
      )
    // A lost create response is reconciled with the original immutable request and key.
    if (intent && allowed())
      return run(
        () => transport.createStorageIntent(c.project.id, intent!, { signal: controller.signal }),
        undefined,
        true,
      )
    return false
  }
  async function upload() {
    const c = options.context(),
      bytes = file.value
    if (!c || !bytes || !allowed() || recoveryId === null || busy.value) return false
    if (!(await recover()) || !allowed()) return false
    const latest = task.value
    if (
      !latest ||
      !latest.allowed_actions.includes('upload_content') ||
      latest.expected_storage_generation !== c.project.storage_generation ||
      latest.expected_location_generation !== c.version.location_generation ||
      !isSafeStorageId(latest.target_space_id) ||
      !isStorageGeneration(latest.input_size) ||
      latest.input_size !== bytes.size
    )
      return false
    // Filename and size are not proof: the original task verifies the complete bytes against its trusted baseline.
    return run(
      () =>
        transport.receiveStorageContent(c.project.id, latest.id, bytes, {
          signal: controller.signal,
        }),
      c,
      false,
      () =>
        taskReady.value &&
        task.value === latest &&
        latest.allowed_actions.includes('upload_content'),
    )
  }
  function dispose() {
    stopRefresh()
    disposed = true
    ++epoch
    controller.abort()
    file.value = null
    busy.value = false
  }
  const stopRefresh = subscribeStorageRefresh({
    scope: () => ({
      projectId: options.context()?.project.id,
      organizationId: options.context()?.project.owner_org_id,
    }),
    invalidate: () => {
      ++readSequence
      taskReady.value = false
    },
    refresh: () => {
      if (recoveryId === null) return
      if (busy.value) {
        refreshPending = true
        return
      }
      return recover()
    },
  })
  return {
    task,
    file,
    busy,
    unknown,
    error,
    hasOperation,
    completed,
    canUpload,
    selectFile,
    start,
    recover,
    upload,
    dispose,
  }
}
