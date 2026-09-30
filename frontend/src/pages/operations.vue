<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import type { LocationQueryRaw } from 'vue-router'
import type { Operation } from '@/api/operations'
import { useOperationsStore } from '@/stores/operations'
import { usePreferencesStore } from '@/stores/preferences'
import { useGlobalJobTrackerStore } from '@/stores/globalJobTracker'
import { parseOperationQuery, safeTaskNumber, type OperationLocator } from '@/utils/operationQuery'
import { formatDateTime } from '@/utils/datetime'
import OperationCounts from '@/components/operations/OperationCounts.vue'
import OperationList from '@/components/operations/OperationList.vue'
import GlossaryTaskDetailDrawer from '@/components/operations/GlossaryTaskDetailDrawer.vue'

const { t } = useI18n(),
  route = useRoute(),
  router = useRouter()
const operations = useOperationsStore(),
  preferences = usePreferencesStore(),
  tracker = useGlobalJobTrackerStore()
const invalid = ref<string | null>(null),
  advanced = ref(false),
  syncLocator = shallowRef<OperationLocator | null>(null)
let detailKey = ''
const detach = operations.attachList()
onScopeDispose(() => {
  detach()
  tracker.closeDetail()
})
const parsed = computed(() => {
  try {
    return parseOperationQuery(route.query, preferences.defaultTaskState)
  } catch {
    return null
  }
})
const filters = computed(() => parsed.value?.filters ?? {})
const options = (keys: string[]) =>
  keys.map((value) => ({ value, label: t(`operations.${value}`) }))
const types = computed(() => options(['translation', 'glossary_sync']))
const states = computed(() => [
  ...['active', 'terminal', 'all'].map((value) => ({
    value: `state:${value}`,
    label: t(`operations.${value}`),
  })),
  ...['pending', 'running', 'paused', 'completed', 'failed', 'cancelled'].map((value) => ({
    value: `status:${value}`,
    label: t(`operations.${value}`),
  })),
])
const stateValue = computed(() =>
  filters.value.status
    ? `status:${filters.value.status}`
    : `state:${filters.value.state ?? preferences.defaultTaskState}`,
)
const dateRange = computed<[number, number] | null>(() =>
  filters.value.updated_from && filters.value.updated_before
    ? [Date.parse(filters.value.updated_from), Date.parse(filters.value.updated_before)]
    : null,
)
const update = (changes: LocationQueryRaw): void => {
  void router.replace({ path: '/operations', query: { ...route.query, ...changes } })
}
const setType = (value: string | null): void =>
  update({
    task_type: value ?? undefined,
    trigger_type: value === 'translation' ? route.query.trigger_type : undefined,
    task_id: undefined,
    job_id: undefined,
  })
const setState = (value: string): void => {
  const [kind, state] = value.split(':')
  update({
    status: kind === 'status' ? state : undefined,
    state: kind === 'state' ? state : undefined,
  })
}
const setDate = (value: [number, number] | null): void =>
  update({
    updated_from: value ? new Date(value[0]).toISOString() : undefined,
    updated_before: value ? new Date(value[1]).toISOString() : undefined,
  })
