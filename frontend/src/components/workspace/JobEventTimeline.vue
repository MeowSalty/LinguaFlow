<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { NButton, NEmpty, NTimeline, NTimelineItem } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import type { BatchEventMetadata, SSEEvent } from '@/composables/sseShared'
import {
  eventLevelType,
  formatDuration,
  getStageLabel,
  isBatchEvent,
} from '@/composables/useWorkspaceUtils'

import BatchEventCard from './BatchEventCard.vue'

const { t } = useI18n()

const props = defineProps<{
  events: SSEEvent[]
  syntheticEvents?: SSEEvent[]
  connected?: boolean
}>()

const emit = defineEmits<{
  clear: []
}>()

const scrollContainer = ref<HTMLElement | null>(null)
const isNearBottom = ref(true)
const hasNewEvents = ref(false)
const prevEventsLength = ref(0)

const formatEventTime = (value: string): string => {
  return new Intl.DateTimeFormat('zh-Hans', {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  }).format(new Date(value))
}

const makeKey = (event: SSEEvent, index: number, prefix: string): string => {
  return `${prefix}-${event.type}-${event.created_at}-${index}`
}

const getBatchSummary = (event: SSEEvent): string => {
  const meta = event.metadata as unknown as BatchEventMetadata | undefined
  if (!meta) return event.message
  const parts: string[] = []
  if (event.stage) parts.push(getStageLabel(event.stage))
  parts.push(
    t('workspace.job.events.batch.summary', { index: meta.batch_index }),
    t('workspace.job.events.batch.segments', { count: meta.segment_count }),
  )
  if (meta.duration_ms) parts.push(formatDuration(meta.duration_ms))
  return parts.join(' · ')
}

const getBatchTimelineType = (event: SSEEvent): 'success' | 'warning' | 'error' => {
  if (event.type === 'batch_error') return 'error'
  const meta = event.metadata as BatchEventMetadata | undefined
  if (meta?.status === 'partial') return 'warning'
  return 'success'
}

const JOB_EVENT_TYPES = new Set(['job_started', 'job_completed', 'job_failed', 'job_cancelled'])

const RESOURCE_EVENT_TYPES = new Set([
  'resource_started',
  'resource_completed',
  'resource_failed',
  'resource_cancelled',
])

const filteredSyntheticEvents = computed(() => {
  const synthetic = props.syntheticEvents ?? []
  const live = props.events

  const liveJobTypes = new Set<string>()
  const liveResourceTypes = new Set<string>()

  for (const event of live) {
    if (JOB_EVENT_TYPES.has(event.type)) {
      liveJobTypes.add(event.type)
    } else if (RESOURCE_EVENT_TYPES.has(event.type)) {
      liveResourceTypes.add(event.type)
    }
  }

  return synthetic.filter((event) => {
    if (JOB_EVENT_TYPES.has(event.type)) {
      return !liveJobTypes.has(event.type)
    }
    if (RESOURCE_EVENT_TYPES.has(event.type)) {
      return !liveResourceTypes.has(event.type)
    }
    return true
  })
})

const allEvents = computed(() => {
  return [...filteredSyntheticEvents.value, ...props.events]
})

let scrollTicking = false
const checkScrollPosition = (): void => {
  if (scrollTicking) return
  scrollTicking = true
  requestAnimationFrame(() => {
    const el = scrollContainer.value
    if (!el) {
      scrollTicking = false
      return
    }
    isNearBottom.value = el.scrollTop + el.clientHeight >= el.scrollHeight - 50
    if (isNearBottom.value) {
      hasNewEvents.value = false
    }
    scrollTicking = false
  })
}

const scrollToBottom = (): void => {
  const el = scrollContainer.value
  if (!el) return
  el.scrollTo({ top: el.scrollHeight, behavior: 'smooth' })
  hasNewEvents.value = false
}

watch(
  () => props.events.length,
  (newLen) => {
    if (newLen > prevEventsLength.value) {
      if (isNearBottom.value) {
        nextTick(() => {
          scrollToBottom()
        })
      } else {
        hasNewEvents.value = true
      }
    }
    prevEventsLength.value = newLen
  },
)

onMounted(() => {
  prevEventsLength.value = props.events.length
  nextTick(() => scrollToBottom())
})
</script>

<template>
  <div class="space-y-3">
    <div class="flex items-center justify-between">
      <h4 class="text-sm font-medium text-lf-text-strong">
        {{ t('workspace.job.events.title') }}
      </h4>
      <div class="flex items-center gap-2">
        <span
          v-if="connected"
          class="inline-block h-1.5 w-1.5 rounded-full bg-green-500"
          :title="t('workspace.job.events.connected')"
        />
        <span
          v-else
          class="inline-block h-1.5 w-1.5 rounded-full bg-gray-400"
          :title="t('workspace.job.events.disconnected')"
        />
        <NButton quaternary size="tiny" @click="emit('clear')">
          {{ t('workspace.actions.clear') }}
        </NButton>
      </div>
    </div>

    <div class="relative min-h-50">
      <div ref="scrollContainer" class="max-h-100 overflow-auto" @scroll="checkScrollPosition">
        <div v-if="allEvents.length === 0" class="py-6 text-center">
          <NEmpty size="small" :description="t('workspace.job.events.empty')" />
        </div>

        <NTimeline v-else :icon-size="16">
          <!-- Synthetic events: dashed line + muted text -->
          <template v-if="filteredSyntheticEvents.length > 0">
            <NTimelineItem
              v-for="(event, index) in filteredSyntheticEvents"
              :key="makeKey(event, index, 'syn')"
              line-type="dashed"
              type="default"
              :title="event.message"
              :content="event.stage ? getStageLabel(event.stage) : undefined"
              :time="formatEventTime(event.created_at)"
              class="[&_.n-timeline-item-content]:text-lf-text-muted"
            />
          </template>

          <!-- Real-time events: solid line + normal text -->
          <template v-for="(event, index) in events" :key="makeKey(event, index, 'live')">
            <NTimelineItem
              v-if="!isBatchEvent(event.type)"
              line-type="default"
              :type="eventLevelType(event.level)"
              :title="event.message"
              :content="event.stage ? getStageLabel(event.stage) : undefined"
              :time="formatEventTime(event.created_at)"
            />
            <NTimelineItem
              v-else
              line-type="default"
              :type="getBatchTimelineType(event)"
              :title="getBatchSummary(event)"
              :time="formatEventTime(event.created_at)"
            >
              <BatchEventCard :event="event" />
            </NTimelineItem>
          </template>
        </NTimeline>
      </div>

      <!-- Floating "new events" button -->
      <Transition
        enter-active-class="transition-opacity duration-200"
        leave-active-class="transition-opacity duration-200"
        enter-from-class="opacity-0"
        leave-to-class="opacity-0"
      >
        <button
          v-if="hasNewEvents && !isNearBottom"
          class="absolute bottom-4 left-1/2 -translate-x-1/2 rounded-full bg-brand-500 px-4 py-1.5 text-xs font-medium text-white shadow-lg hover:bg-brand-600"
          @click="scrollToBottom"
        >
          {{ t('workspace.job.events.newEvents', { count: events.length - prevEventsLength + 1 }) }}
        </button>
      </Transition>
    </div>
  </div>
</template>
