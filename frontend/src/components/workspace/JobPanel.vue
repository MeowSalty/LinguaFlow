<script setup lang="ts">
import { NAlert, NButton, NDataTable, NEmpty, NSelect } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { type ApiSchemas } from '@/api/client'
import { useJobColumns } from '@/composables/useJobColumns'
import { useProjectWorkspaceStore } from '@/stores/projectWorkspace'

type TranslationJob = ApiSchemas['TranslationJob']

const { t } = useI18n()
const workspace = useProjectWorkspaceStore()

defineProps<{
  projectId: number | null
}>()

const emit = defineEmits<{
  detail: [job: TranslationJob]
  cancel: [job: TranslationJob]
  retry: [job: TranslationJob]
  download: [job: TranslationJob]
}>()

const { jobColumns, jobStatusOptions } = useJobColumns({
  openJobDetail: (job) => emit('detail', job),
  cancelJob: (job) => emit('cancel', job),
  retryJob: (job) => emit('retry', job),
  downloadJob: (job) => emit('download', job),
})
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
        <div class="flex flex-wrap gap-3">
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
      :scroll-x="1320"
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
