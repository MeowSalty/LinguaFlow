<script setup lang="ts">
import { toRef } from 'vue'
import { NAlert, NButton, NDataTable, NEmpty, NSelect } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { type ApiSchemas } from '@/api/client'
import { useJobColumns } from '@/composables/useJobColumns'
import { useJobPolling } from '@/composables/useJobPolling'
import { useProjectWorkspaceStore } from '@/stores/projectWorkspace'

type TranslationJob = ApiSchemas['TranslationJob']

const { t } = useI18n()
const workspace = useProjectWorkspaceStore()

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
const { isPolling } = useJobPolling({ projectId: projectIdRef })
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
      <div class="flex flex-col gap-3 md:flex-row md:items-center md:justify-between">
        <NSelect
          v-model:value="workspace.jobStatusFilter"
          class="md:w-56"
          :options="jobStatusOptions"
        />
        <div class="flex flex-wrap items-center gap-3">
          <span v-if="isPolling" class="inline-flex items-center gap-1 text-xs text-lf-text-muted">
            <span class="inline-block h-1.5 w-1.5 animate-pulse rounded-full bg-green-500" />
            {{ t('workspace.job.polling') }}
          </span>
          <NButton
            secondary
            :loading="workspace.loadingJobs"
            @click="projectId && workspace.loadJobs(projectId)"
          >
            {{ t('workspace.actions.refresh') }}
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
