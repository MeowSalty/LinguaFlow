<script setup lang="ts">
import { h, watch } from 'vue'
import {
  NAlert,
  NButton,
  NDataTable,
  NDescriptions,
  NDescriptionsItem,
  NDrawer,
  NDrawerContent,
  NEmpty,
  NSpin,
  NTag,
  NText,
} from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { type ApiSchemas } from '@/api/client'
import {
  formatDate,
  formatConfigValue,
  getJobStatusLabel,
  getJobTriggerLabel,
  getStageLabel,
} from '@/composables/useWorkspaceUtils'
import { useProjectWorkspaceStore } from '@/stores/projectWorkspace'
import { useTranslationJobStore } from '@/stores/translationJob'

import JobEventTimeline from './JobEventTimeline.vue'
import JobProgressCard from './JobProgressCard.vue'

type TranslationJob = ApiSchemas['TranslationJob']

const { t } = useI18n()
const workspace = useProjectWorkspaceStore()
const jobStore = useTranslationJobStore()

defineProps<{
  show: boolean
}>()

const emit = defineEmits<{
  'update:show': [value: boolean]
}>()

const handleRefreshEvents = (): void => {
  if (workspace.selectedJob) {
    void jobStore.loadEvents(workspace.selectedJob.id)
  }
}

// ── 选中任务变化时自动加载事件（非终态才加载）──
watch(
  () => jobStore.selectedJob?.id,
  (newId) => {
    const job = jobStore.selectedJob
    if (newId != null && job && ['pending', 'running'].includes(job.status)) {
      void jobStore.loadEvents(newId)
    }
  },
  { immediate: true },
)
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
          <!-- 增强 2：JobProgressCard 替换原有统计卡片 + 进度条 -->
          <JobProgressCard :job="workspace.selectedJob" />

          <NAlert v-if="workspace.jobDetailError" type="error" :bordered="false">
            {{ workspace.jobDetailError }}
          </NAlert>
          <NAlert v-if="workspace.selectedJob.error_message" type="error" :bordered="false">
            {{ workspace.selectedJob.error_message }}
          </NAlert>

          <NDescriptions bordered label-placement="left" :column="2" size="small">
            <NDescriptionsItem :label="t('workspace.job.columns.trigger')">
              {{ getJobTriggerLabel(workspace.selectedJob.trigger_type) }}
            </NDescriptionsItem>
            <NDescriptionsItem
              v-if="workspace.selectedJob.started_at"
              :label="t('workspace.job.columns.startedAt')"
            >
              {{ formatDate(workspace.selectedJob.started_at) }}
            </NDescriptionsItem>
            <NDescriptionsItem :label="t('workspace.common.createdAt')">
              {{ formatDate(workspace.selectedJob.created_at) }}
            </NDescriptionsItem>
            <NDescriptionsItem
              v-if="workspace.selectedJob.updated_at"
              :label="t('workspace.common.updatedAt')"
            >
              {{ formatDate(workspace.selectedJob.updated_at) }}
            </NDescriptionsItem>
            <NDescriptionsItem :label="t('workspace.job.form.sourceLang')">
              {{ formatConfigValue(workspace.selectedJob.translation_config?.source_lang) }}
            </NDescriptionsItem>
            <NDescriptionsItem :label="t('workspace.job.form.targetLang')">
              {{ formatConfigValue(workspace.selectedJob.translation_config?.target_lang) }}
            </NDescriptionsItem>
          </NDescriptions>

          <!-- 增强 4：资源明细表格（含阶段列） -->
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
                  minWidth: 200,
                  ellipsis: { tooltip: true },
                  render: (row: ApiSchemas['TranslationJobResource']) =>
                    row.resource?.name || `#${row.resource_id}`,
                },
                {
                  title: t('workspace.job.columns.status'),
                  key: 'status',
                  width: 80,
                  render: (row: ApiSchemas['TranslationJobResource']) =>
                    getJobStatusLabel(row.status as TranslationJob['status']),
                },
                {
                  title: t('workspace.job.columns.stage'),
                  key: 'stage',
                  width: 120,
                  render: (row: ApiSchemas['TranslationJobResource']) => {
                    if (!row.current_stage) return h(NText, { depth: 3 }, { default: () => '-' })
                    const label = getStageLabel(row.current_stage)
                    if (row.stage_total) {
                      return h('div', { class: 'flex flex-col gap-0.5' }, [
                        h(
                          NTag,
                          { size: 'tiny', bordered: false, type: 'info' },
                          { default: () => label },
                        ),
                        h(
                          'span',
                          { class: 'text-xs text-lf-text-muted tabular-nums' },
                          {
                            default: () => `${row.stage_completed ?? 0}/${row.stage_total}`,
                          },
                        ),
                      ])
                    }
                    return label
                  },
                },
                {
                  title: t('workspace.job.columns.segments'),
                  key: 'segments',
                  width: 70,
                  render: (row: ApiSchemas['TranslationJobResource']) =>
                    `${row.completed_segments}/${row.segment_count}`,
                },
                {
                  title: t('workspace.job.columns.error'),
                  key: 'error_message',
                  minWidth: 120,
                  ellipsis: { tooltip: true },
                  render: (row: ApiSchemas['TranslationJobResource']) => row.error_message || '-',
                },
              ]"
              :row-key="(row: ApiSchemas['TranslationJobResource']) => row.id"
              :scroll-x="720"
            />
          </div>

          <!-- 增强 5：事件时间线（仅 running/pending 时展示） -->
          <JobEventTimeline
            v-if="
              workspace.selectedJob && ['pending', 'running'].includes(workspace.selectedJob.status)
            "
            :events="jobStore.events"
            :loading="jobStore.loadingEvents"
            @refresh="handleRefreshEvents"
          />
        </div>
        <NEmpty v-else :description="t('workspace.job.detailEmpty')" />
      </NSpin>
      <template #footer>
        <div class="flex flex-wrap justify-end gap-3">
          <NButton @click="emit('update:show', false)">{{ t('workspace.common.close') }}</NButton>
        </div>
      </template>
    </NDrawerContent>
  </NDrawer>
</template>
