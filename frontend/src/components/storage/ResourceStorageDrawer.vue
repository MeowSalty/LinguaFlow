<script setup lang="ts">
import { computed, onUnmounted, ref, shallowRef, watch } from 'vue'
import {
  NAlert,
  NButton,
  NCard,
  NDrawer,
  NDrawerContent,
  NEmpty,
  NSkeleton,
  NTabPane,
  NTabs,
  NTag,
  useDialog,
  useMessage,
} from 'naive-ui'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import type { ApiSchemas } from '@/api/client-core'
import { fetchProject } from '@/api/projects'
import * as api from '@/api/storage'
import { captureSession, isSessionCurrent, sessionGeneration } from '@/api/session-context'
import { isAccessDenied } from '@/api/utils'
import { storageErrorMessage } from '@/api/storage-errors'
import { storageActionAllowed } from '@/utils/storage-contract'
import { useOperationsStore } from '@/stores/operations'
import { useOrganizationsStore } from '@/stores/organizations'
import { triggerBrowserDownload } from '@/composables/useWorkspaceUtils'
import StorageHealth from './StorageHealth.vue'
import StorageRepairDrawer from './StorageRepairDrawer.vue'
import { repairContextAllowed } from './repairSession'
import { createExportSession } from './exportSession'

const props = defineProps<{
  show: boolean
  projectId: number
  project: ApiSchemas['Project'] | null
  resource: ApiSchemas['Resource']
  beforeSavedContent?: () => Promise<boolean>
}>()
const emit = defineEmits<{ 'update:show': [value: boolean]; changed: [] }>()
const { t } = useI18n(),
  dialog = useDialog(),
  message = useMessage(),
  router = useRouter()
const operations = useOperationsStore()
const organizations = useOrganizationsStore()
const writeContextValid = ref(true)
async function prepareMutation() {
  const previous = props.project
  if (!previous || !props.show || !storageActionAllowed(previous, 'exportMutation')) return false
  const session = captureSession(),
    projectId = props.projectId,
    resourceId = props.resource.id
  if (previous.owner_org_id) {
    await organizations.refresh()
    if (organizations.error || !organizations.canWrite(previous.owner_org_id)) return false
  }
  const current = await fetchProject(projectId)
  if (
    !isSessionCurrent(session) ||
    props.projectId !== projectId ||
    props.resource.id !== resourceId ||
    !props.show
  )
    return false
  writeContextValid.value =
    current.storage_generation === previous.storage_generation &&
    storageActionAllowed(current, 'exportMutation')
  if (!writeContextValid.value) emit('changed')
  return writeContextValid.value
}
const versions = shallowRef<ApiSchemas['SourceVersion'][]>([])
const versionsLoading = ref(false),
  versionsLoaded = ref(false),
  versionsError = ref<string | null>(null)
const downloading = ref<number | null>(null)
const tab = ref('versions')
const repairVersion = shallowRef<ApiSchemas['SourceVersion'] | null>(null)
let sequence = 0,
  controller = new AbortController()
