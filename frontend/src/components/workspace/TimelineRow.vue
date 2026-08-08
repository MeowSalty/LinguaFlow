<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

import type { BatchEventMetadata, SSEEvent } from '@/composables/sseShared'
import {
  batchStatusTimelineType,
  eventLevelType,
  formatDuration,
  getPoolSummary,
  getStageLabel,
  isBatchEvent,
  isPoolEvent,
  poolTimelineType,
} from '@/composables/useWorkspaceUtils'

import BatchEventCard from './BatchEventCard.vue'
import PoolEventCard from './PoolEventCard.vue'

const { t } = useI18n()

const props = defineProps<{
  event: SSEEvent & { _key?: string }
  isLast?: boolean
}>()

const emit = defineEmits<{
  'open-detail': [event: SSEEvent]
}>()

const formatEventTime = (value: string): string => {
  return new Intl.DateTimeFormat('zh-Hans', {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  }).format(new Date(value))
}

const getBatchSummary = (event: SSEEvent): string => {
  const meta = event.metadata as unknown as BatchEventMetadata | undefined
  if (!meta) return event.message
  const parts: string[] = []
  if (event.stage) parts.push(getStageLabel(event.stage))
  parts.push(t('workspace.job.events.batch.segments', { count: meta.segment_count }))
  if (meta.duration_ms) parts.push(formatDuration(meta.duration_ms))
  return parts.join(' · ')
}

const getBatchTimelineType = (event: SSEEvent): 'success' | 'warning' | 'error' | 'info' => {
  const meta = event.metadata as unknown as BatchEventMetadata | undefined
  return batchStatusTimelineType(meta?.status, event.level)
}

const getPoolTimelineType = (event: SSEEvent): 'info' | 'warning' | 'error' => {
  const meta = event.metadata as unknown as { phase?: 'pool_start' | 'pool_advance' } | undefined
  return poolTimelineType(meta?.phase, event.level)
}

type RowType = 'success' | 'warning' | 'error' | 'info' | 'default'

const typeColorMap: Record<RowType, string> = {
  success: 'bg-green-500',
  warning: 'bg-amber-500',
  error: 'bg-red-500',
  info: 'bg-brand-500',
  default: 'bg-lf-text-muted',
}

const rowType = computed<RowType>(() => {
  if (isPoolEvent(props.event.type)) return getPoolTimelineType(props.event)
  if (isBatchEvent(props.event.type)) return getBatchTimelineType(props.event)
  return eventLevelType(props.event.level)
})
</script>

<template>
  <div class="flex gap-3 py-1">
    <div class="relative flex w-[14px] shrink-0 flex-col items-center">
      <div
        :class="[isLast ? 'opacity-0' : '', 'absolute left-[5px] top-2 bottom-0 w-px border-l border-lf-border']"
      />
      <div
        class="mt-1 h-3 w-3 shrink-0 rounded-full"
        :class="typeColorMap[rowType]"
      />
    </div>
    <div class="min-w-0 flex-1">
      <span class="font-mono tabular-nums text-xs text-lf-text-muted">{{ formatEventTime(event.created_at) }}</span>
      <div class="text-sm">
        {{ event.message }}
      </div>
      <div
        v-if="event.stage && !isBatchEvent(event.type) && !isPoolEvent(event.type)"
        class="text-xs text-lf-text-muted"
      >
        {{ getStageLabel(event.stage) }}
      </div>
      <template v-if="isPoolEvent(event.type)">
        <div class="text-xs text-lf-text-strong">{{ getPoolSummary(event) }}</div>
        <PoolEventCard :event="event" />
      </template>
      <template v-else-if="isBatchEvent(event.type)">
        <div class="text-xs text-lf-text-strong">{{ getBatchSummary(event) }}</div>
        <BatchEventCard :event="event" @open-detail="emit('open-detail', $event)" />
      </template>
    </div>
  </div>
</template>