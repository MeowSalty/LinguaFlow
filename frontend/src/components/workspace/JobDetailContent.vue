<script setup lang="ts">
import { computed, h } from 'vue'
import { NAlert, NDataTable, NTag, NText } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { type ApiSchemas } from '@/api/client'
import type { SSEEvent } from '@/composables/sseShared'
import {
  formatDate,
  formatConfigValue,
  getJobStatusLabel,
  getJobTriggerLabel,
  getStageLabel,
  statusTagType,
} from '@/composables/useWorkspaceUtils'

import JobEventTimeline from './JobEventTimeline.vue'
import JobProgressCard from './JobProgressCard.vue'

type Job = ApiSchemas['Job']
type JobResource = ApiSchemas['JobResource']

const { t } = useI18n()

const props = defineProps<{
  job: Job
  externalError?: string | null
  projectName?: string
  events?: SSEEvent[]
  sseConnected?: boolean
  hasOlder?: boolean
  loadingOlder?: boolean
  jobEnded?: boolean
}>()

const emit = defineEmits<{
  clearEvents: []
  loadOlder: []
}>()

const warnedResources = computed(() =>
  (props.job.job_resources ?? []).filter((r) => !!r.warning_message?.trim()),
)

// 资源级进度数值：运行期优先 weighted_*（跨轮累加），回退去重 completed_segments/segment_count
const getResourceCompleted = (row: JobResource): number => {
  if (row.weighted_total != null && row.weighted_total > 0) {
    return row.weighted_completed ?? 0
  }
  return row.completed_segments
}

