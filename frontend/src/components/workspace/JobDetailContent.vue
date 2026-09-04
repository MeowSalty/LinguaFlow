<script setup lang="ts">
import { computed, h, onBeforeUnmount, ref } from 'vue'
import { NAlert, NDataTable, NTag, NText, NTooltip } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import { type ApiSchemas } from '@/api/client'
import type { SSEEvent } from '@/composables/sseShared'
import {
  formatDate,
  formatConfigValue,
  getJobStatusLabel,
  getJobTriggerLabel,
  getStageLabel,
  getResourceRound,
  getResourceWorkTotals,
  getRoundColumns,
  getRoundError,
  roundCellView,
  statusTagType,
} from '@/composables/useWorkspaceUtils'

import JobEventTimeline from './JobEventTimeline.vue'
import JobProgressCard from './JobProgressCard.vue'

type Job = ApiSchemas['Job']
type JobResource = ApiSchemas['JobResource']

const { t } = useI18n()

// 移动端断点（width < 640px）：使用原生 matchMedia，避免引入额外依赖
const isMobile = ref(false)
if (typeof window !== 'undefined' && typeof window.matchMedia === 'function') {
  const mql = window.matchMedia('(max-width: 639px)')
  const update = () => {
    isMobile.value = mql.matches
  }
  update()
  mql.addEventListener('change', update)
  onBeforeUnmount(() => mql.removeEventListener('change', update))
}

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

// ── 轮次矩阵（资源×轮次）──

/** 轮次列定义（跨资源按 round_index 求并集；同一任务共享同一轮次序列） */
const roundColumnsDef = computed(() => getRoundColumns(props.job))

/** 矩阵单元格：字形极简（✓ / x/y / · / ✗ / –），详情收进悬停 tooltip */
const renderRoundCell = (row: JobResource, roundIndex: number, mode: string) => {
  const round = getResourceRound(row, roundIndex)
  if (!round) {
    // 遗留终态任务（矩阵重构前创建）无轮次明细
    return h(
      NTooltip,
      { trigger: 'hover', placement: 'top' },
      {
        trigger: () => h('span', { class: 'text-lf-text-subtle' }, '–'),
        default: () => t('workspace.job.round.legacyHint'),
      },
    )
  }

  const view = roundCellView(round.status, round.segment_completed, round.segment_total)
  const content = h(
    'span',
    { class: ['inline-flex items-center gap-1 whitespace-nowrap text-xs', view.class] },
    [
      ...(view.pulse
        ? [
            h('span', {
              class: 'inline-block h-1.5 w-1.5 animate-pulse rounded-full bg-brand-500',
            }),
          ]
        : []),
      view.text,
    ],
  )

  const lines: string[] = [
    t('workspace.job.round.roundLabel', { index: roundIndex + 1 }),
    `${getStageLabel(mode)} · ${t(`workspace.job.round.status.${round.status}`)}`,
  ]
  if (round.status === 'skipped') {
    lines.push(t('workspace.job.round.skippedHint'))
  } else if (round.segment_total > 0) {
    lines.push(
      t('workspace.job.round.segments', {
        completed: round.segment_completed,
        total: round.segment_total,
      }),
    )
  }
  if (round.started_at) {
    lines.push(t('workspace.job.round.startedAt', { time: formatDate(round.started_at) }))
  }
  if (round.finished_at) {
    lines.push(t('workspace.job.round.finishedAt', { time: formatDate(round.finished_at) }))
  }
  if (round.error_message) {
    lines.push(round.error_message)
  }

  return h(
    NTooltip,
    { trigger: 'hover', placement: 'top' },
    {
      trigger: () => content,
      default: () =>
        h(
          'div',
          { class: 'space-y-0.5' },
          lines.map((line, idx) => h('div', { key: idx }, line)),
        ),
    },
  )
}

const roundColumns = computed(() =>
  roundColumnsDef.value.map((col) => ({
    key: `round-${col.roundIndex}`,
    width: 76,
    align: 'center' as const,
    title: () =>
      h(
        NTooltip,
        { trigger: 'hover', placement: 'top' },
        {
          trigger: () =>
            h(
              'span',
              { class: 'cursor-help text-xs font-medium text-lf-text-muted' },
              getStageLabel(col.mode),
            ),
          default: () => t('workspace.job.round.roundLabel', { index: col.roundIndex + 1 }),
        },
      ),
    render: (row: JobResource) => renderRoundCell(row, col.roundIndex, col.mode),
  })),
)

const resourceColumns = computed(() => {
  const base = [
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
  ]

  if (isMobile.value) return base

  return [
    ...base,
    ...roundColumns.value,
    {
      title: t('workspace.job.columns.workload'),
      key: 'workload',
      width: 110,
      render: (row: JobResource) => {
        const skipped = row.skipped_segments ?? 0
        const { completed, total } = getResourceWorkTotals(row)
        if (skipped > 0) {
          return h('span', { class: 'font-mono tabular-nums whitespace-nowrap text-xs' }, [
            h('span', { class: 'text-lf-text-strong' }, `${completed}`),
            h('span', { class: 'text-lf-text-muted' }, ` +${skipped} `),
            h('span', { class: 'text-lf-text-muted' }, `/ ${total}`),
          ])
        }
        return h(
          'span',
          { class: 'font-mono tabular-nums whitespace-nowrap text-xs' },
          { default: () => `${completed}/${total}` },
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
        // 资源自身无错误时回退最近失败轮次的错误信息
        const roundError = getRoundError(row)
        if (roundError) {
          return h('span', { class: 'text-xs text-red-500' }, { default: () => roundError })
        }
        return h(NText, { depth: 3 }, { default: () => '-' })
      },
    },
  ]
})

// 桌面端横向滚动宽度：名称 + 状态 + 轮次列 + 工作量 + 备注
const tableScrollX = computed(() => 200 + 80 + roundColumnsDef.value.length * 76 + 110 + 160)
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
        :scroll-x="isMobile ? undefined : tableScrollX"
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
