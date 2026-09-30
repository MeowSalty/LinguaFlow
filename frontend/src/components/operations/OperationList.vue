<script setup lang="ts">
import type { Operation } from '@/api/operations'
import { useI18n } from 'vue-i18n'
import { operationKey } from '@/utils/operationQuery'
import { formatDateTime } from '@/utils/datetime'
const props = defineProps<{ items: Operation[]; loading?: boolean }>()
const emit = defineEmits<{ open: [operation: Operation] }>()
const { t } = useI18n()
const date = (value: string): string =>
  formatDateTime(value, { dateStyle: 'short', timeStyle: 'short' })
const tone = (status: string): 'info' | 'warning' | 'success' | 'error' | 'default' =>
  status === 'running'
    ? 'info'
    : status === 'completed'
      ? 'success'
      : status === 'failed'
        ? 'error'
        : status === 'paused' || status === 'pending'
          ? 'warning'
          : 'default'
const progress = (item: Operation): string =>
  item.task_type === 'glossary_sync'
    ? t('operations.syncProgress', {
        processed: item.progress.processed_segments,
        total: item.progress.total_segments,
      })
    : item.progress.progress_completed == null || item.progress.progress_total == null
      ? '—'
      : `${item.progress.progress_completed} / ${item.progress.progress_total}`
</script>
<template>
  <div class="lf-panel overflow-hidden">
    <div
      class="hidden grid-cols-[minmax(120px,2fr)_1fr_90px_minmax(120px,1fr)_160px_80px] gap-4 border-b border-lf-border-soft bg-lf-surface-muted px-5 py-3 text-xs text-lf-text-muted lg:grid"
    >
      <span>{{ t('operations.project') }}</span
      ><span>{{ t('operations.type') }}</span
      ><span>{{ t('operations.status') }}</span
      ><span>{{ t('operations.progress') }}</span
      ><span>{{ t('operations.updatedAt') }}</span
      ><span />
    </div>
    <div
      v-for="item in props.items"
      :key="operationKey(item)"
      class="grid grid-cols-[minmax(0,1fr)_auto] gap-3 border-b border-lf-border-soft p-5 last:border-0 lg:grid-cols-[minmax(120px,2fr)_1fr_90px_minmax(120px,1fr)_160px_80px] lg:items-center lg:gap-4"
    >
      <div class="col-start-1 row-start-1 min-w-0 lg:col-auto lg:row-auto">
        <div class="truncate font-medium text-lf-text-strong" :title="item.project_name">
          {{ item.project_name }}
        </div>
        <div class="mt-1 text-xs text-lf-text-subtle">#{{ item.task_id }}</div>
      </div>
      <div class="col-start-1 row-start-2 text-sm lg:col-auto lg:row-auto">
        {{ t(`operations.${item.task_type}`) }}
        <div v-if="item.task_type === 'translation'" class="mt-1 text-xs text-lf-text-subtle">
          {{ t(`operations.${item.trigger_type}`) }}
        </div>
      </div>
      <div class="col-start-2 row-start-1 text-right lg:col-auto lg:row-auto lg:text-left">
        <NTag :type="tone(item.status)" :bordered="false" size="small">{{
          t(`operations.${item.status}`)
        }}</NTag>
      </div>
      <div
        class="col-start-2 row-start-2 text-right text-xs tabular-nums text-lf-text-muted lg:col-auto lg:row-auto lg:text-left"
      >
        {{ progress(item) }}
      </div>
      <time
        class="col-start-1 row-start-3 self-center text-xs text-lf-text-subtle lg:col-auto lg:row-auto"
        :datetime="item.updated_at"
        >{{ date(item.updated_at) }}</time
      >
      <NButton
        class="col-start-2 row-start-3 lg:col-auto lg:row-auto"
        secondary
        size="small"
        @click="emit('open', item)"
        >{{ t('operations.view') }}</NButton
      >
    </div>
    <div v-if="!props.items.length" class="p-10">
      <NSpin :show="props.loading"
        ><NEmpty :description="props.loading ? t('operations.loading') : t('operations.empty')"
      /></NSpin>
    </div>
  </div>
</template>
