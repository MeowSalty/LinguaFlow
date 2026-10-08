<script setup lang="ts">
import { computed, onUnmounted, ref, shallowRef, watch } from 'vue'
import { NAlert, NButton, NDrawer, NDrawerContent } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import type { ApiSchemas } from '@/api/client-core'
import { listSourceVersions } from '@/api/storage'
import { captureSession, isSessionCurrent, sessionGeneration } from '@/api/session-context'
import { storageErrorMessage, storageRequestError } from '@/api/storage-errors'
import { fetchProject } from '@/api/projects'
import { useOrganizationsStore } from '@/stores/organizations'
import { isStorageGeneration } from '@/utils/storage-contract'
import { useProjectStorageSnapshot } from '@/composables/useProjectStorageSnapshot'
import StorageTargetSelect from './StorageTargetSelect.vue'
import {
  createRepairSession,
  repairContextAllowed,
  repairProjectSnapshotMatches,
} from './repairSession'

const props = defineProps<{
  show: boolean
  project: ApiSchemas['Project']
  resourceId: number
  version?: ApiSchemas['SourceVersion']
  taskId?: number
  sourceRevisionId?: number
}>()
const emit = defineEmits<{ 'update:show': [value: boolean]; changed: [] }>()
const { t } = useI18n()
const organizations = useOrganizationsStore()
const storageSnapshot = useProjectStorageSnapshot(
  () => props.project,
  () => props.show,
)
const resolvedVersion = shallowRef<ApiSchemas['SourceVersion'] | null>(null)
const readError = ref<string | null>(null)
const context = computed(() =>
  resolvedVersion.value
    ? { project: props.project, resourceId: props.resourceId, version: resolvedVersion.value }
    : null,
)
const session = shallowRef<ReturnType<typeof createRepairSession> | null>(null)
const selected = shallowRef<ApiSchemas['StorageOption'] | null>(null)
const discovery = shallowRef<ApiSchemas['StorageOptions'] | null>(null)
const selectedId = ref<number | null>(null)
const fileInput = ref<HTMLInputElement | null>(null)
let sequence = 0
const targetContext = computed(() =>
  context.value && repairContextAllowed(context.value)
    ? {
        kind: 'project' as const,
        project: props.project,
        purpose: 'repair' as const,
        sourceRevisionId: context.value.version.id,
      }
    : null,
)
const canStart = computed(
  () =>
    !session.value?.hasOperation.value &&
    !!session.value?.file.value &&
    !!selected.value &&
    repairContextAllowed(context.value) &&
    storageSnapshot.ready.value &&
    !storageSnapshot.value.value?.runtime.maintenance &&
    isStorageGeneration(discovery.value?.storage_generation) &&
    discovery.value?.storage_generation === props.project.storage_generation,
)
watch(
  [
    () => props.show,
    () => props.project.id,
    () => props.project.owner_user_id,
    () => props.project.owner_org_id,
    () => props.resourceId,
    () => props.version?.id,
    () => props.taskId,
    () => props.sourceRevisionId,
    () => sessionGeneration.value,
  ],
  async () => {
    const request = ++sequence,
      scope = captureSession()
    session.value?.dispose()
    session.value = null
    resolvedVersion.value = null
    selected.value = null
    discovery.value = null
    readError.value = null
    if (!props.show) return
    try {
      const versions = await listSourceVersions(props.project.id, props.resourceId)
      if (request !== sequence || !isSessionCurrent(scope)) return
      const id = props.version?.id ?? props.sourceRevisionId
      resolvedVersion.value = versions.items.find((item) => item.id === id) ?? null
      if (!resolvedVersion.value) return
      session.value = createRepairSession({
        context: () => context.value,
        available: () =>
          props.show &&
          storageSnapshot.ready.value &&
          !storageSnapshot.value.value?.runtime.maintenance &&
          repairContextAllowed(context.value) &&
          (!!session.value?.hasOperation.value ||
            !!props.taskId ||
            (!!selected.value && !!discovery.value)),
        changed: () => emit('changed'),
        beforeMutation: async (expected) => {
          const owner = captureSession()
          if (expected.project.owner_org_id != null) {
            await organizations.refresh()
            if (organizations.error) throw storageRequestError({ status: 403 })
          }
          const [latest] = await Promise.all([
            fetchProject(expected.project.id),
            storageSnapshot.refresh(),
          ])
          if (!isSessionCurrent(owner) || !props.show || request !== sequence) return false
          if (!repairProjectSnapshotMatches(expected.project, latest))
            throw storageRequestError(
              { status: 409 },
              { error_code: 'storage_generation_conflict' },
            )
          return storageSnapshot.ready.value && !storageSnapshot.value.value?.runtime.maintenance
        },
      })
      if (props.taskId) await session.value.recover(props.taskId)
    } catch (cause) {
      if (request === sequence && isSessionCurrent(scope))
        readError.value = storageErrorMessage(cause)
    }
  },
  { immediate: true, flush: 'sync' },
)
function chooseFile(event: Event) {
  const input = event.target as HTMLInputElement
  session.value?.selectFile(input.files?.[0] ?? null)
  input.value = ''
}
async function start() {
  if (!canStart.value || !selected.value || !discovery.value || !session.value) return
  await session.value.start(selected.value, discovery.value.storage_generation!)
}
onUnmounted(() => {
  ++sequence
  session.value?.dispose()
})
</script>
<template>
  <NDrawer :show="show" :width="480" @update:show="emit('update:show', $event)">
    <NDrawerContent :title="t('storageProject.repairTitle')" closable>
      <div class="space-y-4">
        <NAlert type="info">{{ t('storageProject.repairHint') }}</NAlert>
        <NAlert v-if="readError" type="warning">{{ readError }}</NAlert>
        <NAlert v-if="storageSnapshot.error.value" type="warning">{{
          storageSnapshot.error.value
        }}</NAlert>
        <NAlert v-if="storageSnapshot.value.value?.runtime.maintenance" type="warning">{{
          t('storageProject.serviceMaintenance')
        }}</NAlert>
        <NAlert v-if="resolvedVersion?.verification_state === 'legacy_unverified'" type="warning">{{
          t('storageProject.legacyRepairBlocked')
        }}</NAlert>
        <template v-if="session">
          <p>{{ t('storageProject.repairVersion', { id: resolvedVersion?.id }) }}</p>
          <NAlert v-if="session.error.value" type="warning">{{ session.error.value }}</NAlert>
          <NAlert v-if="session.unknown.value" type="warning">{{
            t('storageProject.unknownResult')
          }}</NAlert>
          <NAlert v-if="session.completed.value" type="success">{{
            t('storageProject.repairCompleted')
          }}</NAlert>
          <template v-if="!session.task.value && !props.taskId">
            <StorageTargetSelect
              v-model:value="selectedId"
              :context="targetContext"
              :disabled="session.busy.value || session.unknown.value"
              @loaded="discovery = $event"
              @selection="selected = $event"
            />
          </template>
          <input ref="fileInput" type="file" class="hidden" @change="chooseFile" />
          <NButton
            :disabled="
              session.busy.value || !repairContextAllowed(context) || session.completed.value
            "
            @click="fileInput?.click()"
            >{{ t('storageProject.chooseRepairFile') }}</NButton
          >
          <p v-if="session.file.value" class="text-sm break-all">{{ session.file.value.name }}</p>
          <div class="flex flex-wrap gap-2">
            <NButton
              v-if="!session.task.value && !props.taskId && !session.unknown.value"
              type="primary"
              :loading="session.busy.value"
              :disabled="!canStart"
              @click="start"
              >{{ t('storageProject.createRepair') }}</NButton
            >
            <NButton
              v-if="session.task.value"
              type="primary"
              :disabled="!session.canUpload.value"
              :loading="session.busy.value"
              @click="session.upload"
              >{{ t('storageProject.uploadOriginal') }}</NButton
            >
            <NButton
              v-if="session.task.value || session.hasOperation.value || props.taskId"
              :loading="session.busy.value"
              @click="session.recover()"
              >{{ t('storageProject.recoverOriginal') }}</NButton
            >
          </div>
          <p v-if="session.task.value" class="text-sm text-lf-text-muted">
            {{
              t('storageProject.taskPhase', {
                id: session.task.value.id,
                phase: session.task.value.phase,
              })
            }}
          </p>
        </template>
      </div>
    </NDrawerContent>
  </NDrawer>
</template>
