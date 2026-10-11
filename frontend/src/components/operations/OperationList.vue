<script setup lang="ts">
import type { Operation } from '@/api/operations'
import { useI18n } from 'vue-i18n'
import { operationKey } from '@/utils/operationQuery'
import { formatDateTime } from '@/utils/datetime'
import { taskHistoryDeleteOption } from '@/utils/taskHistoryPresentation'
import { toTaskHistoryItem, taskHistoryKey } from '@/api/task-history'
import { useTaskMutationsStore } from '@/stores/taskMutations'
import IconCarbonOverflowMenuVertical from '~icons/carbon/overflow-menu-vertical'
const props = defineProps<{
  items: Operation[]
  loading?: boolean
  selecting?: boolean
  selectedKeys?: string[]
  selectionLocked?: boolean
}>()
const emit = defineEmits<{
  open: [operation: Operation]
  delete: [operation: Operation]
  select: [operation: Operation, checked: boolean]
}>()
const { t } = useI18n()
const mutations = useTaskMutationsStore()
const selectable = (item: Operation): boolean => {
  const target = toTaskHistoryItem(item)
  return !!target?.can_delete && !mutations.isPending(target)
}
const selected = (item: Operation): boolean => {
  const target = toTaskHistoryItem(item)
  return !!target && !!props.selectedKeys?.includes(taskHistoryKey(target))
}
const menu = (item: Operation) => [
  taskHistoryDeleteOption(item.can_delete, item.status, !selectable(item)),
]
const date = (value: string): string =>
  formatDateTime(value, { dateStyle: 'short', timeStyle: 'short' })
const tone = (status: string): 'info' | 'warning' | 'success' | 'error' | 'default' =>
  status === 'running'
    ? 'info'
    : status === 'completed'
      ? 'success'
      : status === 'failed'
        ? 'error'
        : ['pausing', 'paused', 'pending', 'waiting_retry', 'needs_action'].includes(status)
          ? 'warning'
          : 'default'
const progress = (item: Operation): string =>
  item.task_type === 'storage'
    ? t(`operations.storageTask.cleanup.${item.cleanup_status}`)
    : item.task_type === 'glossary_sync'
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
      class="hidden grid-cols-[minmax(120px,2fr)_1fr_90px_minmax(100px,1fr)_140px_120px] gap-4 border-b border-lf-border-soft bg-lf-surface-muted px-5 py-3 text-xs text-lf-text-muted lg:grid"
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
      class="grid grid-cols-[minmax(0,1fr)_auto] gap-3 border-b border-lf-border-soft p-4 last:border-0 lg:grid-cols-[minmax(120px,2fr)_1fr_90px_minmax(100px,1fr)_140px_120px] lg:items-center lg:gap-4"
    >
      <div class="col-start-1 row-start-1 flex min-w-0 items-start gap-2 lg:col-auto lg:row-auto">
        <NCheckbox
          v-if="props.selecting"
          class="mt-0.5"
          :checked="selected(item)"
          :disabled="
            props.selectionLocked ||
            !selectable(item) ||
            (!selected(item) && (props.selectedKeys?.length ?? 0) >= 100)
          "
          :aria-disabled="
            props.selectionLocked ||
            !selectable(item) ||
            (!selected(item) && (props.selectedKeys?.length ?? 0) >= 100)
          "
          :aria-label="
            t('taskHistory.selectRecord', {
              type: t(`operations.${item.task_type}`),
              id: item.task_id,
            })
          "
          @update:checked="(value: boolean) => emit('select', item, value)"
        />
        <div class="min-w-0">
          <div class="truncate font-medium text-lf-text-strong" :title="item.project_name">
            {{ item.project_name }}
          </div>
          <div class="mt-1 text-xs text-lf-text-subtle">#{{ item.task_id }}</div>
          <p
            v-if="props.selecting && item.task_type === 'storage'"
            class="mt-1 text-xs text-lf-text-muted"
          >
            {{ t('taskHistory.storageHint') }}
          </p>
        </div>
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
      <div class="col-start-2 row-start-3 flex justify-end gap-1 lg:col-auto lg:row-auto">
        <NButton secondary size="small" @click="emit('open', item)">{{
          t('operations.view')
        }}</NButton>
        <NDropdown
          v-if="typeof item.can_delete === 'boolean' && item.task_type !== 'storage'"
          trigger="click"
          :options="menu(item)"
          @select="emit('delete', item)"
        >
          <NButton
            quaternary
            size="small"
            :aria-label="t('taskHistory.more')"
            data-task-history-trigger
            :title="t('taskHistory.more')"
          >
            <template #icon><IconCarbonOverflowMenuVertical /></template>
          </NButton>
        </NDropdown>
      </div>
    </div>
    <div v-if="!props.items.length" class="p-10">
      <NSpin :show="props.loading"
        ><NEmpty :description="props.loading ? t('operations.loading') : t('taskHistory.empty')"
      /></NSpin>
    </div>
  </div>
</template>
