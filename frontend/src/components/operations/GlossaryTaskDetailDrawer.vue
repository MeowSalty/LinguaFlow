<script setup lang="ts">
import { computed, onBeforeUnmount, ref, shallowRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useMessage } from 'naive-ui'
import IconCarbonOverflowMenuVertical from '~icons/carbon/overflow-menu-vertical'
import { type ApiSchemas, cancelGlossarySyncTask, fetchProject } from '@/api/client'
import { captureSession, isSessionCurrent, sessionGeneration } from '@/api/session-context'
import { ApiError, isAccessDenied } from '@/api/utils'
import { useOperationsStore } from '@/stores/operations'
import { useTaskHistoryStore } from '@/stores/taskHistory'
import { useTaskMutationsStore } from '@/stores/taskMutations'
import { taskHistoryKey, taskHistoryErrorMessage } from '@/api/task-history'
import { formatDateTime } from '@/utils/datetime'
import { taskHistoryDeleteOption } from '@/utils/taskHistoryPresentation'
import { useAuthStore } from '@/stores/auth'
import { useServiceStore } from '@/stores/service'
import { useOrganizationsStore } from '@/stores/organizations'
import { onOrganizationInvalidated } from '@/utils/organization-scope'
import type { OperationLocator } from '@/utils/operationQuery'
import { DRAWER_WIDTH } from '@/components/common/uiConstants'

