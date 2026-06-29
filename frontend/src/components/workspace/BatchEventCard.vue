<script setup lang="ts">
import { computed, ref } from 'vue'
import { NButton, NIcon, NTag } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import type { BatchEventMetadata, SSEEvent } from '@/composables/useJobSSE'
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
const hasErrorInfo = computed(
  () =>
    meta.value &&
    (meta.value.error_type ||
      meta.value.error_message ||
      meta.value.tried_backends?.length ||
      meta.value.shrink_attempted),
)

const toggleExpand = (): void => {
  expanded.value = !expanded.value
}
</script>

<template>
  <div class="flex flex-wrap items-center gap-1.5">
    <NTag v-if="tokenLine" size="tiny" :bordered="false" type="default">
      {{ tokenLine }}
    </NTag>
    <NTag v-if="glossaryUsedCount" size="tiny" :bordered="false" type="info">
      {{ t('workspace.job.events.batch.glossaryUsed', { count: glossaryUsedCount }) }}
    </NTag>
    <NTag v-if="glossaryAddedCount" size="tiny" :bordered="false" type="success">
      {{ t('workspace.job.events.batch.glossaryAdded', { count: glossaryAddedCount }) }}
    </NTag>
  </div>

  <div v-if="hasErrorInfo" class="mt-2 space-y-0.5 text-xs text-lf-text-muted">
    <div v-if="meta!.error_type">
      <span class="text-lf-text-strong">{{ t('workspace.job.events.batch.errorType') }}:</span>
      {{ meta!.error_type }}
    </div>
    <div v-if="meta!.error_message">
      <span class="text-lf-text-strong">{{ t('workspace.job.events.batch.errorMessage') }}:</span>
      {{ meta!.error_message }}
    </div>
    <div v-if="meta!.tried_backends?.length">
      <span class="text-lf-text-strong">{{ t('workspace.job.events.batch.triedBackends') }}:</span>
      {{ meta!.tried_backends.join(', ') }}
    </div>
    <NTag v-if="meta!.shrink_attempted" size="tiny" type="warning" :bordered="false">
      {{ t('workspace.job.events.batch.shrinkAttempted') }}
    </NTag>
  </div>

  <NButton quaternary size="tiny" class="mt-1.5 -ml-1" @click="toggleExpand">
    <template #icon>
      <NIcon size="14">
        <IconCarbonChevronDown v-if="!expanded" />
        <IconCarbonChevronUp v-else />
      </NIcon>
    </template>
    {{
      expanded ? t('workspace.job.events.batch.collapse') : t('workspace.job.events.batch.expand')
    }}
  </NButton>

  <div v-if="expanded && meta" class="mt-2 space-y-3">
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
</template>
