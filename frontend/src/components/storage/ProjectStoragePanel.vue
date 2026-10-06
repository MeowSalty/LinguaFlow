<script setup lang="ts">
import { computed, onUnmounted, ref, shallowRef, watch } from 'vue'
import { NAlert, NButton, NCard, NRadioButton, NRadioGroup, NSkeleton, useDialog } from 'naive-ui'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import type { ApiSchemas } from '@/api/client-core'
import { bindProjectStorage, getProjectStorage } from '@/api/storage'
import { captureSession, isSessionCurrent, sessionGeneration } from '@/api/session-context'
import {
  storageAccessDenied,
  storageErrorMessage,
  storageRequestError,
  storageNeedsRefresh,
} from '@/api/storage-errors'
import { hasStorageRuntime } from '@/utils/storage-availability'
import { invalidateStorageSnapshots, subscribeStorageRefresh } from '@/utils/storage-snapshots'
import { fetchProject } from '@/api/projects'
import { useOrganizationsStore } from '@/stores/organizations'
import {
  isSafeStorageId,
  isStorageGeneration,
  storageActionAllowed,
  storageProjectWritable,
  storageTaskCommitted,
} from '@/utils/storage-contract'
import StorageTargetSelect from './StorageTargetSelect.vue'
import { createMigrationSession } from './migrationSession'

const props = defineProps<{ project: ApiSchemas['Project'] }>()
const emit = defineEmits<{ changed: [] }>()
const { t } = useI18n(),
  router = useRouter()
const dialog = useDialog(),
  organizations = useOrganizationsStore()
const space = shallowRef<ApiSchemas['ProjectStorage'] | null>(null)
const loading = ref(false),
  error = ref<string | null>(null),
  binding = ref(false)
const purpose = ref<'bind' | 'migrate'>('bind')
const selectedId = ref<number | null>(null),
  selected = shallowRef<ApiSchemas['StorageOption'] | null>(null)
const discovery = shallowRef<ApiSchemas['StorageOptions'] | null>(null)
const currentProject = computed(() =>
  space.value
    ? {
        ...props.project,
        storage_generation: space.value.storage_generation,
        storage_state: space.value.storage_state,
      }
    : props.project,
)
const targetContext = computed(() =>
  space.value
    ? { kind: 'project' as const, project: currentProject.value, purpose: purpose.value }
    : null,
)
const migration = shallowRef<ReturnType<typeof createMigrationSession> | null>(null)
const canSubmit = computed(
  () =>
    !loading.value &&
    !error.value &&
    hasStorageRuntime(space.value) &&
    !binding.value &&
    !migration.value?.busy.value &&
    !migration.value?.unknown.value &&
    (purpose.value !== 'migrate' || !migration.value?.hasOperation.value) &&
    !!selected.value?.selectable &&
    selected.value.space_id === selectedId.value &&
    storageActionAllowed(currentProject.value, purpose.value === 'bind' ? 'bind' : 'migration') &&
    isStorageGeneration(discovery.value?.storage_generation) &&
    discovery.value?.storage_generation === space.value?.storage_generation,
)
let sequence = 0,
  controller = new AbortController()
