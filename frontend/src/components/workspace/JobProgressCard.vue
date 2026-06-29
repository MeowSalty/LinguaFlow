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
  <div class="rounded-xl border border-lf-border-soft bg-lf-surface-muted/60 p-5 space-y-4">
    <!-- 顶部信息行 -->
    <div class="flex flex-wrap items-center gap-3">
      <!-- 状态标签 -->
      <NTag size="small" :type="statusTagType(job.status)">
        {{ getJobStatusLabel(job.status) }}
      </NTag>

      <!-- 阶段信息（running 时显示） -->
      <NTag
        v-if="job.status === 'running' && job.current_stage"
        size="small"
        :bordered="false"
        type="info"
      >
        <template #icon>
          <NIcon size="14">
            <IconCarbonAsync />
          </NIcon>
        </template>
        {{ getStageLabel(job.current_stage) }}
        {{ job.completed_segments }}/{{ job.total_segments }}
      </NTag>

      <!-- 队列位置（pending 时显示） -->
      <NTag
        v-if="job.status === 'pending' && job.queue_position != null"
        size="small"
        :bordered="false"
        type="warning"
      >
        <template #icon>
          <NIcon size="14">
            <IconCarbonCircleDash />
          </NIcon>
        </template>
        {{ getJobProgressText(job) }}
      </NTag>
    </div>

    <!-- 主进度条 -->
    <div class="space-y-1.5">
      <div class="flex items-center justify-between text-xs">
        <span class="text-lf-text-muted">{{ getJobProgressText(job) }}</span>
        <span class="font-medium text-lf-text-strong">{{ progressPercent }}%</span>
      </div>
      <NProgress
        type="line"
        :percentage="progressPercent"
        :show-indicator="false"
        :height="8"
        :border-radius="4"
        :processing="job.status === 'running'"
        :status="progressStatus"
      />
    </div>

    <!-- ETA 与速度行 -->
    <div
      v-if="job.status === 'running' && (etaText || speedText)"
      class="flex flex-wrap items-center gap-3 text-xs text-lf-text-muted"
    >
      <div v-if="etaText" class="flex items-center gap-1.5">
        <NIcon size="14">
          <IconCarbonTime />
        </NIcon>
        <span>{{ etaText }}</span>
      </div>
      <div v-if="speedText" class="flex items-center gap-1.5">
        <!-- <NIcon size="14">
          <IconCarbonSpeedometer />
        </NIcon> -->
        <span>{{ speedText }}</span>
      </div>
    </div>
  </div>
</template>
