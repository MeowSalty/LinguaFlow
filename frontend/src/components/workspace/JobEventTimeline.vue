<script setup lang="ts">
import { computed, nextTick, onMounted, ref, watch } from 'vue'
import { NButton, NEmpty, NTimeline, NTimelineItem } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import type { SSEEvent } from '@/composables/useJobSSE'
import { eventLevelType, getStageLabel, isBatchEvent } from '@/composables/useWorkspaceUtils'

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

const allEvents = computed(() => {
  const synthetic = props.syntheticEvents ?? []
  return [...synthetic, ...props.events]
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

    <div class="relative min-h-[200px]">
      <div ref="scrollContainer" class="max-h-[400px] overflow-auto" @scroll="checkScrollPosition">
        <div v-if="allEvents.length === 0" class="py-6 text-center">
          <NEmpty size="small" :description="t('workspace.job.events.empty')" />
        </div>

        <NTimeline v-else :icon-size="16">
          <!-- Synthetic events: dashed line + muted text -->
          <template v-if="syntheticEvents && syntheticEvents.length > 0">
            <NTimelineItem
              v-for="(event, index) in syntheticEvents"
              :key="makeKey(event, index, 'syn')"
              line-type="dashed"
              type="default"
              :title="event.message"
              :content="event.stage ? getStageLabel(event.stage) : undefined"
              :time="formatEventTime(event.created_at)"
              class="[&_.n-timeline-item-content]:text-lf-text-muted [&_.n-timeline-item-content]:text-lf-text-subtle"
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
            <div v-else class="mb-3">
              <div class="mb-1 text-[10px] text-lf-text-muted">
                {{ formatEventTime(event.created_at) }}
              </div>
              <BatchEventCard :event="event" />
            </div>
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
          class="absolute bottom-4 left-1/2 -translate-x-1/2 rounded-full bg-lf-brand-500 px-4 py-1.5 text-xs font-medium text-white shadow-lg hover:bg-lf-brand-600"
          @click="scrollToBottom"
        >
          {{ t('workspace.job.events.newEvents', { count: events.length - prevEventsLength + 1 }) }}
        </button>
      </Transition>
    </div>
  </div>
</template>
