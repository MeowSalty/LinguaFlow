<script setup lang="ts">
import { computed } from 'vue'
import { NTag } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import type { PoolEventMetadata, SSEEvent } from '@/composables/sseShared'

const { t } = useI18n()

const props = defineProps<{
  event: SSEEvent
}>()

const meta = computed<PoolEventMetadata | null>(() => {
  if (!props.event.metadata) return null
  return props.event.metadata as unknown as PoolEventMetadata
})

const phaseTagType = computed<'info' | 'warning'>(() =>
  meta.value?.phase === 'pool_advance' ? 'warning' : 'info',
)
</script>

<template>
  <div v-if="meta" class="flex flex-wrap items-center gap-1">
    <NTag size="tiny" round :bordered="false" :type="phaseTagType">
      {{
        meta.phase === 'pool_advance'
          ? t('workspace.job.events.pool.poolAdvance')
          : t('workspace.job.events.pool.poolStart')
      }}
    </NTag>
    <NTag size="tiny" round :bordered="false" type="default">
      <span class="font-mono tabular-nums">
        {{
          t('workspace.job.events.pool.progress', {
            index: meta.pool_index + 1,
            total: meta.max_pools,
          })
        }}
      </span>
    </NTag>
    <NTag size="tiny" round :bordered="false" type="default">
      {{ t('workspace.job.events.pool.batches', { count: meta.batches }) }}
    </NTag>
    <NTag size="tiny" round :bordered="false" type="default">
      <span class="font-mono tabular-nums">
        {{ t('workspace.job.events.pool.pending', { count: meta.pending }) }}
      </span>
    </NTag>
    <NTag size="tiny" round :bordered="false" type="default">
      <span class="font-mono tabular-nums">
        {{ t('workspace.job.events.pool.shrinkRate', { rate: meta.shrink_rate.toFixed(2) }) }}
      </span>
    </NTag>
  </div>
</template>
