<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { NButton, useMessage } from 'naive-ui'
import IconCarbonOverflowMenuVertical from '~icons/carbon/overflow-menu-vertical'
import { useI18n } from 'vue-i18n'
import { cancelJob, pauseJob, resumeJob, retryJob } from '@/api/client'
import { ApiError, isAccessDenied } from '@/api/utils'
import { captureSession, isSessionCurrent } from '@/api/session-context'
import { useGlobalJobTrackerStore } from '@/stores/globalJobTracker'
import { useOperationsStore } from '@/stores/operations'
import { useTaskHistoryStore } from '@/stores/taskHistory'
import { useTaskMutationsStore } from '@/stores/taskMutations'
import { taskHistoryErrorMessage } from '@/api/task-history'
import { taskHistoryDeleteOption } from '@/utils/taskHistoryPresentation'
import JobDetailDrawerBase from '@/components/workspace/JobDetailDrawerBase.vue'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const tracker = useGlobalJobTrackerStore()
const operations = useOperationsStore()
const history = useTaskHistoryStore()
const mutations = useTaskMutationsStore()
const message = useMessage()
type Action = 'pause' | 'resume' | 'cancel' | 'retry'
const busy = ref<Action | null>(null)
let detailGeneration = 0
const job = computed(() => tracker.detailJob)
const target = computed(() =>
  job.value
    ? {
        kind: 'translation' as const,
        id: String(job.value.id),
        project_id: job.value.project_id,
        project_name: tracker.projectName,
        can_delete: job.value.can_delete,
        status: job.value.status,
      }
    : null,
)
const mutationPending = computed(() => !!target.value && mutations.isPending(target.value))
const deleteOptions = computed(() => [
  taskHistoryDeleteOption(
    job.value?.can_delete === true,
    job.value?.status ?? '',
    mutationPending.value,
  ),
])
const requestDelete = (): void => {
  if (target.value?.can_delete && !mutationPending.value) history.requestDelete([target.value])
}
const actions = { pause: pauseJob, resume: resumeJob, cancel: cancelJob, retry: retryJob }
const allowed = (action: Action, status: string): boolean =>
  ({
    pause: ['pending', 'running'],
    resume: ['paused'],
    cancel: ['pending', 'running', 'paused'],
    retry: ['failed', 'cancelled'],
  })[action].includes(status)
const close = (): void => {
  tracker.closeDetail()
  if (route.path === '/operations' && (route.query.task_id || route.query.job_id)) {
    const query = { ...route.query }
    delete query.task_id
    delete query.job_id
    void router.replace({ path: route.path, query })
  }
}
const show = computed({
  get: () => tracker.drawerJobId != null,
  set: (value) => {
    if (!value) close()
  },
})
watch(
  () => tracker.drawerJobId,
  () => {
    detailGeneration++
    busy.value = null
  },
  { flush: 'sync' },
)
const handleAction = async (action: Action): Promise<void> => {
  const id = job.value?.id
  if (!id || busy.value || mutationPending.value || !target.value) return
  const submittedTarget = { ...target.value }
  const snapshot = captureSession()
  const generation = detailGeneration
  const current = () =>
    isSessionCurrent(snapshot) && tracker.drawerJobId === id && generation === detailGeneration
  busy.value = action
  try {
    // Read authorization and state are rechecked before the write; the server handles races.
    await mutations.run([submittedTarget], action, async () => {
      const fresh = await operations.queryTranslation(String(id))
      if (!current()) return
      tracker.detailJob = fresh
      if (!allowed(action, fresh.status)) {
        message.info(t('workbench.details.conflict'))
        return
      }
      const response = await actions[action](id)
      if (!current()) return
      tracker.detailJob = response
      if (action === 'pause' && response.status !== 'paused')
        message.success(t('workbench.details.pauseRequested'))
      else message.success(t('workbench.details.updated'))
      await operations.invalidate()
      if (current()) await tracker.refreshDetail()
    })
  } catch (error) {
    if (!current()) return
    if (isAccessDenied(error)) {
      tracker.detailJob = null
      tracker.detailError = t('operations.inaccessible')
      operations.forget({ task_type: 'translation', task_id: String(id) })
      await tracker.refreshDetail()
    } else if (error instanceof ApiError && error.status === 409) {
      await tracker.refreshDetail()
      if (current()) message.warning(t('workbench.details.conflict'))
    } else message.error(taskHistoryErrorMessage(error))
  } finally {
    if (current()) busy.value = null
  }
}
const goToProject = (): void => {
  const projectId = job.value?.project_id ?? tracker.detailProjectId
  if (!projectId) return
  close()
  void router.push({ path: `/projects/${projectId}`, query: { tab: 'jobs' } })
}
</script>
<template>
  <JobDetailDrawerBase
    :show="show"
    :job="job"
    :loading="tracker.loadingDetail"
    :error="tracker.detailError"
    :project-name="tracker.projectName || (job ? `#${job.project_id}` : undefined)"
    :title-prefix="t('globalJobTracker.detailFallbackTitle')"
    :empty-description="tracker.detailError || t('globalJobTracker.noTrackedJobs')"
    @update:show="(value) => (show = value)"
  >
    <template #footer>
      <div class="flex flex-wrap justify-end gap-2">
        <NButton
          v-for="action in ['pause', 'resume', 'cancel', 'retry'] as const"
          v-show="job && allowed(action, job.status)"
          :key="action"
          :type="action === 'cancel' ? 'error' : 'default'"
          :disabled="busy !== null || mutationPending || !!tracker.detailError"
          :loading="busy === action"
          @click="handleAction(action)"
        >
          {{ t(`workspace.job.actions.${action}`) }}
        </NButton>
        <NDropdown
          v-if="typeof job?.can_delete === 'boolean'"
          trigger="click"
          :options="deleteOptions"
          @select="requestDelete"
        >
          <NButton
            :disabled="busy !== null || mutationPending || !!tracker.detailError"
            :aria-label="t('taskHistory.more')"
            data-task-history-trigger
            :title="t('taskHistory.more')"
            ><template #icon><IconCarbonOverflowMenuVertical /></template
          ></NButton>
        </NDropdown>
        <NButton v-if="tracker.detailError" @click="tracker.refreshDetail()">{{
          t('common.actions.refresh')
        }}</NButton>
        <NButton @click="close">{{ t('globalJobTracker.close') }}</NButton>
        <NButton
          v-if="
            (job?.project_id ?? tracker.detailProjectId) &&
            route.path !== `/projects/${job?.project_id ?? tracker.detailProjectId}`
          "
          type="primary"
          @click="goToProject"
          >{{ t('globalJobTracker.goToProject') }}</NButton
        >
      </div>
    </template>
  </JobDetailDrawerBase>
</template>
