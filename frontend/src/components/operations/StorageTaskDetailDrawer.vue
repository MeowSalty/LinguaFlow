<script setup lang="ts">
import { computed, onBeforeUnmount, ref, shallowRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useMessage } from 'naive-ui'
import { fetchProject, type ApiSchemas } from '@/api/client'
import { cancelStorageTask, retryStorageTask } from '@/api/storage'
import {
  storageAccessDenied,
  storageNeedsRefresh,
  storageErrorMessage,
  storageTaskErrorMessage,
} from '@/api/storage-errors'
import { invalidateStorageSnapshots, subscribeStorageRefresh } from '@/utils/storage-snapshots'
import { captureSession, isSessionCurrent, sessionGeneration } from '@/api/session-context'
import { ApiError } from '@/api/utils'
import { useOperationsStore } from '@/stores/operations'
import { useOrganizationsStore } from '@/stores/organizations'
import { onOrganizationInvalidated } from '@/utils/organization-scope'
import { safeTaskNumber, type OperationLocator } from '@/utils/operationQuery'
import { formatDateTime } from '@/utils/datetime'
import { DRAWER_WIDTH } from '@/components/common/uiConstants'
import { storageProjectWritable, storageTaskCommitted } from '@/utils/storage-contract'
import StorageRepairDrawer from '@/components/storage/StorageRepairDrawer.vue'

const props = defineProps<{ locator: (OperationLocator & { task_type: 'storage' }) | null }>()
const emit = defineEmits<{ close: [] }>()
const { t, te } = useI18n()
const router = useRouter()
const message = useMessage()
const operations = useOperationsStore()
const organizations = useOrganizationsStore()
const task = shallowRef<ApiSchemas['StorageTask'] | null>(null)
const project = shallowRef<ApiSchemas['Project'] | null>(null)
const loading = ref(false)
const mutating = ref(false)
const permissionLoading = ref(false)
const error = ref<string | null>(null)
const repairVisible = ref(false)
const taskReady = ref(false)
let admissionVersion = 0
let generation = 0
let unsubscribe: (() => void) | null = null
let controller = new AbortController()
let focusBeforeOpen: HTMLElement | null = null
const stop = (): void => {
  unsubscribe?.()
  unsubscribe = null
  controller.abort()
  controller = new AbortController()
}
const clear = (): void => {
  generation++
  stop()
  task.value = null
  project.value = null
  loading.value = false
  mutating.value = false
  permissionLoading.value = false
  error.value = null
  repairVisible.value = false
  taskReady.value = false
}
const writable = computed(() => {
  if (!project.value || permissionLoading.value) return false
  return project.value.owner_org_id
    ? !organizations.loading &&
        !organizations.error &&
        organizations.canWrite(project.value.owner_org_id)
    : storageProjectWritable(project.value)
})
const can = (action: 'cancel' | 'retry'): boolean =>
  writable.value &&
  taskReady.value &&
  !loading.value &&
  !mutating.value &&
  !error.value &&
  task.value?.kind !== 'export_delete' &&
  !!task.value?.allowed_actions.includes(action)
