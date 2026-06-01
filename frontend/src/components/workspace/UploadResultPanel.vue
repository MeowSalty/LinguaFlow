<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { NButton, NTag } from 'naive-ui'

import type { ApiSchemas } from '@/api/client'
import type {
  IncrementalUploadResult,
  PendingUploadItem,
  ReplaceUploadResult,
  UploadExecutionResult,
} from '@/stores/projectWorkspace'

type ResultRow =
  | (ApiSchemas['ResourceUploadFileResult'] & { rowType: 'result' })
  | {
      rowType: 'incremental'
      path: string
      action: 'incremental_updated' | 'failed'
      incremental: IncrementalUploadResult
      error?: string
    }
  | {
      rowType: 'replace'
      path: string
      action: 'replaced' | 'failed'
      replace: ReplaceUploadResult
      error?: string
    }
  | {
      rowType: 'skipped'
      path: string
      action: 'skipped'
      item: PendingUploadItem
    }

type SummaryTone = 'emerald' | 'blue' | 'purple' | 'amber' | 'red' | 'slate'

interface SummaryItem {
  key: keyof UploadExecutionResult['summary']
  label: string
  value: number
  tone: SummaryTone
}

const props = defineProps<{
  result: UploadExecutionResult
}>()

const emit = defineEmits<{
  close: []
}>()

const { t } = useI18n()

const rows = computed<ResultRow[]>(() => [
  ...props.result.response.items.map((item) => ({ ...item, rowType: 'result' as const })),
  ...props.result.incrementalResults.map((item) => ({
    rowType: 'incremental' as const,
    path: item.item.path,
    action: item.error ? ('failed' as const) : ('incremental_updated' as const),
    incremental: item,
    error: item.error,
  })),
  ...props.result.replaceResults.map((item) => ({
    rowType: 'replace' as const,
    path: item.item.path,
    action: item.error ? ('failed' as const) : ('replaced' as const),
    replace: item,
    error: item.error,
  })),
  ...props.result.skippedItems.map((item) => ({
    rowType: 'skipped' as const,
    path: item.path,
    action: 'skipped' as const,
    item,
  })),
])

const summaryItems = computed<SummaryItem[]>(() => [
  {
    key: 'created',
    label: t('workspace.uploadResult.summary.created'),
    value: props.result.summary.created,
    tone: 'emerald',
  },
  {
    key: 'incrementallyUpdated',
    label: t('workspace.uploadResult.summary.incrementallyUpdated'),
    value: props.result.summary.incrementallyUpdated,
    tone: 'blue',
  },
  {
    key: 'replaced',
    label: t('workspace.uploadResult.summary.replaced'),
    value: props.result.summary.replaced,
    tone: 'purple',
  },
  {
    key: 'conflicts',
    label: t('workspace.uploadResult.summary.conflicts'),
    value: props.result.summary.conflicts,
    tone: 'amber',
  },
  {
    key: 'failed',
    label: t('workspace.uploadResult.summary.failed'),
    value: props.result.summary.failed,
    tone: 'red',
  },
  {
    key: 'skipped',
    label: t('workspace.uploadResult.summary.skipped'),
    value: props.result.summary.skipped,
    tone: 'slate',
  },
])

const tagType = (action: ResultRow['action']): 'success' | 'warning' | 'error' | 'default' => {
  if (action === 'created' || action === 'incremental_updated' || action === 'replaced') {
    return 'success'
  }
  if (action === 'conflict' || action === 'skipped') {
    return 'warning'
  }
  if (action === 'failed') {
    return 'error'
  }
  return 'default'
}

const rowToneClass = (action: ResultRow['action']): string => {
  if (action === 'created' || action === 'incremental_updated' || action === 'replaced') {
    return 'border-emerald-200 bg-emerald-50/40 dark:border-emerald-500/20 dark:bg-emerald-500/5'
  }
  if (action === 'failed') {
    return 'border-red-200 bg-red-50/40 dark:border-red-500/20 dark:bg-red-500/5'
  }
  if (action === 'conflict') {
    return 'border-amber-200 bg-amber-50/40 dark:border-amber-500/20 dark:bg-amber-500/5'
  }
  return 'border-lf-border bg-lf-surface-muted/50'
}

const summaryToneClass = (tone: SummaryTone): string => {
  switch (tone) {
    case 'emerald':
      return 'bg-emerald-50 text-emerald-700 dark:bg-emerald-500/10 dark:text-emerald-300'
    case 'blue':
      return 'bg-blue-50 text-blue-700 dark:bg-blue-500/10 dark:text-blue-300'
    case 'purple':
      return 'bg-purple-50 text-purple-700 dark:bg-purple-500/10 dark:text-purple-300'
    case 'amber':
      return 'bg-amber-50 text-amber-700 dark:bg-amber-500/10 dark:text-amber-300'
    case 'red':
      return 'bg-red-50 text-red-700 dark:bg-red-500/10 dark:text-red-300'
    default:
      return 'bg-slate-100 text-lf-text-muted dark:bg-white/10'
  }
}

const getDetail = (row: ResultRow): string => {
  if (row.action === 'created') {
    return t('workspace.uploadResult.details.created')
  }
  if (row.action === 'incremental_updated') {
    return t('workspace.uploadResult.details.incrementalUpdated')
  }
  if (row.action === 'replaced') {
    return t('workspace.uploadResult.details.replaced')
  }
  if (row.action === 'conflict') {
    return t('workspace.uploadResult.details.conflict', {
      name: row.existing_resource?.name ?? row.path,
    })
  }
  if (row.action === 'failed') {
    return row.error || t('workspace.uploadResult.details.failed')
  }
  if (row.rowType === 'skipped') {
    if (row.item.precheck.action === 'duplicate') {
      return t('workspace.uploadResult.details.skippedDuplicate')
    }
    if (row.item.precheck.action === 'conflict') {
      return t('workspace.uploadResult.details.skippedConflict')
    }
  }
  return t('workspace.uploadResult.details.skipped')
}
</script>

<template>
  <div class="space-y-5">
    <div class="rounded-2xl border border-lf-border bg-lf-surface-muted/70 p-4 shadow-sm">
      <div class="grid grid-cols-2 gap-2 md:grid-cols-6">
        <div
          v-for="item in summaryItems"
          :key="item.key"
          class="rounded-xl px-3 py-3 text-center"
          :class="summaryToneClass(item.tone)"
        >
          <div class="text-2xl font-bold">{{ item.value }}</div>
          <div class="mt-1 text-xs">{{ item.label }}</div>
        </div>
      </div>
    </div>

    <div class="max-h-[52vh] space-y-3 overflow-y-auto pr-1">
      <div
        v-for="row in rows"
        :key="`${row.rowType}:${row.path}:${row.action}`"
        class="rounded-2xl border p-4 transition-colors"
        :class="rowToneClass(row.action)"
      >
        <div class="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
          <div class="min-w-0 flex-1">
            <div class="flex flex-wrap items-center gap-2">
              <div class="min-w-0 truncate text-sm font-semibold text-lf-text-strong">
                {{ row.path }}
              </div>
              <NTag :type="tagType(row.action)" size="small" :bordered="false">
                {{ t(`workspace.uploadResult.actions.${row.action}`) }}
              </NTag>
            </div>
            <p class="mt-1 text-xs leading-5 text-lf-text-muted">
              {{ getDetail(row) }}
            </p>
          </div>
        </div>
      </div>
    </div>

    <div class="flex justify-end border-t border-lf-border pt-4">
      <NButton type="primary" @click="emit('close')">
        {{ t('workspace.common.confirm') }}
      </NButton>
    </div>
  </div>
</template>
