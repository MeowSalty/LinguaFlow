<script setup lang="ts">
import { h } from 'vue'
import { NAlert, NDataTable, NDescriptions, NDescriptionsItem, NTag, NText } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { type ApiSchemas } from '@/api/client'
import type { SSEEvent } from '@/composables/useJobSSE'
import {
  formatDate,
  formatConfigValue,
  getJobStatusLabel,
  getJobTriggerLabel,
  getStageLabel,
} from '@/composables/useWorkspaceUtils'

import JobEventTimeline from './JobEventTimeline.vue'
import JobProgressCard from './JobProgressCard.vue'

type TranslationJob = ApiSchemas['TranslationJob']

const { t } = useI18n()

defineProps<{
  job: TranslationJob
  externalError?: string | null
  projectName?: string
  events?: SSEEvent[]
  syntheticEvents?: SSEEvent[]
  sseConnected?: boolean
}>()

const emit = defineEmits<{
  clearEvents: []
}>()
</script>

<template>
  <div class="space-y-5">
    <JobProgressCard :job="job" />

    <NAlert v-if="externalError" type="error" :bordered="false">
      {{ externalError }}
    </NAlert>
    <NAlert v-if="job.error_message" type="error" :bordered="false">
      {{ job.error_message }}
    </NAlert>

    <NDescriptions bordered label-placement="left" :column="2" size="small">
      <NDescriptionsItem v-if="projectName" :label="t('globalJobTracker.project')">
        {{ projectName }}
      </NDescriptionsItem>
      <NDescriptionsItem :label="t('workspace.job.columns.trigger')">
        {{ getJobTriggerLabel(job.trigger_type) }}
      </NDescriptionsItem>
      <NDescriptionsItem v-if="job.started_at" :label="t('workspace.job.columns.startedAt')">
        {{ formatDate(job.started_at) }}
      </NDescriptionsItem>
      <NDescriptionsItem :label="t('workspace.common.createdAt')">
        {{ formatDate(job.created_at) }}
      </NDescriptionsItem>
      <NDescriptionsItem v-if="job.updated_at" :label="t('workspace.common.updatedAt')">
        {{ formatDate(job.updated_at) }}
      </NDescriptionsItem>
      <NDescriptionsItem :label="t('workspace.job.form.sourceLang')">
        {{ formatConfigValue(job.translation_config?.source_lang) }}
      </NDescriptionsItem>
      <NDescriptionsItem :label="t('workspace.job.form.targetLang')">
        {{ formatConfigValue(job.translation_config?.target_lang) }}
      </NDescriptionsItem>
    </NDescriptions>

    <div>
      <div class="mb-3 text-sm font-medium text-lf-text-strong">
        {{ t('workspace.job.resourcesTitle') }}
      </div>
      <NDataTable
        :data="job.job_resources ?? []"
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

    <JobEventTimeline
      v-if="events"
      :events="events"
      :synthetic-events="syntheticEvents"
      :connected="sseConnected"
      @clear="emit('clearEvents')"
    />
  </div>
</template>