const props = defineProps<{ locator: OperationLocator | null }>()
const emit = defineEmits<{ close: [] }>()
const { t } = useI18n()
const router = useRouter()
const message = useMessage()
const operations = useOperationsStore()
const history = useTaskHistoryStore()
const mutations = useTaskMutationsStore()
const auth = useAuthStore()
const service = useServiceStore()
const organizations = useOrganizationsStore()
const task = shallowRef<ApiSchemas['GlossarySyncTaskStatusResponse'] | null>(null)
const project = shallowRef<ApiSchemas['Project'] | null>(null)
const loading = ref(false)
const permissionLoading = ref(false)
const error = ref<string | null>(null)
const cancelling = ref(false)
const target = computed(() =>
  props.locator?.project_id && task.value
    ? {
        kind: 'glossary_sync' as const,
        id: props.locator.task_id,
        project_id: props.locator.project_id,
        project_name: project.value?.name,
        can_delete: task.value.can_delete,
        status: task.value.status,
      }
    : null,
)
const mutationPending = computed(() => !!target.value && mutations.isPending(target.value))
const deleteOptions = computed(() => [
  taskHistoryDeleteOption(
    task.value?.can_delete === true,
    task.value?.status ?? '',
    mutationPending.value,
  ),
])
const requestDelete = (): void => {
  if (target.value?.can_delete && !mutationPending.value) history.requestDelete([target.value])
}
let generation = 0
let unsubscribe: (() => void) | null = null
let drawerController = new AbortController()
let focusBeforeOpen: HTMLElement | null = null
const stop = (): void => {
  unsubscribe?.()
  unsubscribe = null
  drawerController.abort()
  drawerController = new AbortController()
}
const clear = (): void => {
  generation++
  stop()
  task.value = null
  project.value = null
  error.value = null
  loading.value = false
  cancelling.value = false
  permissionLoading.value = false
}
const allowed = computed(() => {
  if (!project.value || permissionLoading.value) return false
  if (project.value.owner_org_id) return organizations.canWrite(project.value.owner_org_id)
  return service.isLocal || project.value.owner_user_id === auth.user?.id
})
const canCancel = computed(
  () => allowed.value && task.value && ['pending', 'running'].includes(task.value.status),
)
const progress = computed(() =>
  task.value && task.value.total > 0
    ? Math.min(100, Math.round((task.value.processed / task.value.total) * 100))
    : null,
)
const fail = (cause: unknown, projectDenied = false): void => {
  loading.value = false
  error.value =
    cause instanceof ApiError && cause.status === 404
      ? t('taskHistory.missing')
      : isAccessDenied(cause)
        ? t('taskHistory.inaccessible')
        : cause instanceof Error
          ? cause.message
          : t('operations.loadFailed')
  if (isAccessDenied(cause)) {
    generation++
    task.value = null
    if (projectDenied) project.value = null
    stop()
    if (props.locator) operations.forget(props.locator)
  }
}
const load = async (): Promise<void> => {
  clear()
  const locator = props.locator
  if (!locator || locator.task_type !== 'glossary_sync' || !locator.project_id) return
  const snapshot = captureSession()
  const request = generation
  const current = () => request === generation && isSessionCurrent(snapshot)
  loading.value = true
  permissionLoading.value = true
  try {
    const found = await fetchProject(locator.project_id, undefined, drawerController.signal)
    if (!current()) return
    project.value = found
    if (found.owner_org_id) await organizations.refresh()
    if (!current()) return
    permissionLoading.value = false
    unsubscribe = operations.subscribeTask(
      { ...locator, task_type: 'glossary_sync' },
      (data) => {
        if (!current()) return
        task.value = data
        error.value = null
        loading.value = false
      },
      (cause) => {
        if (current()) fail(cause)
      },
      { terminalRecheckMs: 30000 },
    )
  } catch (cause) {
    if (current()) fail(cause, true)
  } finally {
    if (current()) permissionLoading.value = false
  }
}
const refresh = async (): Promise<void> => {
  const locator = props.locator
  if (!locator?.project_id) return
  if (!project.value) {
    await load()
    return
  }
  const snapshot = captureSession()
  const request = generation
  try {
    const value = await operations.querySync(
      locator.project_id,
      locator.task_id,
      drawerController.signal,
    )
    if (request === generation && isSessionCurrent(snapshot)) {
      task.value = value
      error.value = null
    }
  } catch (cause) {
    if (request === generation && isSessionCurrent(snapshot)) fail(cause)
  }
}
const cancel = async (): Promise<void> => {
  const locator = props.locator
  if (
    !locator?.project_id ||
    !canCancel.value ||
    cancelling.value ||
    mutationPending.value ||
    !target.value
  )
    return
  const submittedTarget = { ...target.value }
  const snapshot = captureSession()
  const request = generation
  const current = () => request === generation && isSessionCurrent(snapshot)
  cancelling.value = true
  try {
    await mutations.run([submittedTarget], 'cancel', async () => {
      const latest = await operations.querySync(
        submittedTarget.project_id,
        locator.task_id,
        drawerController.signal,
      )
      if (!current()) return
      task.value = latest
      if (!['pending', 'running'].includes(latest.status)) {
        message.info(t('workbench.details.conflict'))
        return
      }
      const response = await cancelGlossarySyncTask(submittedTarget.project_id, locator.task_id)
      if (!isSessionCurrent(snapshot)) return
      void operations.invalidate()
      if (!current()) return
      task.value = { ...latest, status: response.status }
      await refresh()
    })
  } catch (cause) {
    if (!current()) return
    if (cause instanceof ApiError && cause.status === 409) {
      await refresh()
      if (current()) message.warning(t('workbench.details.conflict'))
    } else if (cause instanceof ApiError && cause.status === 403) {
      await refresh()
      if (current()) message.warning(taskHistoryErrorMessage(cause))
    } else fail(cause)
  } finally {
    if (current()) cancelling.value = false
  }
}
const close = (): void => {
  clear()
  emit('close')
}
watch(
  () => history.removalRevision,
  () => {
    const locator = props.locator
    if (!locator?.project_id) return
    const key = taskHistoryKey({
      kind: 'glossary_sync',
      id: locator.task_id,
      project_id: locator.project_id,
    })
    if (history.lastRemoved.some((item) => taskHistoryKey(item) === key)) close()
  },
)
const restoreFocus = (): void => {
  if (focusBeforeOpen?.isConnected) focusBeforeOpen.focus()
}
const goToGlossary = (): void => {
  const projectId = props.locator?.project_id
  close()
  if (projectId) void router.push({ path: `/projects/${projectId}`, query: { tab: 'glossary' } })
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
  if (project.value?.owner_org_id === orgId) {
    clear()
    void load()
  }
})
const visibility = (): void => {
  if (document.visibilityState === 'visible' && props.locator) void load()
}
document.addEventListener('visibilitychange', visibility)
onBeforeUnmount(() => {
  clear()
  document.removeEventListener('visibilitychange', visibility)
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
      :title="`${t('workbench.details.syncTitle')} #${locator?.task_id ?? ''}`"
      closable
    >
      <div class="space-y-5">
        <p v-if="project" class="text-sm text-lf-text-muted">{{ project.name }}</p>
        <NAlert
          v-if="error"
          type="warning"
          :title="task ? t('workbench.home.refreshFailed') : undefined"
          >{{ error }}</NAlert
        >
        <NSkeleton v-if="loading && !task" height="130px" />
        <template v-if="task">
          <NTag
            :type="
              task.status === 'failed' ? 'error' : task.status === 'completed' ? 'success' : 'info'
            "
            :bordered="false"
            >{{ t(`workbench.home.statuses.${task.status}`) }}</NTag
          >
          <p class="text-lg font-semibold tabular-nums">
            {{ t('workbench.details.progress', { processed: task.processed, total: task.total }) }}
          </p>
          <NProgress
            v-if="progress !== null"
            type="line"
            :percentage="progress"
            :processing="task.status === 'running'"
          />
          <p v-else class="text-sm text-lf-text-muted">{{ t('workbench.details.unknownTotal') }}</p>
          <p class="text-xs leading-5 text-lf-text-muted">
            {{ t('workbench.details.progressHint') }}
          </p>
          <dl class="text-sm">
            <dt class="text-xs text-lf-text-muted">{{ t('taskHistory.finishedAt') }}</dt>
            <dd class="mt-1 tabular-nums">
              {{
                task.finished_at
                  ? formatDateTime(task.finished_at, { dateStyle: 'short', timeStyle: 'medium' })
                  : t('taskHistory.finishedUnknown')
              }}
            </dd>
          </dl>
          <NAlert v-if="task.error" type="error">{{ task.error }}</NAlert>
          <div
            v-if="task.result"
            class="grid grid-cols-2 gap-4 rounded-lg border border-lf-border-soft p-4 text-sm"
          >
            <div>
              {{ t('workbench.details.updatedSegments') }}
              <p class="mt-2 text-xl tabular-nums">{{ task.result.total_updated ?? '—' }}</p>
            </div>
            <div>
              {{ t('workbench.details.skippedSegments') }}
              <p class="mt-2 text-xl tabular-nums">{{ task.result.total_skipped ?? '—' }}</p>
            </div>
          </div>
          <p v-if="!allowed && !permissionLoading" class="text-xs text-lf-text-muted">
            {{ t('workbench.details.readOnly') }}
          </p>
        </template>
      </div>
      <template #footer>
        <div class="flex flex-wrap justify-end gap-2">
          <NButton :loading="loading" @click="refresh">{{ t('common.actions.refresh') }}</NButton>
          <NButton
            v-if="canCancel"
            type="error"
            :loading="cancelling"
            :disabled="!!error || mutationPending"
            @click="cancel"
            >{{ t('workbench.details.cancellation') }}</NButton
          >
          <NDropdown
            v-if="typeof task?.can_delete === 'boolean'"
            trigger="click"
            :options="deleteOptions"
            @select="requestDelete"
          >
            <NButton
              :disabled="cancelling || mutationPending || !!error"
              :aria-label="t('taskHistory.more')"
              data-task-history-trigger
              :title="t('taskHistory.more')"
              ><template #icon><IconCarbonOverflowMenuVertical /></template
            ></NButton>
          </NDropdown>
          <NButton v-if="!task && project" @click="goToGlossary">{{
            t('taskHistory.project')
          }}</NButton>
          <NButton v-if="task?.status === 'failed'" @click="goToGlossary">{{
            t('workbench.details.reanalyze')
          }}</NButton>
          <NButton @click="close">{{ t('globalJobTracker.close') }}</NButton>
        </div>
      </template>
    </NDrawerContent>
  </NDrawer>
</template>
