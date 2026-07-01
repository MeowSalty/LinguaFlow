<script setup lang="ts">
import { computed, ref } from 'vue'
import { NTag } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import type { BatchEventMetadata, SSEEvent } from '@/composables/sseShared'
import { formatTokens } from '@/composables/useWorkspaceUtils'

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

const tokenLine = computed(() => {
  if (!meta.value) return ''
  if (!meta.value.input_tokens && !meta.value.output_tokens) return ''
  return t('workspace.job.events.batch.tokens', {
    input: formatTokens(meta.value.input_tokens),
    output: formatTokens(meta.value.output_tokens),
  })
})

const glossaryUsedCount = computed(() => meta.value?.used_glossary?.length ?? 0)
const glossaryAddedCount = computed(() => meta.value?.added_glossary?.length ?? 0)
const statusTagType = computed(() => {
  const status = meta.value?.status
  if (status === 'failed') return 'error'
  if (status === 'partial') return 'warning'
  if (status === 'success') return 'success'
  return 'default'
})

const hasErrorInfo = computed(
  () =>
    meta.value &&
    (meta.value.error_type ||
      meta.value.error_message ||
      meta.value.http_status != null ||
      (meta.value.tried_backends?.length != null && meta.value.tried_backends.length > 1) ||
      meta.value.shrink_attempted),
)

const toggleExpand = (): void => {
  expanded.value = !expanded.value
}
</script>

<template>
  <div class="flex flex-wrap items-center gap-1">
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
  </div>

  <div
    v-if="hasErrorInfo"
    class="mt-2 space-y-0.5 rounded-md bg-lf-danger-soft/50 p-2 text-xs text-lf-text-muted"
  >
    <div v-if="meta!.error_type">
      <span class="text-lf-text-strong">{{ t('workspace.job.events.batch.errorType') }}:</span>
      {{ meta!.error_type }}
    </div>
    <div v-if="meta!.error_message">
      <span class="text-lf-text-strong">{{ t('workspace.job.events.batch.errorMessage') }}:</span>
      {{ meta!.error_message }}
    </div>
    <div v-if="meta!.http_status != null">
      <span class="text-lf-text-strong">{{ t('workspace.job.events.batch.httpStatus') }}:</span>
      {{ meta!.http_status }}
    </div>
    <div v-if="meta!.tried_backends?.length && meta!.tried_backends.length > 1">
      <span class="text-lf-text-strong">{{ t('workspace.job.events.batch.triedBackends') }}:</span>
      {{ meta!.tried_backends.join(', ') }}
    </div>
    <NTag v-if="meta!.shrink_attempted" size="tiny" round type="warning" :bordered="false">
      {{ t('workspace.job.events.batch.shrinkAttempted') }}
    </NTag>
  </div>

  <button
    class="mt-1.5 inline-flex items-center gap-0.5 text-xs text-lf-text-muted hover:text-brand-500 transition-colors"
    @click="toggleExpand"
  >
    <IconCarbonChevronDown v-if="!expanded" class="inline-block h-3.5 w-3.5" />
    <IconCarbonChevronUp v-else class="inline-block h-3.5 w-3.5" />
    {{
      expanded ? t('workspace.job.events.batch.collapse') : t('workspace.job.events.batch.expand')
    }}
  </button>

  <div v-if="expanded && meta" class="mt-2 space-y-3">
    <BatchContentViewer
      :content="meta.sent_content"
      :label="t('workspace.job.events.batch.sentContent')"
      :truncated="meta.sent_truncated"
      :original-length="meta.sent_length"
    />
    <BatchContentViewer
      :content="meta.received_content"
      :label="t('workspace.job.events.batch.receivedContent')"
      :truncated="meta.received_truncated"
      :original-length="meta.received_length"
    />
    <GlossaryDiffTable
      :used-glossary="meta.used_glossary ?? []"
      :added-glossary="meta.added_glossary ?? []"
    />
  </div>
</template>
