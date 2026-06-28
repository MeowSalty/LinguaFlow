<script setup lang="ts">
import { NButton, NEmpty, NTimeline, NTimelineItem } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import type { SSEEvent } from '@/composables/useJobSSE'
import { eventLevelType, getStageLabel } from '@/composables/useWorkspaceUtils'

const { t } = useI18n()

defineProps<{
  events: SSEEvent[]
  connected?: boolean
}>()

const emit = defineEmits<{
  clear: []
}>()

const formatEventTime = (value: string): string => {
  return new Intl.DateTimeFormat('zh-Hans', {
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  }).format(new Date(value))
}
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

    <div v-if="events.length === 0" class="py-6 text-center">
      <NEmpty size="small" :description="t('workspace.job.events.empty')" />
    </div>

    <NTimeline v-else :icon-size="16">
      <NTimelineItem
        v-for="(event, index) in events"
        :key="`${event.created_at}-${index}`"
        :type="eventLevelType(event.level)"
        :title="event.message"
        :content="event.stage ? getStageLabel(event.stage) : undefined"
        :time="formatEventTime(event.created_at)"
        :line-type="index === events.length - 1 ? 'dashed' : 'default'"
      />
    </NTimeline>
  </div>
</template>
