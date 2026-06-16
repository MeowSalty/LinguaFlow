<script setup lang="ts">
import {
  NAlert,
  NButton,
  NDataTable,
  NDescriptions,
  NDescriptionsItem,
  NDrawer,
  NDrawerContent,
  NEmpty,
  NProgress,
  NSpin,
  NTag,
} from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { type ApiSchemas } from '@/api/client'
import {
  formatDate,
  formatConfigValue,
  getJobProgress,
  getJobStatusLabel,
  getJobTriggerLabel,
  statusTagType,
} from '@/composables/useWorkspaceUtils'
import { useProjectWorkspaceStore } from '@/stores/projectWorkspace'

type TranslationJob = ApiSchemas['TranslationJob']

const { t } = useI18n()
const workspace = useProjectWorkspaceStore()

defineProps<{
  show: boolean
}>()

const emit = defineEmits<{
  'update:show': [value: boolean]
  download: [job: TranslationJob]
}>()
</script>

<template>
  <NDrawer
    :show="show"
    :width="720"
    placement="right"
    @update:show="(value: boolean) => emit('update:show', value)"
  >
    <NDrawerContent
      :title="
        workspace.selectedJob
          ? t('workspace.job.detailTitle', { id: workspace.selectedJob.id })
          : t('workspace.job.detailFallbackTitle')
      "
      closable
    >
      <NSpin :show="workspace.loadingJobDetail">
        <div v-if="workspace.selectedJob" class="space-y-5">
          <div class="grid grid-cols-1 gap-3 sm:grid-cols-3">
            <div class="rounded-lg bg-lf-surface-muted p-4">
              <div class="text-xs text-lf-text-muted">
                {{ t('workspace.job.columns.status') }}
              </div>
              <NTag class="mt-2" size="small" :type="statusTagType(workspace.selectedJob.status)">
                {{ getJobStatusLabel(workspace.selectedJob.status) }}
              </NTag>
            </div>
            <div class="rounded-lg bg-lf-surface-muted p-4">
              <div class="text-xs text-lf-text-muted">
                {{ t('workspace.job.columns.resources') }}
              </div>
              <div class="mt-2 text-lg font-semibold text-lf-text-strong">
                {{ workspace.selectedJob.completed_resources }}/{{
                  workspace.selectedJob.resource_count
                }}
              </div>
            </div>
            <div class="rounded-lg bg-lf-surface-muted p-4">
              <div class="text-xs text-lf-text-muted">
                {{ t('workspace.job.columns.segments') }}
              </div>
              <div class="mt-2 text-lg font-semibold text-lf-text-strong">
                {{ workspace.selectedJob.completed_segments }}/{{
                  workspace.selectedJob.total_segments
                }}
              </div>
            </div>
          </div>

          <NProgress
            type="line"
            :percentage="getJobProgress(workspace.selectedJob)"
            indicator-placement="inside"
            :processing="
              workspace.selectedJob.status === 'pending' ||
              workspace.selectedJob.status === 'running'
            "
          />

          <NAlert v-if="workspace.jobDetailError" type="error" :bordered="false">
            {{ workspace.jobDetailError }}
          </NAlert>
          <NAlert v-if="workspace.selectedJob.error_message" type="error" :bordered="false">
            {{ workspace.selectedJob.error_message }}
          </NAlert>

          <NDescriptions bordered :column="1" size="small">
            <NDescriptionsItem :label="t('workspace.job.columns.trigger')">
              {{ getJobTriggerLabel(workspace.selectedJob.trigger_type) }}
            </NDescriptionsItem>
            <NDescriptionsItem :label="t('workspace.common.createdAt')">
              {{ formatDate(workspace.selectedJob.created_at) }}
            </NDescriptionsItem>
            <NDescriptionsItem :label="t('workspace.common.updatedAt')">
              {{ formatDate(workspace.selectedJob.updated_at) }}
            </NDescriptionsItem>
            <NDescriptionsItem :label="t('workspace.job.form.sourceLang')">
              {{ formatConfigValue(workspace.selectedJob.translation_config?.source_lang) }}
            </NDescriptionsItem>
            <NDescriptionsItem :label="t('workspace.job.form.targetLang')">
              {{ formatConfigValue(workspace.selectedJob.translation_config?.target_lang) }}
            </NDescriptionsItem>
            <NDescriptionsItem :label="t('workspace.job.form.backendOrder')">
              <pre class="m-0 whitespace-pre-wrap text-xs leading-5">{{
                formatConfigValue(workspace.selectedJob.translation_config?.backend_order)
              }}</pre>
            </NDescriptionsItem>
          </NDescriptions>

          <div>
            <div class="mb-3 text-sm font-medium text-lf-text-strong">
              {{ t('workspace.job.resourcesTitle') }}
            </div>
            <NDataTable
              :data="workspace.selectedJob.job_resources ?? []"
              :columns="[
                {
                  title: t('workspace.resource.columns.name'),
                  key: 'name',
                  render: (row: ApiSchemas['TranslationJobResource']) =>
                    row.resource?.name || `#${row.resource_id}`,
                },
                {
                  title: t('workspace.job.columns.status'),
                  key: 'status',
                  render: (row: ApiSchemas['TranslationJobResource']) =>
                    getJobStatusLabel(row.status as TranslationJob['status']),
                },
                {
                  title: t('workspace.job.columns.segments'),
                  key: 'segments',
                  render: (row: ApiSchemas['TranslationJobResource']) =>
                    `${row.completed_segments}/${row.segment_count}`,
                },
                {
                  title: t('workspace.job.columns.error'),
                  key: 'error_message',
                  render: (row: ApiSchemas['TranslationJobResource']) => row.error_message || '-',
                },
              ]"
              :row-key="(row: ApiSchemas['TranslationJobResource']) => row.id"
              :scroll-x="720"
            />
          </div>
        </div>
        <NEmpty v-else :description="t('workspace.job.detailEmpty')" />
      </NSpin>
      <template #footer>
        <div class="flex flex-wrap justify-end gap-3">
          <NButton @click="emit('update:show', false)">{{ t('workspace.common.close') }}</NButton>
          <NButton
            v-if="workspace.selectedJob"
            :disabled="workspace.selectedJob.status !== 'completed'"
            :loading="workspace.downloadingKeys.includes(`job:${workspace.selectedJob.id}:all`)"
            type="primary"
            @click="workspace.selectedJob && emit('download', workspace.selectedJob)"
          >
            {{ t('workspace.common.download') }}
          </NButton>
        </div>
      </template>
    </NDrawerContent>
  </NDrawer>
</template>
