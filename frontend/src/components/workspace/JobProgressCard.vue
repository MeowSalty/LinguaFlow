<script setup lang="ts">
import { computed } from 'vue'
import { NIcon, NTag, NTooltip } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import type { ApiSchemas } from '@/api/client'
import {
  aggregateRound,
  calculateJobETA,
  calculateJobSpeed,
  formatETA,
  formatEtaCompletionTime,
  formatJobSpeed,
  getJobProgress,
  getJobProgressNumbers,
  getJobProgressText,
  getJobStatusLabel,
  getRoundColumns,
  getStageLabel,
  roundCellView,
  statusTagType,
} from '@/composables/useWorkspaceUtils'
import StackedProgressBar from '@/components/common/StackedProgressBar.vue'

const { t } = useI18n()

const props = defineProps<{
  job: ApiSchemas['Job']
}>()

// 主进度条与大号百分比共用工作量口径（progress_completed/progress_total）；
// 非完成状态一律显示真实比例——失败/取消可从断点续跑，"停在哪"更有价值
const completedPct = computed(() => getJobProgress(props.job))

// 资源级段落统计（任务级段落数字段已随矩阵重构移除）
const skippedSegments = computed(() =>
  (props.job.job_resources ?? []).reduce((sum, r) => sum + (r.skipped_segments ?? 0), 0),
)

const totalSegments = computed(() =>
  (props.job.job_resources ?? []).reduce((sum, r) => sum + r.segment_count, 0),
)

const skippedPct = computed(() => {
  if (totalSegments.value <= 0) return 0
  return Math.round((skippedSegments.value / totalSegments.value) * 100)
})

const hasFailures = computed(() => props.job.progress.failed_resources > 0)

const warnedResourceCount = computed(
  () => (props.job.job_resources ?? []).filter((r) => !!r.warning_message?.trim()).length,
)

const hasWarnings = computed(() => warnedResourceCount.value > 0)

// 堆叠条三段宽度以 100% 收敛：skipped 从剩余空间截取（段落口径的比例仅作示意），
// 终态失败的红色段取剩余减去 skipped——三段总宽恒 ≤100%，不会互相挤占或被裁剪
const remainingPct = computed(() => Math.max(100 - completedPct.value, 0))

const skippedBarPct = computed(() =>
  showSkipped.value ? Math.min(skippedPct.value, remainingPct.value) : 0,
)

const failedBarPct = computed(() =>
  hasFailures.value && isTerminal.value ? Math.max(remainingPct.value - skippedBarPct.value, 0) : 0,
)

const isTerminal = computed(() => ['completed', 'failed', 'cancelled'].includes(props.job.status))

const showSkipped = computed(() => skippedSegments.value > 0)

const showStatsRow = computed(() => showSkipped.value || hasFailures.value || hasWarnings.value)

const completedCount = computed(() => getJobProgressNumbers(props.job).completed)

// 轮次管线条：资源×轮次矩阵的聚合投影，非完成态展示（paused/failed/cancelled 可定位停点）
const roundStrip = computed(() =>
  getRoundColumns(props.job).map((col) => {
    const agg = aggregateRound(props.job, col.roundIndex)
    return {
      label: getStageLabel(col.mode),
      view: agg ? roundCellView(agg.status, agg.completed, agg.total) : null,
    }
  }),
)

const showRoundStrip = computed(
  () => props.job.status !== 'completed' && roundStrip.value.length > 0,
)

const barTone = computed<'brand' | 'success' | 'warning'>(() => {
  if (props.job.status === 'completed' && !hasFailures.value && !hasWarnings.value) return 'success'
  if (props.job.status === 'completed' && hasWarnings.value && !hasFailures.value) return 'warning'
  return 'brand'
})

const etaSeconds = computed(() => calculateJobETA(props.job))

const etaText = computed(() => formatETA(etaSeconds.value))

