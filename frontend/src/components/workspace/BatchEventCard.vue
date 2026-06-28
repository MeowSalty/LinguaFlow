<script setup lang="ts">
import { computed, ref } from 'vue'
import { NButton, NIcon, NTag } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import type { BatchEventMetadata, SSEEvent } from '@/composables/useJobSSE'
import { formatDuration, formatTokens } from '@/composables/useWorkspaceUtils'

import BatchContentViewer from './BatchContentViewer.vue'
import GlossaryDiffTable from './GlossaryDiffTable.vue'

const { t } = useI18n()

const props = defineProps<{
  event: SSEEvent
}>()

const expanded = ref(false)

const meta = computed<BatchEventMetadata | null>(() => {
  if (!props.event.metadata) return null
  return props.event.metadata as unknown as BatchEventMetadata
})

const isError = computed(() => props.event.type === 'batch_error')

const summaryLine = computed(() => {
  if (!meta.value) return ''
  const parts: string[] = [
    t('workspace.job.events.batch.summary', { index: meta.value.batch_index }),
    t('workspace.job.events.batch.segments', { count: meta.value.segment_count }),
  ]
  if (meta.value.backend_name) parts.push(meta.value.backend_name)
  if (meta.value.duration_ms) parts.push(formatDuration(meta.value.duration_ms))
  return parts.join(' · ')
})

const tokenLine = computed(() => {
  if (!meta.value) return ''
  if (!meta.value.input_tokens && !meta.value.output_tokens) return ''
  return t('workspace.job.events.batch.tokens', {
    input: formatTokens(meta.value.input_tokens),
    output: formatTokens(meta.value.output_tokens),
  })
})

const glossaryLine = computed(() => {
  if (!meta.value) return ''
  const used = meta.value.used_glossary?.length ?? 0
  const added = meta.value.added_glossary?.length ?? 0
  if (!used && !added) return ''
  const parts: string[] = []
  if (used) parts.push(t('workspace.job.events.batch.glossaryUsed', { count: used }))
  if (added) parts.push(t('workspace.job.events.batch.glossaryAdded', { count: added }))
  return parts.join(' · ')
})

const toggleExpand = (): void => {
  expanded.value = !expanded.value
}
</script>

<template>
  <div
    class="rounded-lg border p-3"
    :class="isError
      ? 'border-l-2 border-l-red-500 bg-lf-danger-soft/40'
      : 'border-l-2 border-lf-brand-500 bg-lf-brand-soft/40'"
  >
    <!-- Summary row -->
    <div class="flex items-center gap-2 text-sm">
      <NIcon size="16" :class="isError ? 'text-red-500' : 'text-lf-brand-500'">
        <IconCarbonWarningAlt v-if="isError" />
        <IconCarbonCheckmarkFilled v-else />
      </NIcon>
      <span class="font-medium text-lf-text-strong">{{ summaryLine }}</span>
    </div>

    <!-- Metrics row -->
    <div
      v-if="tokenLine || glossaryLine"
      class="mt-1.5 flex flex-wrap items-center gap-2 text-xs text-lf-text-muted"
    >
      <span v-if="tokenLine">{{ tokenLine }}</span>
      <span v-if="glossaryLine">{{ glossaryLine }}</span>
    </div>

    <!-- Error info -->
    <div
      v-if="isError && meta"
      class="mt-1.5 space-y-1 text-xs text-lf-text-muted"
    >
      <div v-if="meta.error_type">
        <span class="text-lf-text-strong">{{ t('workspace.job.events.batch.errorType') }}:</span>
        {{ meta.error_type }}
      </div>
      <div v-if="meta.error_message">
        <span class="text-lf-text-strong">{{ t('workspace.job.events.batch.errorMessage') }}:</span>
        {{ meta.error_message }}
      </div>
      <div v-if="meta.tried_backends?.length">
        <span class="text-lf-text-strong">{{ t('workspace.job.events.batch.triedBackends') }}:</span>
        {{ meta.tried_backends.join(', ') }}
      </div>
      <div v-if="meta.shrink_attempted">
        <NTag size="tiny" type="warning" :bordered="false">
          {{ t('workspace.job.events.batch.shrinkAttempted') }}
        </NTag>
      </div>
    </div>

    <!-- Expand toggle -->
    <NButton
      quaternary
      size="tiny"
      class="mt-2"
      @click="toggleExpand"
    >
      {{ expanded ? t('workspace.job.events.batch.collapse') : t('workspace.job.events.batch.expand') }}
    </NButton>

    <!-- Expanded details -->
    <div v-if="expanded && meta" class="mt-3 space-y-3">
      <BatchContentViewer
        :content="meta.sent_content"
        :label="t('workspace.job.events.batch.sentContent')"
      />
      <BatchContentViewer
        :content="meta.received_content"
        :label="t('workspace.job.events.batch.receivedContent')"
      />
      <GlossaryDiffTable
        :used-glossary="meta.used_glossary ?? []"
        :added-glossary="meta.added_glossary ?? []"
      />
    </div>
  </div>
</template>
