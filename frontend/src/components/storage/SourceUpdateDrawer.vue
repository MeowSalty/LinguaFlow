<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NDescriptions,
  NDescriptionsItem,
  NDrawer,
  NDrawerContent,
} from 'naive-ui'
import type { ApiSchemas } from '@/api/client'
import { captureSession, isSessionCurrent, sessionGeneration } from '@/api/session-context'
import { downloadLegacySourceSnapshot } from '@/api/storage'
import { storageErrorMessage, storageTaskErrorMessage } from '@/api/storage-errors'
import { useSourceUpdateSession } from '@/composables/useSourceUpdateSession'
import { storageActionAllowed } from '@/utils/storage-contract'
import { useProjectWorkspaceStore } from '@/stores/projectWorkspace'
import { DRAWER_WIDTH } from '@/components/common/uiConstants'

const props = defineProps<{
  show: boolean
  projectId: number
  resource: ApiSchemas['Resource']
  file?: File | null
  task?: ApiSchemas['StorageTask'] | null
  beforeSavedContent?: () => Promise<boolean>
}>()
const emit = defineEmits<{
  'update:show': [value: boolean]
  completed: []
  state: [value: string]
}>()
const { t } = useI18n()
const workspace = useProjectWorkspaceStore()
const writable = computed(
  () => workspace.storageContentWritable && storageActionAllowed(workspace.project, 'sourceUpdate'),
)
const session = useSourceUpdateSession(
  async () => {
    if (!(await (props.beforeSavedContent?.() ?? true))) return false
    await workspace.prepareStorageWrite(props.projectId, 'sourceUpdate')
    return true
  },
  () => writable.value,
)
const busy = computed(
  () => ['previewing', 'submitting'].includes(session.state.value) || session.recovering.value,
)
const unresolved = computed(() => ['unknown', 'tracking', 'blocked'].includes(session.state.value))
const viewState = ref<'refreshed' | 'deferred' | 'failed' | null>(null)
const downloadBusy = ref(false)
const downloadError = ref('')
const selectedResource = computed(
  () => workspace.resources.find((item) => item.id === props.resource.id) ?? props.resource,
)
watch(
  [
    () => props.projectId,
    () => props.resource.id,
    () => workspace.project?.owner_user_id,
    () => workspace.project?.owner_org_id,
  ],
  () => {
    session.reset()
    viewState.value = null
  },
  { flush: 'sync' },
)
watch(
  [
    () => selectedResource.value.source_generation,
    () => selectedResource.value.translation_generation,
    () => workspace.contentWriteRevision,
  ],
  () => session.invalidate(),
  { flush: 'sync' },
)
watch(sessionGeneration, () => {
  session.reset()
  emit('update:show', false)
})
watch(
  [() => props.show, () => props.projectId, () => props.resource.id, () => props.file],
  ([show, project, resource, file]) => {
    if (show && file && session.file.value !== file) session.select(project, resource, file)
  },
  { immediate: true },
)
watch(session.state, (value) => emit('state', value))
watch(
  [() => props.show, () => props.projectId, () => props.resource.id, () => props.task],
  ([show, project, resource, task]) => {
    if (show && task && session.taskId.value !== task.id) session.restore(project, resource, task)
  },
  { immediate: true },
)
const refreshView = async () => {
  const publication = session.published.value
  if (!publication) return
  const result = await workspace.refreshAfterSourceUpdate(
    publication.projectId,
    publication.resourceId,
    async () => props.beforeSavedContent?.() ?? true,
  )
  if (session.published.value === publication) viewState.value = result
}
watch(
  session.published,
  async (value) => {
    if (!value) return
    emit('completed')
    await refreshView()
  },
  { immediate: true },
)
const chooseFile = () => {
  const owner = captureSession()
  const project = props.projectId
  const resource = props.resource.id
  const input = document.createElement('input')
  input.type = 'file'
  input.onchange = () => {
    const candidate = input.files?.[0]
    if (
      candidate &&
      isSessionCurrent(owner) &&
      props.projectId === project &&
      props.resource.id === resource
    )
      session.select(project, resource, candidate)
  }
  input.click()
}
const downloadLegacy = async () => {
  if (
    !session.preview.value?.legacy_snapshot_available ||
    !session.taskId.value ||
    downloadBusy.value
  )
    return
  const owner = captureSession()
  const id = session.taskId.value
  downloadBusy.value = true
  downloadError.value = ''
  try {
    const result = await downloadLegacySourceSnapshot(props.projectId, id)
    if (!isSessionCurrent(owner) || session.taskId.value !== id) return
    const url = URL.createObjectURL(result.blob)
    try {
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = result.filename || 'legacy-snapshot.json'
      anchor.click()
    } finally {
      URL.revokeObjectURL(url)
    }
  } catch (error) {
    if (isSessionCurrent(owner) && session.taskId.value === id)
      downloadError.value = storageErrorMessage(error)
  } finally {
    downloadBusy.value = false
  }
}
const expiryLabel = (value: string | null | undefined) =>
  value ? new Date(value).toLocaleString() : t('sourceStorage.noExpiry')
const taskHref = computed(() =>
  session.taskId.value
    ? `/operations?task_type=storage&project_id=${props.projectId}&task_id=${session.taskId.value}`
    : `/operations?task_type=storage&project_id=${props.projectId}`,
)
</script>

