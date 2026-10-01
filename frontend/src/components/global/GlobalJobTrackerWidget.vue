<script setup lang="ts">
import { computed, watch } from 'vue'
import { storeToRefs } from 'pinia'
import { useI18n } from 'vue-i18n'
import { useOperationsStore } from '@/stores/operations'
import { usePreferencesStore } from '@/stores/preferences'
import { useAuthStore } from '@/stores/auth'
import { useUiStore } from '@/stores/ui'
import { isTerminalOperation, operationKey, operationLocation } from '@/utils/operationQuery'
import type { Operation } from '@/api/operations'
const { t } = useI18n(),
  router = useRouter(),
  operations = useOperationsStore(),
  preferences = usePreferencesStore(),
  auth = useAuthStore(),
  ui = useUiStore()
const { trackerExpanded: expanded } = storeToRefs(preferences)
const progress = (task: Operation): string => {
  if (task.task_type === 'glossary_sync')
    return t('operations.syncProgress', {
      processed: task.progress.processed_segments,
      total: task.progress.total_segments,
    })
  const { progress_completed: completed, progress_total: total } = task.progress
  return completed == null || total == null ? '—' : `${completed} / ${total}`
}
const activeCount = computed(() =>
  operations.summary
    ? operations.summary.total.running +
      operations.summary.total.pending +
      operations.summary.total.paused
    : null,
)
const displayed = computed(() =>
  [
    ...operations.active,
    ...(preferences.retainTerminal
      ? operations.terminal.filter(
          (task) => !preferences.hiddenTerminalKeys.includes(operationKey(task)),
        )
      : []),
  ].slice(0, 20),
)
const open = (task: Operation): void => {
  expanded.value = false
  void router.push(operationLocation(task))
}
const clear = (): void => {
  for (const task of operations.terminal) preferences.hideTerminal(operationKey(task))
}
const viewAll = (): void => {
  expanded.value = false
  void router.push('/operations')
}
watch(
  () => auth.user?.id,
  (id) => {
    if (id) operations.start()
    else operations.stop()
  },
  { immediate: true },
)
</script>
<template>
  <div
    v-if="auth.user"
    class="fixed right-4 z-50 md:right-6"
    :class="ui.selectionBarActive ? 'bottom-24' : 'bottom-6'"
  >
    <div
      v-if="expanded"
      class="absolute bottom-14 right-0 flex max-h-[65vh] w-84 max-w-[calc(100vw-2rem)] flex-col overflow-hidden rounded-lf-card border border-lf-border-soft bg-lf-surface shadow-lg"
    >
      <div class="flex items-center justify-between border-b border-lf-border-soft p-4">
        <strong class="text-sm">{{ t('operations.widget') }}</strong
        ><NButton quaternary size="tiny" @click="expanded = false">{{
          t('operations.close')
        }}</NButton>
      </div>
      <div v-if="operations.discoveryError" class="p-3 text-xs text-lf-warning">
        {{ t('operations.stale') }}
      </div>
      <div class="overflow-y-auto">
        <div
          v-for="task in displayed"
          :key="operationKey(task)"
          class="flex items-center gap-2 border-b border-lf-border-soft p-4 last:border-0"
        >
          <button type="button" class="min-w-0 flex-1 cursor-pointer text-left" @click="open(task)">
            <div class="truncate text-sm font-medium">{{ task.project_name }}</div>
            <div class="mt-1 text-xs text-lf-text-muted">
              {{ t(`operations.${task.task_type}`) }} #{{ task.task_id }} ·
              {{ t(`operations.${task.status}`) }}
            </div>
            <div class="mt-1 text-xs tabular-nums text-lf-text-subtle">
              {{ t('operations.progress') }} · {{ progress(task) }}
            </div>
          </button>
          <NButton
            v-if="isTerminalOperation(task.status)"
            quaternary
            size="tiny"
            :aria-label="t('operations.hide')"
            @click="preferences.hideTerminal(operationKey(task))"
            >×</NButton
          >
        </div>
        <NEmpty v-if="!displayed.length" class="p-8" :description="t('operations.empty')" />
      </div>
      <div class="flex items-center justify-between border-t border-lf-border-soft p-3">
        <NButton quaternary size="small" @click="clear">{{
          t('operations.clearCompleted')
        }}</NButton
        ><NButton secondary size="small" @click="viewAll">{{ t('operations.viewAll') }}</NButton>
      </div>
    </div>
    <NButton round type="primary" :aria-expanded="expanded" @click="expanded = !expanded"
      >{{ t('operations.widget')
      }}<span class="ml-2 tabular-nums">{{ activeCount ?? '—' }}</span></NButton
    >
  </div>
</template>
