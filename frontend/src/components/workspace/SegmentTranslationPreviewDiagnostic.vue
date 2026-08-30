<script setup lang="ts">
import { computed, ref } from 'vue'
import { NTag } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import type { ApiSchemas } from '@/api/client'
import { formatTokens } from '@/composables/useWorkspaceUtils'

import BatchContentViewer from './BatchContentViewer.vue'
import GlossaryDiffTable from './GlossaryDiffTable.vue'

type Diagnostic = ApiSchemas['TranslationBatchDiagnostic']

const props = defineProps<{
  batch: Diagnostic
  index: number
}>()

const { t } = useI18n()
const expanded = ref(false)

const statusType = computed<'default' | 'success' | 'warning' | 'error'>(() => {
  if (props.batch.status === 'success') return 'success'
  if (props.batch.status === 'partial') return 'warning'
  return 'error'
})

const schemaContent = computed(() =>
  props.batch.request?.json_schema ? JSON.stringify(props.batch.request.json_schema, null, 2) : '',
)

const tokenLine = computed(() => {
  const input = props.batch.input_tokens ?? 0
  const output = props.batch.output_tokens ?? 0
  if (!input && !output) return ''
  return t('workspace.segment.translationPreview.diagnostic.tokens', {
    input: formatTokens(input),
    output: formatTokens(output),
  })
})

const stageLabel = computed(() =>
  t(`workspace.segment.translationPreview.stage.${props.batch.stage}`, props.batch.stage),
)

const hasErrorInfo = computed(
  () =>
    props.batch.error_type ||
    props.batch.error_message ||
    props.batch.http_status != null ||
    (props.batch.tried_backends?.length ?? 0) > 0 ||
    props.batch.shrink_attempted,
)
</script>

<template>
  <div class="rounded-lg border border-lf-border-soft bg-lf-surface-muted/30 p-3">
    <div class="flex flex-wrap items-center gap-1.5">
      <span class="text-xs font-medium text-lf-text-strong">
        {{ t('workspace.segment.translationPreview.diagnostic.batch', { index: index + 1 }) }}
      </span>
      <NTag size="tiny" :type="statusType" :bordered="false">
        {{ t(`workspace.segment.translationPreview.status.${batch.status}`) }}
      </NTag>
      <NTag v-if="batch.round_index != null" size="tiny" :bordered="false">
        {{
          t('workspace.segment.translationPreview.diagnostic.round', { index: batch.round_index })
        }}
      </NTag>
      <NTag v-if="stageLabel" size="tiny" :bordered="false">{{ stageLabel }}</NTag>
      <NTag v-if="batch.attempt != null" size="tiny" :bordered="false">
        {{ t('workspace.segment.translationPreview.diagnostic.attempt', { count: batch.attempt }) }}
      </NTag>
      <NTag v-if="batch.backend_name" size="tiny" :bordered="false">{{ batch.backend_name }}</NTag>
      <NTag v-if="batch.duration_ms != null" size="tiny" :bordered="false">
        {{
          t('workspace.segment.translationPreview.diagnostic.duration', { ms: batch.duration_ms })
        }}
      </NTag>
      <NTag v-if="batch.segment_count != null" size="tiny" :bordered="false">
        {{
          t('workspace.segment.translationPreview.diagnostic.segmentCount', {
            count: batch.segment_count,
          })
        }}
      </NTag>
      <NTag v-if="tokenLine" size="tiny" :bordered="false">
        <span class="font-mono tabular-nums">{{ tokenLine }}</span>
      </NTag>
      <NTag
        v-if="batch.truncated"
        size="tiny"
        type="warning"
        :bordered="false"
        :title="t('workspace.segment.translationPreview.diagnostic.truncatedHint')"
      >
        {{ t('workspace.segment.translationPreview.diagnostic.truncated') }}
      </NTag>
      <NTag
        v-if="batch.repaired?.length"
        size="tiny"
        :bordered="false"
        :title="t('workspace.segment.translationPreview.diagnostic.repairedHint')"
      >
        {{
          t('workspace.segment.translationPreview.diagnostic.repaired', {
            ops: batch.repaired.join(', '),
          })
        }}
      </NTag>
    </div>

    <div v-if="hasErrorInfo" class="mt-2 space-y-1 text-xs text-lf-text-muted">
      <div v-if="batch.error_type">
        <span class="font-medium text-lf-text-strong">
          {{ t('workspace.segment.translationPreview.diagnostic.errorType') }}:
        </span>
        {{ batch.error_type }}
      </div>
      <div v-if="batch.error_message">
        <span class="font-medium text-lf-text-strong">
          {{ t('workspace.segment.translationPreview.diagnostic.errorMessage') }}:
        </span>
        {{ batch.error_message }}
      </div>
      <div v-if="batch.http_status != null">
        <span class="font-medium text-lf-text-strong">
          {{ t('workspace.segment.translationPreview.diagnostic.httpStatus') }}:
        </span>
        {{ batch.http_status }}
      </div>
      <div v-if="batch.tried_backends?.length">
        <span class="font-medium text-lf-text-strong">
          {{ t('workspace.segment.translationPreview.diagnostic.triedBackends') }}:
        </span>
        {{ batch.tried_backends.join(', ') }}
      </div>
      <NTag v-if="batch.shrink_attempted" size="tiny" type="warning" :bordered="false">
        {{ t('workspace.segment.translationPreview.diagnostic.shrinkAttempted') }}
      </NTag>
    </div>

    <button
      type="button"
      class="mt-2 text-xs text-lf-text-muted transition-colors hover:text-brand-500"
      @click="expanded = !expanded"
    >
      <IconCarbonChevronDown v-if="!expanded" class="mr-0.5 inline-block h-3.5 w-3.5" />
      <IconCarbonChevronUp v-else class="mr-0.5 inline-block h-3.5 w-3.5" />
      {{
        expanded
          ? t('workspace.segment.translationPreview.diagnostic.collapse')
          : t('workspace.segment.translationPreview.diagnostic.expand')
      }}
    </button>

    <div v-if="expanded" class="mt-3 space-y-3">
      <BatchContentViewer
        :content="batch.request?.system_prompt ?? ''"
        :label="t('workspace.segment.translationPreview.diagnostic.systemPrompt')"
        :truncated="batch.request?.system_prompt_truncated"
        :original-length="batch.request?.system_prompt_length"
      />
      <BatchContentViewer
        :content="batch.request?.user_message ?? ''"
        :label="t('workspace.segment.translationPreview.diagnostic.userMessage')"
        :truncated="batch.request?.user_message_truncated"
        :original-length="batch.request?.user_message_length"
      />
      <BatchContentViewer
        :content="schemaContent"
        :label="t('workspace.segment.translationPreview.diagnostic.jsonSchema')"
      />
      <BatchContentViewer
        :content="batch.response?.content ?? ''"
        :label="t('workspace.segment.translationPreview.diagnostic.rawResponse')"
        :truncated="batch.response?.content_truncated"
        :original-length="batch.response?.content_length"
      />
      <GlossaryDiffTable
        :used-glossary="batch.used_glossary ?? []"
        :added-glossary="batch.added_glossary ?? []"
      />
    </div>
  </div>
</template>