const etaCompletionText = computed(() => formatEtaCompletionTime(etaSeconds.value))

const speedText = computed(() => {
  const speed = calculateJobSpeed(props.job)
  return formatJobSpeed(speed)
})
</script>

<template>
  <div
    class="rounded-lf-card border border-lf-border-soft bg-lf-surface p-4 space-y-3"
    :class="{
      'border-l-3 border-brand-500': job.status === 'running',
      'border-l-3 border-lf-success': job.status === 'completed' && !hasFailures && !hasWarnings,
      'border-l-3 border-lf-warning':
        job.status === 'paused' ||
        job.status === 'cancelled' ||
        (job.status === 'completed' && (hasFailures || hasWarnings)),
      'border-l-3 border-lf-danger': job.status === 'failed',
    }"
  >
    <!-- 顶部信息行：左侧标签 + 右侧大号百分比 -->
    <div class="flex items-center justify-between">
      <div class="flex flex-wrap items-center gap-2">
        <!-- 状态标签 -->
        <NTag size="tiny" round :type="statusTagType(job.status)">
          {{ getJobStatusLabel(job.status) }}
        </NTag>

        <NTag v-if="hasWarnings" size="tiny" round :bordered="false" type="warning">
          {{ t('workspace.job.warnings.badge', { count: warnedResourceCount }) }}
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

      <!-- 大号进度百分比：悬停解释分母揭示机制（不常驻显示） -->
      <NTooltip trigger="hover" placement="top-end">
        <template #trigger>
          <span class="cursor-help text-2xl font-mono font-bold text-brand-500">
            {{ completedPct }}%
          </span>
        </template>
        {{ t('workspace.job.progress.percentTooltip') }}
      </NTooltip>
    </div>

    <!-- 主进度条（StackedProgressBar：主段 + 失败段 + 跳过段） -->
    <div class="space-y-1">
      <!-- 终态显示工作量计数（状态语义交给 NTag）；运行/排队沿用进度文案 -->
      <div class="text-xs text-lf-text-muted">
        <template v-if="isTerminal">
          {{
            t('workspace.job.progress.workload', {
              completed: completedCount,
              total: job.progress.progress_total,
            })
          }}
          <span class="ml-1 text-[10px] text-lf-text-subtle">
            {{ t('workspace.job.stats.unitWorkload') }}
          </span>
        </template>
        <template v-else>
          {{ getJobProgressText(job) }}
        </template>
      </div>
      <StackedProgressBar
        :value="completedPct"
        :error-value="failedBarPct"
        :skipped-value="skippedBarPct"
        :error="job.status === 'failed'"
        :tone="barTone"
        :class="job.status === 'running' ? 'animate-pulse' : ''"
      />
    </div>

    <!-- 轮次管线条：各轮跨资源聚合进度 -->
    <div v-if="showRoundStrip" class="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs">
      <template v-for="(item, i) in roundStrip" :key="i">
        <span v-if="i > 0" class="text-lf-text-subtle">·</span>
        <span class="flex items-center gap-1 whitespace-nowrap">
          <span
            v-if="item.view?.pulse"
            class="inline-block h-1.5 w-1.5 shrink-0 animate-pulse rounded-full bg-brand-500"
          />
          <span class="text-lf-text-muted">{{ item.label }}</span>
          <span class="font-medium" :class="item.view?.class">{{ item.view?.text }}</span>
        </span>
      </template>
    </div>

    <!-- 统计摘要行：各计数单位口径不同（段×轮 / 段落 / 资源），以小号标注区分 -->
    <div v-if="showStatsRow" class="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs">
      <span class="flex items-center gap-1 text-lf-text-muted">
        <span class="inline-block h-2 w-2 rounded-full bg-brand-500" />
        {{ t('workspace.job.stats.completed') }}
        <span class="font-mono tabular-nums font-medium text-lf-text-strong">
          {{ completedCount }}
        </span>
        <span class="text-[10px] text-lf-text-subtle">
          {{ t('workspace.job.stats.unitWorkload') }}
        </span>
      </span>
      <span v-if="hasFailures" class="flex items-center gap-1 text-lf-text-muted">
        <span class="inline-block h-2 w-2 rounded-full bg-lf-danger" />
        {{ t('workspace.job.stats.failed') }}
        <span class="font-mono tabular-nums font-medium text-lf-text-strong">
          {{ job.progress.failed_resources }}
        </span>
        <span class="text-[10px] text-lf-text-subtle">
          {{ t('workspace.job.stats.unitResources') }}
        </span>
      </span>
      <span v-if="hasWarnings" class="flex items-center gap-1 text-lf-text-muted">
        <span class="inline-block h-2 w-2 rounded-full bg-lf-warning" />
        {{ t('workspace.job.stats.warned') }}
        <span class="font-mono tabular-nums font-medium text-lf-text-strong">
          {{ warnedResourceCount }}
        </span>
        <span class="text-[10px] text-lf-text-subtle">
          {{ t('workspace.job.stats.unitResources') }}
        </span>
      </span>
      <span v-if="showSkipped" class="flex items-center gap-1 text-lf-text-muted">
        <span class="inline-block h-2 w-2 rounded-full bg-lf-text-muted/60" />
        {{ t('workspace.job.stats.skipped') }}
        <span class="font-mono tabular-nums font-medium text-lf-text-strong">
          {{ skippedSegments }}
        </span>
        <span class="text-[10px] text-lf-text-subtle">
          {{ t('workspace.job.stats.unitSegments') }}
        </span>
      </span>
      <span class="flex items-center gap-1 text-lf-text-muted">
        {{ t('workspace.job.stats.total') }}
        <span class="font-mono tabular-nums font-medium text-lf-text-strong">
          {{ job.progress.progress_total }}
        </span>
        <span class="text-[10px] text-lf-text-subtle">
          {{ t('workspace.job.stats.unitWorkload') }}
        </span>
      </span>
    </div>

    <!-- ETA / 预计完成 / 速度行：网格卡片布局（仅运行中） -->
    <div
      v-if="job.status === 'running' && (etaText || speedText)"
      class="grid grid-cols-2 gap-2 sm:grid-cols-3"
    >
      <div
        v-if="etaText"
        class="flex items-center gap-1.5 rounded-lf-ctl bg-lf-surface/60 px-2.5 py-1.5"
      >
        <NIcon size="14" class="text-lf-text-muted">
          <IconCarbonTime />
        </NIcon>
        <div class="flex flex-col">
          <span class="text-[10px] text-lf-text-muted">{{ t('workspace.job.eta.label') }}</span>
          <span class="font-mono tabular-nums text-sm text-lf-text-strong">{{ etaText }}</span>
        </div>
      </div>
      <div
        v-if="etaCompletionText"
        class="flex items-center gap-1.5 rounded-lf-ctl bg-lf-surface/60 px-2.5 py-1.5"
      >
        <NIcon size="14" class="text-lf-text-muted">
          <IconCarbonCalendar />
        </NIcon>
        <div class="flex flex-col">
          <span class="text-[10px] text-lf-text-muted">
            {{ t('workspace.job.eta.expectedLabel') }}
          </span>
          <span class="font-mono tabular-nums text-sm text-lf-text-strong">
            {{ etaCompletionText }}
          </span>
        </div>
      </div>
      <div
        v-if="speedText"
        class="flex items-center gap-1.5 rounded-lf-ctl bg-lf-surface/60 px-2.5 py-1.5"
      >
        <div class="flex flex-col">
          <span class="text-[10px] text-lf-text-muted">{{ t('workspace.job.speed.label') }}</span>
          <span class="font-mono tabular-nums text-sm text-lf-text-strong">{{ speedText }}</span>
        </div>
      </div>
    </div>
  </div>
</template>