const open = (operation: Operation): void => {
  void router.push({
    path: '/operations',
    query: {
      ...route.query,
      task_type: operation.task_type,
      task_id: operation.task_id,
      project_id: operation.project_id,
      job_id: undefined,
      trigger_type: operation.task_type === 'translation' ? route.query.trigger_type : undefined,
    },
  })
}
const closeSync = (): void => {
  syncLocator.value = null
  update({ task_id: undefined, job_id: undefined })
}
watch(
  () => [route.query, preferences.defaultTaskState] as const,
  async () => {
    const result = parsed.value
    if (!result) {
      invalid.value = t('operations.invalidLink')
      syncLocator.value = null
      tracker.closeDetail()
      detailKey = ''
      return
    }
    invalid.value = null
    void operations.setFilters(result.filters)
    const key = JSON.stringify(result.locator)
    if (key === detailKey) return
    detailKey = key
    tracker.closeDetail()
    syncLocator.value = null
    if (result.locator?.task_type === 'translation') {
      try {
        await tracker.openDetail(safeTaskNumber(result.locator.task_id), result.locator.project_id)
        if (
          result.locator.project_id != null &&
          tracker.detailJob &&
          tracker.detailJob.project_id !== result.locator.project_id
        ) {
          tracker.closeDetail()
          invalid.value = t('operations.inaccessible')
        }
      } catch {
        invalid.value = t('operations.unsafeId')
      }
    } else if (result.locator) syncLocator.value = result.locator
  },
  { immediate: true },
)
</script>
<template>
  <div class="lf-page mx-auto w-full max-w-350">
    <PageHeader :title="t('operations.title')" :subtitle="t('operations.subtitle')">
      <template #actions
        ><NButton secondary :loading="operations.listLoading" @click="operations.refresh()">{{
          t('operations.refresh')
        }}</NButton></template
      >
    </PageHeader>
    <p v-if="operations.listUpdatedAt" class="text-xs text-lf-text-muted">
      {{ t('operations.lastUpdated') }} ·
      {{ formatDateTime(operations.listUpdatedAt, { dateStyle: 'short', timeStyle: 'medium' }) }}
    </p>
    <OperationCounts
      :summary="operations.filteredSummary"
      :loading="operations.listLoading"
      :error="operations.filteredSummaryError"
    />
    <p class="text-xs text-lf-text-muted">{{ t('operations.summaryNote') }}</p>
    <div class="lf-panel space-y-3 p-4">
      <div class="grid gap-3 sm:grid-cols-2 lg:grid-cols-4">
        <NSelect
          :value="filters.task_type ?? null"
          :options="types"
          clearable
          :placeholder="t('operations.allTypes')"
          :aria-label="t('operations.type')"
          @update:value="setType"
        />
        <NSelect
          :value="stateValue"
          :options="states"
          :aria-label="t('operations.status')"
          @update:value="setState"
        />
        <NInputNumber
          :value="filters.project_id ?? null"
          :min="1"
          :max="Number.MAX_SAFE_INTEGER"
          :show-button="false"
          clearable
          :placeholder="t('operations.projectId')"
          :aria-label="t('operations.projectId')"
          @update:value="(value: number | null) => update({ project_id: value ?? undefined })"
        />
        <div class="flex gap-2">
          <NButton quaternary @click="advanced = !advanced">{{
            t('operations.moreFilters')
          }}</NButton
          ><NButton
            quaternary
            @click="router.replace({ path: '/operations', query: { state: 'active' } })"
            >{{ t('operations.clearFilters') }}</NButton
          >
        </div>
      </div>
      <div v-if="advanced" class="grid gap-3 md:grid-cols-2">
        <NSelect
          :value="filters.trigger_type ?? null"
          :options="options(['manual', 'file_update', 'glossary_change', 'web_edit'])"
          :disabled="filters.task_type !== 'translation'"
          clearable
          :placeholder="t('operations.allSources')"
          :aria-label="t('operations.source')"
          @update:value="(value) => update({ trigger_type: value ?? undefined })"
        />
        <NDatePicker
          type="datetimerange"
          :value="dateRange"
          clearable
          :aria-label="t('operations.range')"
          @update:value="setDate"
        />
      </div>
    </div>
    <NAlert v-if="invalid" type="error" :bordered="false">{{ invalid }}</NAlert>
    <template v-else>
      <NAlert v-if="operations.listError" type="warning" :bordered="false"
        >{{ operations.items.length ? t('operations.stale') + ' · ' : ''
        }}{{ operations.listError }}</NAlert
      >
      <OperationList :items="operations.items" :loading="operations.listLoading" @open="open" />
      <div class="flex justify-end gap-3 text-xs text-lf-text-muted">
        <NButton
          v-if="operations.nextCursor"
          :loading="operations.listLoading"
          @click="operations.loadList(true)"
          >{{ t('operations.loadMore') }}</NButton
        >
      </div>
    </template>
    <GlossaryTaskDetailDrawer :locator="syncLocator" @close="closeSync" />
  </div>
</template>
