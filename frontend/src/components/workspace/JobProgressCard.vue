<script setup lang="ts">
import { computed } from 'vue'
import { NIcon, NTag } from 'naive-ui'

import type { ApiSchemas } from '@/api/client'
import {
  calculateJobETA,
  calculateJobSpeed,
  formatETA,
  formatJobSpeed,
  getJobProgressText,
  getJobStatusLabel,
  statusTagType,
} from '@/composables/useWorkspaceUtils'
import { t } from '@/i18n'

const props = defineProps<{
  job: ApiSchemas['Job']
}>()

const completedPct = computed(() => {
  if (props.job.status === 'completed') return 100
  if (props.job.status === 'failed' || props.job.status === 'cancelled') return 0
  const { total_segments, completed_segments } = props.job.progress
  if (total_segments <= 0) return 0
  return Math.round((completed_segments / total_segments) * 100)
})

const completedBarPct = computed(() => {
  const { total_segments, completed_segments } = props.job.progress
  if (total_segments <= 0) return 0
  return Math.round((completed_segments / total_segments) * 100)
})

const skippedPct = computed(() => {
  const { total_segments, skipped_segments } = props.job.progress
  if (total_segments <= 0) return 0
  return Math.round((skipped_segments / total_segments) * 100)
})

const hasFailures = computed(() => props.job.progress.failed_resources > 0)

const failedPct = computed(() => {
  if (!hasFailures.value) return 0
  const { total_segments, completed_segments, skipped_segments } = props.job.progress
  if (total_segments <= 0) return 0
  const remaining = total_segments - completed_segments - skipped_segments
  return remaining > 0 ? Math.round((remaining / total_segments) * 100) : 0
})

const isTerminal = computed(() => ['completed', 'failed', 'cancelled'].includes(props.job.status))

const showSkipped = computed(() => props.job.progress.skipped_segments > 0)

const showStatsRow = computed(() => showSkipped.value || hasFailures.value)

const barColor = computed(() => {
  if (props.job.status === 'completed' && !hasFailures.value) return 'bg-green-500'
  if (props.job.status === 'failed') return 'bg-red-500'
  return 'bg-brand-500'
})

const etaText = computed(() => {
  const seconds = calculateJobETA(props.job)
  return formatETA(seconds)
})

const speedText = computed(() => {
  const speed = calculateJobSpeed(props.job)
  return formatJobSpeed(speed)
})
</script>

