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

type MetricTier = 'primary' | 'secondary'

interface SummaryItem {
  key: keyof UploadExecutionResult['summary']
  label: string
  value: number
  tier: MetricTier
  dotColor: string
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
    tier: 'primary',
    dotColor: 'bg-emerald-500',
  },
  {
    key: 'incrementallyUpdated',
    label: t('workspace.uploadResult.summary.incrementallyUpdated'),
    value: props.result.summary.incrementallyUpdated,
    tier: 'secondary',
    dotColor: 'bg-blue-400',
  },
  {
    key: 'replaced',
    label: t('workspace.uploadResult.summary.replaced'),
    value: props.result.summary.replaced,
    tier: 'secondary',
    dotColor: 'bg-violet-400',
  },
  {
    key: 'conflicts',
    label: t('workspace.uploadResult.summary.conflicts'),
    value: props.result.summary.conflicts,
    tier: 'primary',
    dotColor: 'bg-amber-500',
  },
  {
    key: 'failed',
    label: t('workspace.uploadResult.summary.failed'),
    value: props.result.summary.failed,
    tier: 'primary',
    dotColor: 'bg-red-500',
  },
  {
    key: 'skipped',
    label: t('workspace.uploadResult.summary.skipped'),
    value: props.result.summary.skipped,
    tier: 'secondary',
    dotColor: 'bg-slate-400 dark:bg-slate-500',
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

const rowAccentClass = (action: ResultRow['action']): string => {
  if (action === 'failed') {
    return 'border-l-red-300 dark:border-l-red-500/60'
  }
  if (action === 'conflict') {
    return 'border-l-amber-300 dark:border-l-amber-500/60'
  }
  return 'border-l-transparent'
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
    <!-- Summary metrics -->
    <div class="grid grid-cols-3 gap-2 sm:grid-cols-3 lg:grid-cols-6">
      <div
        v-for="item in summaryItems"
        :key="item.key"
        class="flex items-center gap-2.5 rounded-lg px-3 py-2.5"
        :class="item.value === 0 ? 'opacity-40' : ''"
      >
        <span
          class="h-2 w-2 shrink-0 rounded-full"
          :class="item.dotColor"
        />
        <div class="min-w-0">
          <div
            class="leading-none font-semibold text-lf-text-strong"
            :class="item.tier === 'primary' ? 'text-xl' : 'text-base'"
          >
            {{ item.value }}
          </div>
          <div class="mt-0.5 truncate text-[11px] text-lf-text-muted">
            {{ item.label }}
          </div>
        </div>
      </div>
    </div>

    <!-- Divider -->
    <div class="border-t border-lf-border" />

    <!-- File detail list -->
    <div class="max-h-[52vh] space-y-1.5 overflow-y-auto pr-1">
      <div
        v-for="row in rows"
        :key="`${row.rowType}:${row.path}:${row.action}`"
        class="flex flex-col gap-2 rounded-lg border border-l-3 border-lf-border/60 bg-lf-surface px-4 py-3 sm:flex-row sm:items-start sm:justify-between"
        :class="rowAccentClass(row.action)"
      >
        <div class="min-w-0 flex-1">
          <div class="flex flex-wrap items-center gap-2">
            <div class="min-w-0 truncate text-sm font-medium text-lf-text-strong">
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

    <!-- Actions -->
    <div class="flex justify-end border-t border-lf-border pt-4">
      <NButton type="primary" @click="emit('close')">
        {{ t('workspace.common.confirm') }}
      </NButton>
    </div>
  </div>
</template>