const resourceColumns = computed(() => [
  {
    title: t('workspace.resource.columns.name'),
    key: 'name',
    minWidth: 200,
    ellipsis: { tooltip: true },
    render: (row: JobResource) => row.resource?.name || `#${row.resource_id}`,
  },
  {
    title: t('workspace.job.columns.status'),
    key: 'status',
    width: 80,
    render: (row: JobResource) =>
      h(
        NTag,
        {
          size: 'tiny',
          round: true,
          type: statusTagType(row.status as Job['status']),
          bordered: false,
        },
        { default: () => getJobStatusLabel(row.status as Job['status']) },
      ),
  },
  {
    title: t('workspace.job.columns.stage'),
    key: 'stage',
    width: 120,
    render: (row: JobResource) => {
      if (!row.current_stage) return h(NText, { depth: 3 }, { default: () => '-' })
      const label = getStageLabel(row.current_stage)
      if (row.stage_total) {
        return h('div', { class: 'flex items-center gap-1.5' }, [
          h(
            NTag,
            { size: 'tiny', round: true, bordered: false, type: 'info' },
            { default: () => label },
          ),
          h(
            'span',
            { class: 'text-xs text-lf-text-muted font-mono tabular-nums' },
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
    width: 120,
    render: (row: JobResource) => {
      const skipped = row.skipped_segments ?? 0
      const completed = getResourceCompleted(row)
      if (skipped > 0) {
        return h('span', { class: 'font-mono tabular-nums whitespace-nowrap text-xs' }, [
          h('span', { class: 'text-lf-text-strong' }, `${completed}`),
          h('span', { class: 'text-lf-text-muted' }, ` +${skipped} `),
          h('span', { class: 'text-lf-text-muted' }, `/ ${row.segment_count}`),
        ])
      }
      return h(
        'span',
        { class: 'font-mono tabular-nums whitespace-nowrap text-xs' },
        { default: () => `${completed}/${row.segment_count}` },
      )
    },
  },
  {
    title: t('workspace.job.columns.remark'),
    key: 'remark',
    minWidth: 160,
    ellipsis: { tooltip: true },
    render: (row: JobResource) => {
      if (row.error_message) {
        return h('span', { class: 'text-xs text-red-500' }, { default: () => row.error_message })
      }
      if (row.warning_message) {
        return h(
          'span',
          { class: 'text-xs text-amber-600 dark:text-amber-400' },
          { default: () => row.warning_message },
        )
      }
      return h(NText, { depth: 3 }, { default: () => '-' })
    },
  },
])
</script>

<template>
  <div class="space-y-3">
    <JobProgressCard :job="job" />

    <NAlert v-if="externalError" type="error" :bordered="false">
      {{ externalError }}
    </NAlert>
    <NAlert v-if="job.error_message" type="error" :bordered="false">
      {{ job.error_message }}
    </NAlert>
    <NAlert v-if="warnedResources.length > 0" type="warning" :bordered="false">
      <div class="space-y-1">
        <div class="text-sm font-medium">
          {{ t('workspace.job.warnings.summary', { count: warnedResources.length }) }}
        </div>
        <div
          v-for="resource in warnedResources"
          :key="resource.id"
          class="text-xs leading-relaxed text-lf-text-muted"
        >
          <span class="font-medium text-lf-text-strong">
            {{ resource.resource?.name || `#${resource.resource_id}` }}
          </span>
          <span class="mx-1 text-lf-text-muted">·</span>
          <span>{{ resource.warning_message }}</span>
        </div>
      </div>
    </NAlert>

    <!-- KV Grid 详情 -->
    <div
      class="grid grid-cols-[repeat(auto-fit,minmax(120px,1fr))] gap-x-8 gap-y-1 rounded-lg border border-lf-border-soft bg-lf-surface-muted/40 p-3"
    >
      <div v-if="projectName">
        <div class="text-xs text-lf-text-muted">{{ t('globalJobTracker.project') }}</div>
        <div class="text-sm font-medium">{{ projectName }}</div>
      </div>
      <div>
        <div class="text-xs text-lf-text-muted">{{ t('workspace.job.columns.trigger') }}</div>
        <div class="text-sm font-medium">{{ getJobTriggerLabel(job.trigger_type) }}</div>
      </div>
      <div v-if="job.started_at">
        <div class="text-xs text-lf-text-muted">{{ t('workspace.job.columns.startedAt') }}</div>
        <div class="text-sm font-medium font-mono tabular-nums">
          {{ formatDate(job.started_at) }}
        </div>
      </div>
      <div>
        <div class="text-xs text-lf-text-muted">{{ t('workspace.common.createdAt') }}</div>
        <div class="text-sm font-medium font-mono tabular-nums">
          {{ formatDate(job.created_at) }}
        </div>
      </div>
      <div v-if="job.updated_at">
        <div class="text-xs text-lf-text-muted">{{ t('workspace.common.updatedAt') }}</div>
        <div class="text-sm font-medium font-mono tabular-nums">
          {{ formatDate(job.updated_at) }}
        </div>
      </div>
      <div>
        <div class="text-xs text-lf-text-muted">{{ t('workspace.job.form.sourceLang') }}</div>
        <div class="text-sm font-medium">
          {{ formatConfigValue(job.execution_config?.source_lang) }}
        </div>
      </div>
      <div>
        <div class="text-xs text-lf-text-muted">{{ t('workspace.job.form.targetLang') }}</div>
        <div class="text-sm font-medium">
          {{ formatConfigValue(job.execution_config?.target_lang) }}
        </div>
      </div>
    </div>

    <div>
      <div
        class="mb-2 border-l-2 border-brand-500 pl-2 text-xs font-semibold uppercase tracking-wider text-lf-text-muted"
      >
        {{ t('workspace.job.resourcesTitle') }}
      </div>
      <NDataTable
        class="rounded-lg overflow-hidden"
        :data="job.job_resources ?? []"
        :columns="resourceColumns"
        :row-key="(row: JobResource) => row.id"
        :scroll-x="720"
      />
    </div>

    <JobEventTimeline
      v-if="events"
      :events="events"
      :connected="sseConnected"
      :has-older="hasOlder"
      :loading-older="loadingOlder"
      :job-ended="jobEnded"
      @clear="emit('clearEvents')"
      @load-older="emit('loadOlder')"
    />
  </div>
</template>