<template>
  <div
    class="rounded-xl border border-lf-border-soft bg-linear-to-br from-lf-surface to-lf-surface-muted p-4 space-y-3"
    :class="{
      'border-l-3 border-brand-500': job.status === 'running',
      'border-l-3 border-green-500': job.status === 'completed' && !hasFailures,
      'border-l-3 border-amber-500': job.status === 'completed' && hasFailures,
      'border-l-3 border-red-500': job.status === 'failed',
    }"
  >
    <!-- 顶部信息行：左侧标签 + 右侧大号百分比 -->
    <div class="flex items-center justify-between">
      <div class="flex flex-wrap items-center gap-2">
        <!-- 状态标签 -->
        <NTag size="tiny" round :type="statusTagType(job.status)">
          {{ getJobStatusLabel(job.status) }}
        </NTag>

        <!-- 队列位置（pending 时显示） -->
        <NTag
          v-if="job.status === 'pending' && job.progress.queue_position != null"
          size="tiny"
          round
          :bordered="false"
          type="warning"
        >
          <template #icon>
            <NIcon size="12">
              <IconCarbonCircleDash />
            </NIcon>
          </template>
          {{ getJobProgressText(job) }}
        </NTag>
      </div>

      <!-- 大号进度百分比 -->
      <span class="text-2xl font-mono font-bold text-brand-500"> {{ completedPct }}% </span>
    </div>

    <!-- 主进度条（自定义堆叠条） -->
    <div class="space-y-1">
      <div class="text-xs text-lf-text-muted">{{ getJobProgressText(job) }}</div>
      <div class="relative h-1.5 w-full overflow-hidden rounded-full bg-lf-border/60">
        <!-- 已完成段 -->
        <div
          class="absolute inset-y-0 left-0 transition-all duration-300"
          :class="[
            barColor,
            job.status === 'running' ? 'animate-pulse' : '',
            showSkipped || (isTerminal && hasFailures) ? 'rounded-l-full' : 'rounded-full',
          ]"
          :style="{ width: `${completedBarPct}%` }"
        />
        <!-- 失败段（紧接已完成段右侧） -->
        <div
          v-if="isTerminal && hasFailures && failedPct > 0"
          class="absolute inset-y-0 bg-red-400 transition-all duration-300"
          :class="showSkipped ? '' : 'rounded-r-full'"
          :style="{ left: `${completedBarPct}%`, width: `${failedPct}%` }"
        />
        <!-- 跳过段（最后） -->
        <div
          v-if="showSkipped"
          class="absolute inset-y-0 rounded-r-full bg-lf-text-muted/40 transition-all duration-300"
          :style="{
            left: `${completedBarPct + (isTerminal && hasFailures ? failedPct : 0)}%`,
            width: `${skippedPct}%`,
          }"
        />
      </div>
    </div>

    <!-- 统计摘要行 -->
    <div v-if="showStatsRow" class="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
      <span class="flex items-center gap-1 text-lf-text-muted">
        <span class="inline-block h-2 w-2 rounded-full bg-brand-500" />
        {{ t('workspace.job.stats.completed') }}
        <span class="font-mono tabular-nums font-medium text-lf-text-strong">
          {{ job.progress.completed_segments }}
        </span>
      </span>
      <span v-if="hasFailures" class="flex items-center gap-1 text-lf-text-muted">
        <span class="inline-block h-2 w-2 rounded-full bg-red-400" />
        {{ t('workspace.job.stats.failed') }}
        <span class="font-mono tabular-nums font-medium text-lf-text-strong">
          {{ job.progress.failed_resources }}
        </span>
      </span>
      <span v-if="showSkipped" class="flex items-center gap-1 text-lf-text-muted">
        <span class="inline-block h-2 w-2 rounded-full bg-lf-text-muted/60" />
        {{ t('workspace.job.stats.skipped') }}
        <span class="font-mono tabular-nums font-medium text-lf-text-strong">
          {{ job.progress.skipped_segments }}
        </span>
      </span>
      <span class="flex items-center gap-1 text-lf-text-muted">
        {{ t('workspace.job.stats.total') }}
        <span class="font-mono tabular-nums font-medium text-lf-text-strong">
          {{ job.progress.total_segments }}
        </span>
      </span>
    </div>

    <!-- ETA 与速度行：网格卡片布局 -->
    <div v-if="job.status === 'running' && (etaText || speedText)" class="grid grid-cols-2 gap-2">
      <div
        v-if="etaText"
        class="flex items-center gap-1.5 rounded-md bg-lf-surface/60 px-2.5 py-1.5"
      >
        <NIcon size="14" class="text-lf-text-muted">
          <IconCarbonTime />
        </NIcon>
        <div class="flex flex-col">
          <span class="text-[10px] text-lf-text-muted">ETA</span>
          <span class="font-mono tabular-nums text-sm text-lf-text-strong">{{ etaText }}</span>
        </div>
      </div>
      <div
        v-if="speedText"
        class="flex items-center gap-1.5 rounded-md bg-lf-surface/60 px-2.5 py-1.5"
      >
        <div class="flex flex-col">
          <span class="text-[10px] text-lf-text-muted">速度</span>
          <span class="font-mono tabular-nums text-sm text-lf-text-strong">{{ speedText }}</span>
        </div>
      </div>
    </div>
  </div>
</template>
