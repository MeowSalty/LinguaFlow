<script setup lang="ts">
import { NButton, NEmpty, NSpin, NTimeline, NTimelineItem } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import type { ApiSchemas } from '@/api/client'
import { eventLevelType, getStageLabel } from '@/composables/useWorkspaceUtils'

const { t } = useI18n()

defineProps<{
  events: ApiSchemas['JobEvent'][]
  loading?: boolean
}>()

const emit = defineEmits<{
  refresh: []
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
      <NButton quaternary size="tiny" :loading="loading" @click="emit('refresh')">
        {{ t('workspace.actions.refresh') }}
      </NButton>
    </div>

    <NSpin :show="loading" size="small">
      <div v-if="events.length === 0" class="py-6 text-center">
        <NEmpty size="small" :description="t('workspace.job.events.empty')" />
      </div>

      <NTimeline v-else :icon-size="16">
        <NTimelineItem
          v-for="event in events"
          :key="event.id"
          :type="eventLevelType(event.level)"
          :title="event.message"
          :content="event.stage ? getStageLabel(event.stage) : undefined"
          :time="formatEventTime(event.created_at)"
          :line-type="event.id === events[events.length - 1]?.id ? 'dashed' : 'default'"
        />
      </NTimeline>
    </NSpin>
  </div>
</template>