<template>
  <NDrawer
    :show="show"
    :width="DRAWER_WIDTH.m"
    :mask-closable="!busy"
    :close-on-esc="!busy"
    @update:show="emit('update:show', $event)"
  >
    <NDrawerContent :title="t('sourceStorage.update')" :closable="!busy">
      <div class="space-y-4">
        <div class="font-medium text-lf-text-strong">{{ resource.name }}</div>
        <NAlert v-if="!writable" type="warning" :bordered="false">{{
          t('sourceStorage.maintenanceUnknown')
        }}</NAlert>
        <p class="text-sm text-lf-text-muted">{{ t('sourceStorage.updateHint') }}</p>
        <NButton :disabled="busy || unresolved || !writable" @click="chooseFile">{{
          t('sourceStorage.chooseFile')
        }}</NButton>
        <NDescriptions v-if="session.file.value || session.preview.value" :column="1" bordered>
          <NDescriptionsItem v-if="session.file.value" :label="t('sourceStorage.file')">{{
            session.file.value.name
          }}</NDescriptionsItem>
          <template v-if="session.preview.value">
            <NDescriptionsItem
              v-for="name in ['added', 'updated', 'deleted', 'unchanged'] as const"
              :key="name"
              :label="t(`sourceStorage.stats.${name}`)"
              >{{ session.preview.value.stats[name] }}</NDescriptionsItem
            >
            <NDescriptionsItem :label="t('sourceStorage.expiresAt')">{{
              expiryLabel(session.expiresAt.value)
            }}</NDescriptionsItem>
          </template>
        </NDescriptions>
        <NAlert
          v-if="session.preview.value?.baseline_trust === 'legacy_unverified'"
          type="warning"
          :bordered="false"
          >{{ t('sourceStorage.legacyHint') }}</NAlert
        >
        <div v-if="session.preview.value?.legacy_snapshot_available" class="space-y-2">
          <NButton :loading="downloadBusy" @click="downloadLegacy">{{
            t('sourceStorage.legacyDownload')
          }}</NButton>
          <p class="text-sm text-lf-text-muted">
            {{ t('sourceStorage.expiresAt') }}：{{
              expiryLabel(session.preview.value.legacy_snapshot_expires_at)
            }}
          </p>
        </div>
        <NAlert v-if="downloadError" type="error">{{ downloadError }}</NAlert>
        <NAlert v-if="session.lastError.value?.error_code" type="warning" :bordered="false">{{
          storageTaskErrorMessage(session.lastError.value.error_code)
        }}</NAlert>
        <NAlert v-if="session.state.value === 'preview_ready'" type="info" :bordered="false">{{
          t('sourceStorage.prepared')
        }}</NAlert>
        <NAlert v-if="unresolved" type="info" :bordered="false">{{
          t('sourceStorage.pending')
        }}</NAlert>
        <NAlert v-if="session.state.value === 'invalidated'" type="warning" :bordered="false">{{
          t('sourceStorage.invalidated')
        }}</NAlert>
        <NAlert
          v-if="
            ['failed', 'expired', 'cancelled', 'read_only', 'blocked'].includes(session.state.value)
          "
          type="warning"
          :bordered="false"
          >{{ t(`sourceStorage.states.${session.state.value}`) }}</NAlert
        >
        <NAlert v-if="session.state.value === 'completed'" type="success" :bordered="false">{{
          t(viewState === 'refreshed' ? 'sourceStorage.completed' : 'sourceStorage.refreshPending')
        }}</NAlert>
        <NButton v-if="session.published.value && viewState !== 'refreshed'" @click="refreshView">{{
          t('sourceStorage.refreshView')
        }}</NButton>
        <NAlert v-if="session.refreshError.value" type="warning">{{
          t('sourceStorage.refreshFailed')
        }}</NAlert>
        <RouterLink
          v-if="session.taskId.value || unresolved"
          :to="taskHref"
          class="text-brand-600"
          >{{ t('sourceStorage.task') }}</RouterLink
        >
      </div>
      <template #footer>
        <div class="flex flex-wrap gap-2">
          <NButton
            :disabled="
              !writable ||
              !session.file.value ||
              busy ||
              !['selected', 'failed', 'invalidated', 'cancelled', 'expired'].includes(
                session.state.value,
              )
            "
            :loading="session.state.value === 'previewing'"
            @click="session.prepare"
            >{{
              t(
                session.state.value === 'selected'
                  ? 'sourceStorage.preview'
                  : 'sourceStorage.newPreview',
              )
            }}</NButton
          >
          <NButton
            v-if="session.taskId.value || unresolved"
            :disabled="busy"
            :loading="session.recovering.value"
            @click="session.recover"
            >{{ t('sourceStorage.recover') }}</NButton
          >
          <NButton
            type="primary"
            :disabled="
              !writable ||
              busy ||
              !session.taskSnapshotReady.value ||
              session.state.value !== 'preview_ready'
            "
            :loading="session.state.value === 'submitting'"
            @click="session.confirm"
            >{{ t('sourceStorage.confirm') }}</NButton
          >
        </div>
      </template>
    </NDrawerContent>
  </NDrawer>
</template>
