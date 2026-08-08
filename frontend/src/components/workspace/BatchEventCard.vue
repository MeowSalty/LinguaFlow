<script setup lang="ts">
import { NTag } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import type { SSEEvent } from '@/composables/sseShared'
import { useBatchEventMeta } from '@/composables/useBatchEventMeta'

const { t } = useI18n()

const props = defineProps<{
  event: SSEEvent
}>()

const emit = defineEmits<{
  'open-detail': [event: SSEEvent]
}>()

const { meta, tokenLine, glossaryUsedCount, glossaryAddedCount, statusTagType, hasErrorInfo } =
  useBatchEventMeta(() => props.event)
</script>

<template>
  <button
    class="mt-1 flex flex-wrap items-center gap-1 rounded transition-colors hover:text-brand-500"
    @click="emit('open-detail', event)"
  >
    <NTag v-if="meta?.status" size="tiny" round :bordered="false" :type="statusTagType">
      {{ t(`workspace.job.events.batch.status.${meta.status}`) }}
    </NTag>
    <NTag v-if="meta?.backend_name" size="tiny" round :bordered="false" type="default">
      {{ meta.backend_name }}
    </NTag>
    <NTag v-if="tokenLine" size="tiny" round :bordered="false" type="default">
      <span class="font-mono tabular-nums">{{ tokenLine }}</span>
    </NTag>
    <NTag v-if="glossaryUsedCount" size="tiny" round :bordered="false" type="info">
      {{ t('workspace.job.events.batch.glossaryUsed', { count: glossaryUsedCount }) }}
    </NTag>
    <NTag v-if="glossaryAddedCount" size="tiny" round :bordered="false" type="success">
      {{ t('workspace.job.events.batch.glossaryAdded', { count: glossaryAddedCount }) }}
    </NTag>
    <NTag v-if="hasErrorInfo" size="tiny" round :bordered="false" type="error">
      {{ t('workspace.job.events.batch.errorBadge') }}
    </NTag>
  </button>
</template>