const phase = computed(() => {
  const key = `operations.storageTask.phases.${task.value?.phase ?? ''}`
  return te(key) ? t(key) : t('operations.storageTask.unknownPhase')
})
const kind = computed(() => {
  const key = `operations.storageTask.kinds.${task.value?.kind ?? ''}`
  return te(key) ? t(key) : t('operations.storageTask.title')
})
const fail = (cause: unknown): void => {
  loading.value = false
  taskReady.value = false
  error.value = storageAccessDenied(cause)
    ? t('workbench.details.unavailable')
    : storageErrorMessage(cause)
  if (storageAccessDenied(cause)) {
    generation++
    task.value = null
    project.value = null
    mutating.value = false
    permissionLoading.value = false
    stop()
  }
}
const subscribe = (): void => {
  const locator = props.locator
  if (!locator?.project_id) return
  unsubscribe?.()
  const snapshot = captureSession()
  const request = generation
  const admission = admissionVersion
  const current = () =>
    request === generation && admission === admissionVersion && isSessionCurrent(snapshot)
  unsubscribe = operations.subscribeTask(
    locator,
    (value) => {
      if (!current()) return
      task.value = value
      taskReady.value = true
      error.value = null
      loading.value = false
    },
    (cause) => {
      if (current()) fail(cause)
    },
  )
}
const load = async (): Promise<void> => {
  clear()
  const locator = props.locator
  if (!locator?.project_id) return
  const snapshot = captureSession()
  const request = generation
  const current = () => request === generation && isSessionCurrent(snapshot)
  loading.value = true
  permissionLoading.value = true
  try {
    safeTaskNumber(locator.task_id)
    safeTaskNumber(String(locator.project_id))
    const found = await fetchProject(locator.project_id, undefined, controller.signal)
    if (!current()) return
    project.value = found
    if (found.owner_org_id) await organizations.refresh()
    if (!current()) return
    permissionLoading.value = false
    subscribe()
  } catch (cause) {
    if (current()) fail(cause)
  } finally {
    if (current()) permissionLoading.value = false
  }
}
const refresh = async (): Promise<void> => {
  if (mutating.value) return
  if (!project.value) return load()
  // Renew the subscription so a formerly blocked cleanup can resume polling.
  generation++
  stop()
  loading.value = true
  taskReady.value = false
  subscribe()
}
const act = async (action: 'cancel' | 'retry'): Promise<void> => {
  const locator = props.locator
  if (!locator?.project_id || !can(action) || mutating.value) return
  const admission = admissionVersion
  // Stop pre-mutation deliveries before checking the current server allowance.
  generation++
  stop()
  const snapshot = captureSession()
  const request = generation
  const current = () => request === generation && isSessionCurrent(snapshot)
  mutating.value = true
  try {
    const fresh = await fetchProject(locator.project_id, undefined, controller.signal)
    if (!current()) return
    project.value = fresh
    if (fresh.owner_org_id) await organizations.refresh()
    if (!current()) return
    const latest = await operations.queryStorage(
      locator.project_id,
      locator.task_id,
      controller.signal,
    )
    if (!current()) return
    task.value = latest
    if (
      admission !== admissionVersion ||
      !writable.value ||
      !latest.allowed_actions.includes(action)
    ) {
      message.info(t('operations.storageTask.actionChanged'))
      return
    }
    const response = await (action === 'cancel' ? cancelStorageTask : retryStorageTask)(
      locator.project_id,
      safeTaskNumber(locator.task_id),
      { signal: controller.signal },
    )
    if (!current()) return
    task.value = response
    // Invalidation aborts older shared GETs before any post-mutation subscription starts.
    await operations.invalidate()
    if (!current()) return
    error.value = null
  } catch (cause) {
    if (!current()) return
    if (storageAccessDenied(cause)) {
      fail(cause)
      operations.forget(locator)
    } else if (storageNeedsRefresh(cause) || (cause instanceof ApiError && cause.status === 409)) {
      if (storageNeedsRefresh(cause)) invalidateStorageSnapshots({ projectId: locator.project_id })
      await operations.invalidate()
      if (current()) message.warning(storageErrorMessage(cause))
    } else {
      // A failed transport does not establish whether a mutation was applied.
      error.value = t('operations.storageTask.unknownResult')
      await operations.invalidate()
    }
  } finally {
    if (current()) {
      mutating.value = false
      subscribe()
    }
  }
}
const close = (): void => {
  clear()
  emit('close')
}
const restoreFocus = (): void => {
  if (focusBeforeOpen?.isConnected) focusBeforeOpen.focus()
}
const openProject = (): void => {
  const projectId = props.locator?.project_id
  close()
  if (projectId) void router.push({ path: `/projects/${projectId}`, query: { tab: 'storage' } })
}
const openSourceUpdate = (): void => {
  const value = task.value
  if (!value || !project.value) return
  const projectId = project.value.id
  close()
  void router.push({
    path: `/projects/${projectId}`,
    query: { tab: 'resources', source_task: String(value.id) },
  })
}
watch(
  () => props.locator,
  (next, old) => {
    if (next && !old)
      focusBeforeOpen =
        document.activeElement instanceof HTMLElement ? document.activeElement : null
    void load()
  },
  { immediate: true },
)
watch(
  sessionGeneration,
  () => {
    clear()
    emit('close')
  },
  { flush: 'sync' },
)
onOrganizationInvalidated((orgId) => {
  if (project.value?.owner_org_id === orgId) void load()
})
const stopRefresh = subscribeStorageRefresh({
  scope: () => ({
    projectId: props.locator?.project_id,
    organizationId: project.value?.owner_org_id,
  }),
  invalidate: () => {
    admissionVersion++
    taskReady.value = false
  },
  refresh: () => (props.locator ? refresh() : undefined),
})
onBeforeUnmount(() => {
  clear()
  stopRefresh()
})
</script>