async function refresh() {
  controller.abort()
  controller = new AbortController()
  const request = ++sequence,
    session = captureSession(),
    id = props.project.id
  if (!isSafeStorageId(id)) return
  const current = () => request === sequence && isSessionCurrent(session) && id === props.project.id
  loading.value = true
  error.value = null
  try {
    const result = await getProjectStorage(id, { signal: controller.signal })
    if (current()) {
      space.value = result
      if (isSafeStorageId(result.migration_task_id))
        await migration.value?.recover(result.migration_task_id)
    }
  } catch (cause) {
    if (current()) {
      if (storageAccessDenied(cause)) space.value = null
      error.value = storageErrorMessage(cause)
    }
  } finally {
    if (current()) loading.value = false
  }
}
watch(
  [
    () => props.project.id,
    () => props.project.owner_user_id,
    () => props.project.owner_org_id,
    () => sessionGeneration.value,
  ],
  () => {
    space.value = null
    discovery.value = null
    selected.value = null
    migration.value?.dispose()
    migration.value = createMigrationSession({
      project: () => currentProject.value,
      available: () =>
        !loading.value &&
        !error.value &&
        hasStorageRuntime(space.value) &&
        !space.value.runtime.maintenance &&
        (migration.value?.hasOperation.value
          ? storageProjectWritable(currentProject.value)
          : storageActionAllowed(currentProject.value, 'migration')),
    })
    void refresh()
  },
  { immediate: true, flush: 'sync' },
)
watch([() => props.project.storage_generation, () => props.project.storage_state], () => {
  void refresh()
})
async function submit() {
  if (!canSubmit.value || !selected.value || !space.value) return
  const session = captureSession(),
    id = props.project.id,
    generation = space.value.storage_generation,
    target = selected.value,
    action = purpose.value
  binding.value = true
  error.value = null
  try {
    if (props.project.owner_org_id != null) {
      await organizations.refresh()
      if (organizations.error) throw storageRequestError({ status: 403 })
    }
    const [latestProject, latestStorage] = await Promise.all([
      fetchProject(id),
      getProjectStorage(id),
    ])
    if (!isSessionCurrent(session) || id !== props.project.id) return
    if (hasStorageRuntime(latestStorage) && latestStorage.runtime.maintenance)
      throw storageRequestError({ status: 409 }, { error_code: 'storage_maintenance' })
    if (
      !selected.value ||
      selected.value.space_id !== target.space_id ||
      !discovery.value ||
      !hasStorageRuntime(latestStorage) ||
      latestStorage.project_id !== id ||
      latestStorage.storage_generation !== generation ||
      latestProject.storage_generation !== generation ||
      latestStorage.storage_state !== 'active' ||
      !storageActionAllowed(latestProject, action === 'bind' ? 'bind' : 'migration')
    ) {
      space.value = latestStorage
      throw storageRequestError({ status: 409 }, { error_code: 'storage_generation_conflict' })
    }
    if (action === 'migrate') {
      if (!(await migration.value?.start(target, generation))) return
    } else
      await bindProjectStorage(id, { space_id: target.space_id, expected_generation: generation })
    if (isSessionCurrent(session) && id === props.project.id) {
      emit('changed')
      invalidateStorageSnapshots({ projectId: id })
      await refresh()
    }
  } catch (cause) {
    if (isSessionCurrent(session) && id === props.project.id) {
      error.value = storageErrorMessage(cause)
      if (storageNeedsRefresh(cause)) invalidateStorageSnapshots({ projectId: id })
    }
  } finally {
    binding.value = false
  }
}
function confirmSubmission() {
  if (!canSubmit.value) return
  const identity = [
    props.project.id,
    purpose.value,
    selectedId.value,
    space.value?.storage_generation,
  ].join(':')
  const session = captureSession()
  dialog.warning({
    title: t(purpose.value === 'bind' ? 'storage.rebind' : 'storage.migrate'),
    content: t(
      purpose.value === 'bind'
        ? 'storageProject.confirmBinding'
        : 'storageProject.confirmMigration',
    ),
    positiveText: t('common.confirm'),
    negativeText: t('common.cancel'),
    onPositiveClick: () => {
      if (
        isSessionCurrent(session) &&
        identity ===
          [props.project.id, purpose.value, selectedId.value, space.value?.storage_generation].join(
            ':',
          )
      )
        return submit()
    },
  })
}
async function recover() {
  await migration.value?.recover()
  if (migration.value?.task.value && storageTaskCommitted(migration.value.task.value))
    emit('changed')
  await refresh()
}
onUnmounted(() => {
  ++sequence
  controller.abort()
  migration.value?.dispose()
})
onUnmounted(
  subscribeStorageRefresh({
    scope: () => ({ projectId: props.project.id, organizationId: props.project.owner_org_id }),
    invalidate: () => {
      ++sequence
      controller.abort()
      controller = new AbortController()
      loading.value = true
      discovery.value = null
      selected.value = null
    },
    refresh,
  }),
)
</script>
<template>
  <NCard :title="t('storage.projectTitle')" size="small">
    <template #header-extra
      ><NButton size="small" :loading="loading" @click="refresh">{{
        t('storage.refresh')
      }}</NButton></template
    >
    <div class="space-y-4">
      <NAlert v-if="error" type="warning">{{ error }}</NAlert>
      <NSkeleton v-if="loading && !space" height="130px" />
      <template v-if="space">
        <dl class="grid grid-cols-2 gap-3 text-sm">
          <div>
            <dt class="text-lf-text-subtle">{{ t('storage.binding') }}</dt>
            <dd>{{ space.binding?.name ?? t('storageProject.unbound') }}</dd>
          </div>
          <div>
            <dt class="text-lf-text-subtle">{{ t('storage.generation') }}</dt>
            <dd>{{ space.storage_generation }}</dd>
          </div>
        </dl>
        <NAlert v-if="space.binding?.historical" type="info">{{
          t('storageProject.historicalBinding')
        }}</NAlert>
        <NAlert v-if="space.runtime?.deployment_enabled === false" type="info">{{
          t('storageProject.deploymentDisabled')
        }}</NAlert>
        <NAlert v-if="space.runtime?.maintenance" type="warning">{{
          t('storageProject.serviceMaintenance')
        }}</NAlert>
        <NAlert v-if="!hasStorageRuntime(space)" type="warning">{{
          t('storageProject.contractIncomplete')
        }}</NAlert>
        <NAlert v-if="space.storage_state !== 'active'" type="info">{{
          t('storageProject.projectBarrier')
        }}</NAlert>
        <template
          v-if="
            storageActionAllowed(currentProject, 'bind') ||
            storageActionAllowed(currentProject, 'migration')
          "
        >
          <NRadioGroup
            v-model:value="purpose"
            :disabled="migration?.busy.value || migration?.unknown.value || binding"
          >
            <NRadioButton value="bind">{{ t('storage.rebind') }}</NRadioButton
            ><NRadioButton value="migrate">{{ t('storage.migrate') }}</NRadioButton>
          </NRadioGroup>
          <p class="text-sm text-lf-text-muted">
            {{
              t(purpose === 'bind' ? 'storageProject.emptyBindingHint' : 'storage.migrationHint')
            }}
          </p>
          <StorageTargetSelect
            v-model:value="selectedId"
            :context="targetContext"
            :disabled="binding || migration?.busy.value || migration?.unknown.value"
            @loaded="discovery = $event"
            @selection="selected = $event"
          />
          <NButton
            type="primary"
            :disabled="!canSubmit"
            :loading="binding || migration?.busy.value"
            @click="confirmSubmission"
            >{{ t(purpose === 'bind' ? 'storage.rebind' : 'storage.migrate') }}</NButton
          >
          <NButton v-if="migration?.canStartNew.value" @click="migration.startNew()">{{
            t('storageProject.newMigration')
          }}</NButton>
        </template>
      </template>
      <NAlert v-if="migration?.error.value" type="warning">{{ migration.error.value }}</NAlert>
      <NAlert v-if="migration?.unknown.value" type="warning">{{
        t('storageProject.unknownResult')
      }}</NAlert>
      <p v-if="migration?.task.value" class="text-sm">
        {{
          t('storageProject.taskPhase', {
            id: migration.task.value.id,
            phase: migration.task.value.phase,
          })
        }}
      </p>
      <div class="flex flex-wrap gap-2">
        <NButton
          v-if="migration?.task.value || migration?.hasOperation.value"
          :loading="migration?.busy.value"
          @click="recover"
          >{{ t('storageProject.recoverOriginal') }}</NButton
        >
        <NButton
          @click="
            router.push({
              path: '/operations',
              query: { task_type: 'storage', project_id: String(project.id) },
            })
          "
          >{{ t('storage.projectTasks') }}</NButton
        >
      </div>
    </div>
  </NCard>
</template>