function newExportSession() {
  return createExportSession({
    projectId: props.projectId,
    resourceId: props.resource.id,
    beforeSavedContent: () => props.beforeSavedContent?.() ?? Promise.resolve(true),
    beforeMutation: prepareMutation,
    canMutate: () =>
      props.show &&
      writeContextValid.value &&
      storageActionAllowed(props.project, 'exportMutation'),
    subscribe: (projectId, taskId, receive, fail) =>
      operations.subscribeTask(
        { task_type: 'storage', project_id: projectId, task_id: String(taskId) },
        receive,
        fail,
      ),
    changed: () => emit('changed'),
  })
}
const exports = shallowRef(newExportSession())
const exportState = computed(() => ({
  items: exports.value.items.value,
  loading: exports.value.loading.value,
  loaded: exports.value.loaded.value,
  busy: exports.value.busy.value,
  unknown: exports.value.unknown.value,
  error: exports.value.error.value,
  task: exports.value.task.value,
  published: exports.value.published.value,
  deletions: exports.value.deletions.value,
}))
const mutationReady = computed(
  () =>
    props.show && writeContextValid.value && storageActionAllowed(props.project, 'exportMutation'),
)
async function refreshVersions() {
  const request = ++sequence,
    session = captureSession()
  const current = () => request === sequence && isSessionCurrent(session)
  versionsLoading.value = true
  versionsError.value = null
  try {
    const result = await api.listSourceVersions(props.projectId, props.resource.id, {
      signal: controller.signal,
    })
    if (current()) {
      versions.value = result.items
      versionsLoaded.value = true
    }
  } catch (cause) {
    if (current()) {
      if (isAccessDenied(cause)) versions.value = []
      versionsError.value = storageErrorMessage(cause)
    }
  } finally {
    if (current()) versionsLoading.value = false
  }
}
async function refresh() {
  await Promise.all([refreshVersions(), exports.value.refresh()])
}
watch(
  () => [props.project?.storage_generation, props.project?.storage_state],
  () => {
    writeContextValid.value = true
  },
)
async function download(item: ApiSchemas['ExportArtifact']) {
  if (!exports.value.canDownload(item) || downloading.value !== null) return
  const session = captureSession(),
    project = props.projectId,
    resource = props.resource.id
  const current = () =>
    isSessionCurrent(session) && project === props.projectId && resource === props.resource.id
  downloading.value = item.id
  try {
    const file = await api.downloadExportArtifact(project, item.id, { signal: controller.signal })
    if (current()) triggerBrowserDownload(file, item.filename)
  } catch (cause) {
    if (current()) message.error(storageErrorMessage(cause))
  } finally {
    if (current()) downloading.value = null
  }
}
function rebuild(item: ApiSchemas['ExportArtifact']) {
  if (!mutationReady.value || !item.rebuildable) return
  const session = exports.value
  dialog.info({
    title: t('storage.rebuild'),
    content: t('storage.rebuildHint'),
    positiveText: t('storage.create'),
    negativeText: t('storage.cancel'),
    onPositiveClick: () =>
      exports.value === session ? session.start(item.id).then(() => undefined) : undefined,
  })
}
function remove(item: ApiSchemas['ExportArtifact']) {
  if (!mutationReady.value) return
  const session = exports.value
  dialog.warning({
    title: t('storage.delete'),
    content: t('storage.deleteConfirm', { name: item.filename }),
    positiveText: t('storage.delete'),
    negativeText: t('storage.cancel'),
    onPositiveClick: async () => {
      if (exports.value !== session) return
      if (await session.remove(item.id)) message.success(t('storage.deleteAccepted'))
    },
  })
}
watch(
  () => [props.projectId, props.resource.id, sessionGeneration.value],
  () => {
    ++sequence
    controller.abort()
    controller = new AbortController()
    versions.value = []
    versionsLoaded.value = false
    versionsError.value = null
    versionsLoading.value = false
    downloading.value = null
    repairVersion.value = null
    writeContextValid.value = true
    exports.value.dispose()
    exports.value = newExportSession()
    if (props.show) void refresh()
  },
)
watch(
  () => props.show,
  (show) => {
    if (show) void refresh()
  },
  { immediate: true },
)
onUnmounted(() => {
  ++sequence
  controller.abort()
  exports.value.dispose()
})
</script>
<template>
  <NDrawer
    :show="show"
    :width="640"
    placement="right"
    class="max-w-full"
    @update:show="(value: boolean) => emit('update:show', value)"
  >
    <NDrawerContent :title="resource.path" closable>
      <div class="mb-4 flex justify-end">
        <NButton :loading="versionsLoading || exportState.loading" @click="refresh">{{
          t('storage.refresh')
        }}</NButton>
      </div>
      <NTabs v-model:value="tab" type="line" animated>
        <NTabPane name="versions" :tab="t('storage.versions')">
          <div class="space-y-4">
            <NAlert v-if="versionsError" type="warning">{{ versionsError }}</NAlert>
            <p class="text-sm text-lf-text-muted">{{ t('storage.repairHint') }}</p>
            <NSkeleton v-if="versionsLoading && !versionsLoaded" height="180px" />
            <NEmpty
              v-else-if="versionsLoaded && !versionsError && !versions.length"
              :description="t('storage.versionsEmpty')"
            />
            <NCard
              v-for="version in versions"
              :key="version.id"
              :title="t('storage.version', { id: version.id })"
              size="small"
            >
              <template #header-extra
                ><NTag size="small" :type="version.current ? 'info' : 'default'">{{
                  t(version.current ? 'storage.current' : 'storage.historical')
                }}</NTag></template
              >
              <div class="mb-3 flex flex-wrap gap-2">
                <StorageHealth :value="version.health" /><NTag size="small">{{
                  t(
                    version.verification_state === 'verified'
                      ? 'storage.verified'
                      : 'storage.unverified',
                  )
                }}</NTag>
              </div>
              <NAlert
                v-if="version.verification_state === 'legacy_unverified'"
                type="warning"
                class="mb-3"
                >{{ t('storage.legacy') }}</NAlert
              >
              <dl class="grid grid-cols-2 gap-3 text-sm">
                <div>
                  <dt class="text-lf-text-subtle">{{ t('storage.format') }}</dt>
                  <dd>{{ version.format }}</dd>
                </div>
                <div>
                  <dt class="text-lf-text-subtle">{{ t('storage.parser') }}</dt>
                  <dd>{{ version.parser_version }}</dd>
                </div>
                <div>
                  <dt class="text-lf-text-subtle">{{ t('storage.size') }}</dt>
                  <dd>
                    {{
                      version.size == null
                        ? t('storage.unknown')
                        : t('storage.bytes', { value: version.size.toLocaleString() })
                    }}
                  </dd>
                </div>
                <div>
                  <dt class="text-lf-text-subtle">{{ t('storage.locationGeneration') }}</dt>
                  <dd>{{ version.location_generation }}</dd>
                </div>
                <div v-if="version.sha256" class="col-span-2">
                  <dt class="text-lf-text-subtle">{{ t('storage.digest') }}</dt>
                  <dd class="break-all font-mono text-xs">{{ version.sha256 }}</dd>
                </div>
              </dl>
              <NButton
                class="mt-4"
                :disabled="
                  !project || !repairContextAllowed({ project, resourceId: resource.id, version })
                "
                @click="repairVersion = version"
                >{{ t('storage.repair') }}</NButton
              >
            </NCard>
          </div>
        </NTabPane>
        <NTabPane name="exports" :tab="t('storage.exports')">
          <div class="space-y-4">
            <p class="text-sm text-lf-text-muted">{{ t('storage.exportHint') }}</p>
            <NButton
              type="primary"
              :disabled="!mutationReady || exportState.busy || exportState.unknown"
              :loading="exportState.busy"
              @click="exports.start()"
              >{{ t('storage.createExport') }}</NButton
            >
            <NAlert v-if="!mutationReady" type="info">{{ t('storage.exportPending') }}</NAlert>
            <NAlert v-if="exportState.error" type="warning">{{ exportState.error }}</NAlert>
            <NAlert v-if="exportState.published" type="success">{{
              t('storageManagement.exportPublished')
            }}</NAlert>
            <div v-if="exportState.unknown" class="space-y-2">
              <p class="text-sm text-lf-text-muted">
                {{ t('storageManagement.exportRecoveryHint') }}
              </p>
              <NButton :loading="exportState.busy" @click="exports.recover()">{{
                t('storageManagement.recover')
              }}</NButton>
            </div>
            <NButton
              v-if="exportState.task"
              secondary
              @click="
                router.push({
                  path: '/operations',
                  query: {
                    task_type: 'storage',
                    project_id: String(projectId),
                    task_id: String(exportState.task.id),
                  },
                })
              "
              >{{ t('storage.taskLink', { id: exportState.task.id }) }}</NButton
            >
            <NSkeleton v-if="exportState.loading && !exportState.loaded" height="140px" />
            <NEmpty
              v-else-if="exportState.loaded && !exportState.error && !exportState.items.length"
              :description="t('storage.exportsEmpty')"
            />
            <NCard
              v-for="item in exportState.items"
              :key="item.id"
              :title="item.filename"
              size="small"
            >
              <template #header-extra
                ><NTag size="small">{{ t(`storage.exportStates.${item.status}`) }}</NTag></template
              >
              <p class="text-xs text-lf-text-muted">
                {{ t('storage.version', { id: item.source_revision_id }) }} ·
                {{ t('storage.renderer') }} {{ item.renderer_version }}
              </p>
              <div v-if="exportState.deletions[item.id]" class="mt-3 space-y-2">
                <NAlert
                  :type="exportState.deletions[item.id]?.status === 'done' ? 'success' : 'info'"
                  :bordered="false"
                >
                  {{
                    t(`storageManagement.deletionStates.${exportState.deletions[item.id]!.status}`)
                  }}
                </NAlert>
                <p class="text-xs text-lf-text-muted">{{ t('storageManagement.deletionHint') }}</p>
                <NButton
                  v-if="exportState.deletions[item.id]?.taskId"
                  size="small"
                  @click="
                    router.push({
                      path: '/operations',
                      query: {
                        task_type: 'storage',
                        project_id: String(projectId),
                        task_id: String(exportState.deletions[item.id]!.taskId),
                      },
                    })
                  "
                >
                  {{ t('storage.taskLink', { id: exportState.deletions[item.id]!.taskId! }) }}
                </NButton>
              </div>
              <div class="mt-3 flex flex-wrap gap-2">
                <NButton
                  :disabled="!exports.canDownload(item) || downloading !== null"
                  :loading="downloading === item.id"
                  @click="download(item)"
                  >{{ t('storage.download') }}</NButton
                ><NButton
                  :disabled="!mutationReady || !item.rebuildable || exportState.busy"
                  :title="t(item.rebuildable ? 'storage.rebuildHint' : 'storage.cannotRebuild')"
                  @click="rebuild(item)"
                  >{{ t('storage.rebuild') }}</NButton
                ><NButton
                  type="error"
                  secondary
                  :disabled="!mutationReady || item.status === 'deleted' || exportState.busy"
                  @click="remove(item)"
                  >{{ t('storage.delete') }}</NButton
                >
              </div>
            </NCard>
          </div>
        </NTabPane>
      </NTabs>
    </NDrawerContent>
  </NDrawer>
  <StorageRepairDrawer
    v-if="project && repairVersion"
    :show="show && !!repairVersion"
    :project="project"
    :resource-id="resource.id"
    :version="repairVersion"
    @update:show="
      (value) => {
        if (!value) repairVersion = null
      }
    "
    @changed="refreshVersions"
  />
</template>
