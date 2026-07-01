<script setup lang="ts">
import { computed } from 'vue'
import { NIcon, NProgress, NTag } from 'naive-ui'

import type { ApiSchemas } from '@/api/client'
import {
  calculateJobETA,
  calculateJobSpeed,
  formatETA,
  formatJobSpeed,
  getJobProgressText,
  getJobStatusLabel,
  getStageLabel,
  statusTagType,
} from '@/composables/useWorkspaceUtils'

const props = defineProps<{
  job: ApiSchemas['TranslationJob']
}>()

const progressPercent = computed(() => {
  if (props.job.status === 'completed') return 100
  if (props.job.status === 'failed' || props.job.status === 'cancelled') return 0
  if (props.job.total_segments > 0) {
    return Math.round((props.job.completed_segments / props.job.total_segments) * 100)
  }
  return 0
})

const progressStatus = computed(() => {
  if (props.job.status === 'completed') return 'success' as const
  if (props.job.status === 'failed') return 'error' as const
  return 'default' as const
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
    class="rounded-xl border border-lf-border-soft bg-gradient-to-br from-lf-surface to-lf-surface-muted p-4 space-y-3"
    :class="{
      'border-l-3 border-brand-500': job.status === 'running',
      'border-l-3 border-green-500': job.status === 'completed',
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

        <!-- 阶段信息（running 时显示） -->
        <NTag
          v-if="job.status === 'running' && job.current_stage"
          size="tiny"
          round
          :bordered="false"
          type="info"
        >
          <template #icon>
            <NIcon size="12">
              <IconCarbonAsync />
            </NIcon>
          </template>
          {{ getStageLabel(job.current_stage) }}
          <span class="font-mono tabular-nums"
            >{{ job.completed_segments }}/{{ job.total_segments }}</span
          >
        </NTag>

        <!-- 队列位置（pending 时显示） -->
        <NTag
          v-if="job.status === 'pending' && job.queue_position != null"
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
      <span class="text-2xl font-mono font-bold text-brand-500"> {{ progressPercent }}% </span>
    </div>

    <!-- 主进度条 -->
    <div class="space-y-1">
      <div class="text-xs text-lf-text-muted">{{ getJobProgressText(job) }}</div>
      <NProgress
        type="line"
        :percentage="progressPercent"
        :show-indicator="false"
        :height="6"
        :border-radius="3"
        :processing="job.status === 'running'"
        :status="progressStatus"
      />
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