<template>
  <NDrawer
    :show="!!locator"
    :width="DRAWER_WIDTH.l"
    @update:show="
      (value: boolean) => {
        if (!value) close()
      }
    "
    @after-leave="restoreFocus"
  >
    <NDrawerContent
      :title="`${t('operations.storageTask.title')} #${locator?.task_id ?? ''}`"
      closable
    >
      <div class="space-y-5">
        <p v-if="project" class="text-sm text-lf-text-muted">{{ project.name }}</p>
        <NAlert v-if="error" type="warning" :bordered="false">{{ error }}</NAlert>
        <NSkeleton v-if="loading && !task" height="160px" />
        <template v-if="task">
          <NTag
            :type="
              task.status === 'failed' ? 'error' : storageTaskCommitted(task) ? 'success' : 'info'
            "
            :bordered="false"
            >{{ t(`operations.${task.status}`) }}</NTag
          >
          <NDescriptions label-placement="top" :column="1" bordered>
            <NDescriptionsItem :label="t('operations.storageTask.kind')">{{
              kind
            }}</NDescriptionsItem>
            <NDescriptionsItem :label="t('operations.storageTask.phase')">{{
              phase
            }}</NDescriptionsItem>
            <NDescriptionsItem :label="t('operations.storageTask.cleanupTitle')">{{
              t(`operations.storageTask.cleanup.${task.cleanup_status}`)
            }}</NDescriptionsItem>
            <NDescriptionsItem
              v-if="task.next_retry_at"
              :label="t('operations.storageTask.nextRetryAt')"
              >{{
                formatDateTime(task.next_retry_at, { dateStyle: 'short', timeStyle: 'medium' })
              }}</NDescriptionsItem
            >
            <NDescriptionsItem
              v-if="task.result_resource_id"
              :label="t('operations.storageTask.resourceResult')"
              >#{{ task.result_resource_id }}</NDescriptionsItem
            >
            <NDescriptionsItem
              v-if="task.result_revision_id"
              :label="t('operations.storageTask.revisionResult')"
              >#{{ task.result_revision_id }}</NDescriptionsItem
            >
            <NDescriptionsItem
              v-if="task.result_artifact_id"
              :label="t('operations.storageTask.artifactResult')"
              >#{{ task.result_artifact_id }}</NDescriptionsItem
            >
          </NDescriptions>
          <NAlert v-if="task.phase === 'prepared'" type="info" :bordered="false">{{
            t('operations.storageTask.preparedHint')
          }}</NAlert>
          <NAlert
            v-if="task.status === 'completed' && task.cleanup_status === 'blocked'"
            type="warning"
            :bordered="false"
            >{{ t('operations.storageTask.completedBlocked') }}</NAlert
          >
          <NAlert v-if="task.error_code" type="warning" :bordered="false">{{
            storageTaskErrorMessage(task.error_code)
          }}</NAlert>
          <p class="text-xs leading-5 text-lf-text-muted">
            {{ t('operations.storageTask.cleanupHint') }}
          </p>
          <p v-if="!writable && !permissionLoading" class="text-xs text-lf-text-muted">
            {{ t('operations.storageTask.readOnly') }}
          </p>
        </template>
      </div>
      <template #footer>
        <div class="flex flex-wrap justify-end gap-2">
          <NButton :loading="loading" :disabled="mutating" @click="refresh">{{
            t('operations.refresh')
          }}</NButton>
          <NButton v-if="can('retry')" :loading="mutating" @click="act('retry')">{{
            t('operations.retry')
          }}</NButton>
          <NPopconfirm v-if="can('cancel')" @positive-click="act('cancel')">
            <template #trigger
              ><NButton type="error" :loading="mutating">{{
                t('operations.cancel')
              }}</NButton></template
            >
            {{ t('operations.cancelDescription') }}
          </NPopconfirm>
          <NButton v-if="project" @click="openProject">{{
            t('operations.storageTask.projectStorage')
          }}</NButton>
          <NButton
            v-if="
              task?.kind === 'repair' && task.resource_id && task.source_revision_id && writable
            "
            @click="repairVisible = true"
            >{{ t('storage.repair') }}</NButton
          >
          <NButton
            v-if="
              task?.kind === 'source_update' && task.resource_id && task.source_preview && writable
            "
            @click="openSourceUpdate"
            >{{ t('sourceStorage.update') }}</NButton
          >
          <NButton @click="close">{{ t('operations.close') }}</NButton>
        </div>
      </template>
    </NDrawerContent>
  </NDrawer>
  <StorageRepairDrawer
    v-if="project && task?.kind === 'repair' && task.resource_id && task.source_revision_id"
    v-model:show="repairVisible"
    :project="project"
    :resource-id="task.resource_id"
    :source-revision-id="task.source_revision_id"
    :task-id="task.id"
    @changed="refresh"
  />
</template>
