<script setup lang="ts">
import { computed, ref, toRef } from 'vue'
import { NAlert, NButton, NDataTable, NEmpty, NIcon, NSelect, NSwitch } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { type ApiSchemas } from '@/api/client'
import { useJobColumns } from '@/composables/useJobColumns'
import { useJobPolling } from '@/composables/useJobPolling'
import { useGlobalJobTrackerStore } from '@/stores/globalJobTracker'
import { useProjectWorkspaceStore } from '@/stores/projectWorkspace'

type TranslationJob = ApiSchemas['TranslationJob']

const { t } = useI18n()
const workspace = useProjectWorkspaceStore()
const globalTracker = useGlobalJobTrackerStore()

const props = defineProps<{
  projectId: number | null
}>()

const emit = defineEmits<{
  detail: [job: TranslationJob]
  cancel: [job: TranslationJob]
  retry: [job: TranslationJob]
}>()

const { jobColumns, jobStatusOptions } = useJobColumns({
  openJobDetail: (job) => emit('detail', job),
  cancelJob: (job) => emit('cancel', job),
  retryJob: (job) => emit('retry', job),
})

// ── 自适应轮询：面板挂载时自动轮询运行中的任务 ──
const projectIdRef = toRef(props, 'projectId')
const autoRefreshEnabled = ref(true)
const detailDrawerOpen = computed(() => globalTracker.drawerJobId != null)
const pollingEnabled = computed(() => autoRefreshEnabled.value && !detailDrawerOpen.value)
const { isPolling } = useJobPolling({ projectId: projectIdRef, enabled: pollingEnabled })
</script>

<template>
  <div class="space-y-4 pt-3">
    <div class="rounded-xl border border-lf-border-soft bg-lf-surface-muted/60 p-4">
      <div class="mb-4 flex flex-col gap-1">
        <h3 class="text-base font-semibold text-lf-text-strong">
          {{ t('workspace.sections.jobs.title') }}
        </h3>
        <p class="text-sm text-lf-text-muted">
          {{ t('workspace.sections.jobs.description') }}
        </p>
      </div>
      <div class="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between">
        <NSelect
          v-model:value="workspace.jobStatusFilter"
          class="w-full sm:w-36"
          :options="jobStatusOptions"
        />
        <div class="flex items-center gap-3">
          <div class="flex items-center gap-2">
            <NSwitch v-model:value="autoRefreshEnabled" size="small" />
            <span class="whitespace-nowrap text-xs text-lf-text-muted">
              {{
                autoRefreshEnabled && isPolling
                  ? t('workspace.job.polling')
                  : autoRefreshEnabled
                    ? t('workspace.job.waitingJobs')
                    : t('workspace.job.autoRefresh')
              }}
            </span>
            <span
              v-if="autoRefreshEnabled && isPolling"
              class="inline-block h-1.5 w-1.5 animate-pulse rounded-full bg-green-500"
            />
            <span
              v-else-if="autoRefreshEnabled"
              class="inline-block h-1.5 w-1.5 rounded-full bg-gray-400"
            />
          </div>
          <NButton
            quaternary
            circle
            size="small"
            :loading="workspace.loadingJobs"
            :title="t('workspace.actions.refresh')"
            @click="projectId && workspace.loadJobs(projectId)"
          >
            <template #icon>
              <NIcon size="16"><IconCarbonRenew /></NIcon>
            </template>
          </NButton>
        </div>
      </div>
    </div>

    <NAlert v-if="workspace.jobsError" type="error" :bordered="false">
      {{ workspace.jobsError }}
    </NAlert>

    <NDataTable
      remote
      :columns="jobColumns"
      :data="workspace.jobs"
      :loading="workspace.loadingJobs"
      :row-key="(row: TranslationJob) => row.id"
      :row-props="
        (row: TranslationJob) => ({
          class: 'cursor-pointer',
          onClick: () => emit('detail', row),
        })
      "
      :scroll-x="1180"
    />
    <div v-if="workspace.jobsCursor" class="flex justify-center pt-3">
      <NButton :loading="workspace.loadingJobs" @click="workspace.loadJobs(projectId!, true)">
        {{ t('common.loadMore') }}
      </NButton>
    </div>
    <NEmpty
      v-if="!workspace.loadingJobs && workspace.jobs.length === 0"
      class="py-12"
      :description="t('workspace.job.empty')"
    />
  </div>
</template>
