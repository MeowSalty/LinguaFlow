<script setup lang="ts">
import { NDrawer, NDrawerContent, NTag } from 'naive-ui'
import { useI18n } from 'vue-i18n'

import type { SSEEvent } from '@/composables/sseShared'
import { useBatchEventMeta } from '@/composables/useBatchEventMeta'

import BatchContentViewer from './BatchContentViewer.vue'
import GlossaryDiffTable from './GlossaryDiffTable.vue'

const { t } = useI18n()

const props = defineProps<{
  show: boolean
  event: SSEEvent | null
}>()

const emit = defineEmits<{
  'update:show': [value: boolean]
}>()

const { meta, tokenLine, glossaryUsedCount, glossaryAddedCount, statusTagType, hasErrorInfo } =
  useBatchEventMeta(
    () =>
      props.event ?? { type: '', job_id: 0, level: 'info', message: '', created_at: '', seq: 0 },
  )
</script>

<template>
  <NDrawer :show="show" :width="560" placement="right" @update:show="emit('update:show', $event)">
    <NDrawerContent
      :title="t('workspace.job.events.batch.detailTitle')"
      closable
      :native-scrollbar="false"
    >
      <div v-if="meta" class="space-y-4">
        <!-- Tags summary -->
        <div class="flex flex-wrap items-center gap-1">
          <NTag size="tiny" round :bordered="false" :type="statusTagType">
            {{ t(`workspace.job.events.batch.status.${meta.status}`) }}
          </NTag>
          <NTag v-if="meta.backend_name" size="tiny" round :bordered="false" type="default">
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

        <!-- Error info -->
        <div
          v-if="hasErrorInfo"
          class="space-y-0.5 rounded-md bg-lf-danger-soft/50 p-2 text-xs text-lf-text-muted"
        >
          <div v-if="meta.error_type">
            <span class="text-lf-text-strong"
              >{{ t('workspace.job.events.batch.errorType') }}:</span
            >
            {{ meta.error_type }}
          </div>
          <div v-if="meta.error_message">
            <span class="text-lf-text-strong"
              >{{ t('workspace.job.events.batch.errorMessage') }}:</span
            >
            {{ meta.error_message }}
          </div>
          <div v-if="meta.http_status != null">
            <span class="text-lf-text-strong"
              >{{ t('workspace.job.events.batch.httpStatus') }}:</span
            >
            {{ meta.http_status }}
          </div>
          <div v-if="meta.tried_backends?.length && meta.tried_backends.length > 1">
            <span class="text-lf-text-strong"
              >{{ t('workspace.job.events.batch.triedBackends') }}:</span
            >
            {{ meta.tried_backends.join(', ') }}
          </div>
          <NTag v-if="meta.shrink_attempted" size="tiny" round type="warning" :bordered="false">
            {{ t('workspace.job.events.batch.shrinkAttempted') }}
          </NTag>
        </div>

        <!-- Content viewers -->
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
    </NDrawerContent>
  </NDrawer>
</template>
