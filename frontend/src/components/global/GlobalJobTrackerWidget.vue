<script setup lang="ts">
import { computed, nextTick, ref, useId, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NButton } from 'naive-ui'
import { useOperationsStore } from '@/stores/operations'
import { usePreferencesStore } from '@/stores/preferences'
import { useAuthStore } from '@/stores/auth'
import IconCarbonTask from '~icons/carbon/task'
import IconCarbonClose from '~icons/carbon/close'
import IconCarbonError from '~icons/carbon/error'
import IconCarbonWarning from '~icons/carbon/warning'
import IconCarbonTime from '~icons/carbon/time'
import { isTerminalOperation, operationKey, operationLocation } from '@/utils/operationQuery'
import { taskTrackerPresentation } from '@/utils/taskTrackerPresentation'
import type { Operation } from '@/api/operations'

const { t } = useI18n()
const router = useRouter(),
  route = useRoute()
const operations = useOperationsStore(),
  preferences = usePreferencesStore(),
  auth = useAuthStore()
const expanded = ref(false)
const trigger = ref<InstanceType<typeof NButton> | null>(null)
const panel = ref<HTMLElement | null>(null)
const id = useId()
const panelId = `${id}-tasks`,
  titleId = `${id}-title`,
  statusId = `${id}-status`
const presentation = computed(() =>
  taskTrackerPresentation({
    summary: operations.summary,
    active: operations.active,
    terminal: operations.terminal,
    retainTerminal: preferences.retainTerminal,
    hiddenTerminalKeys: preferences.hiddenTerminalKeys,
  }),
)
const attentionIcon = computed(
  () =>
    ({
      failed: IconCarbonError,
      needs_action: IconCarbonWarning,
      waiting_retry: IconCarbonTime,
      normal: IconCarbonTask,
    })[presentation.value.attention],
)
const attentionType = computed(() =>
  presentation.value.attention === 'failed'
    ? 'error'
    : presentation.value.attention === 'normal'
      ? 'default'
      : 'warning',
)
const attentionLabels = computed(() =>
  [
    presentation.value.failedCount
      ? t('operations.trackerFailed', { count: presentation.value.failedCount })
      : '',
    presentation.value.needsActionCount
      ? t('operations.trackerNeedsAction', { count: presentation.value.needsActionCount })
      : '',
    presentation.value.waitingRetryCount
      ? t('operations.trackerWaitingRetry', { count: presentation.value.waitingRetryCount })
      : '',
  ].filter(Boolean),
)
const readError = computed(() => !!(operations.discoveryError || operations.summaryError))
// 空闲且无异常时只留图标，避免页头常驻一块“当前任务 0”文字。
const quiet = computed(
  () =>
    presentation.value.countLabel === '0' &&
    presentation.value.attention === 'normal' &&
    !readError.value,
)
const hasSnapshot = computed(
  () => operations.summary !== null || presentation.value.displayed.length > 0,
)
const readState = computed(() =>
  readError.value
    ? hasSnapshot.value
      ? 'stale'
      : 'error'
    : !operations.initialized
      ? 'loading'
      : 'ready',
)
const readMessage = computed(() =>
  readState.value === 'stale'
    ? t('operations.stale')
    : readState.value === 'error'
      ? t('operations.trackerInitialError')
      : '',
)
const triggerLabel = computed(() =>
  [
    `${t('operations.widget')} ${presentation.value.activeCount ?? '—'}`,
    ...attentionLabels.value,
    ...(readError.value ? [t('operations.trackerReadError')] : []),
  ].join('，'),
)
async function setExpanded(value: boolean): Promise<void> {
  expanded.value = value
  if (value) {
    await nextTick()
    panel.value?.focus({ preventScroll: true })
  }
}
function closeWithFocus(): void {
  expanded.value = false
  trigger.value?.$el.focus({ preventScroll: true })
}
function onFocusout(event: FocusEvent): void {
  const root = event.currentTarget as HTMLElement
  if (event.relatedTarget instanceof Node && !root.contains(event.relatedTarget))
    expanded.value = false
}
const progress = (task: Operation): string => {
  if (task.task_type === 'storage')
    return t(`operations.storageTask.cleanup.${task.cleanup_status}`)
  if (task.task_type === 'glossary_sync')
    return t('operations.syncProgress', {
      processed: task.progress.processed_segments,
      total: task.progress.total_segments,
    })
  const { progress_completed: completed, progress_total: total } = task.progress
  return completed == null || total == null ? '—' : `${completed} / ${total}`
}
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
  () => route.fullPath,
  () => {
    expanded.value = false
  },
)
watch(
  () => auth.user?.id,
  (userId) => {
    expanded.value = false
    if (userId) operations.start()
    else operations.stop()
  },
  { immediate: true },
)
</script>

<template>
  <div
    v-if="auth.user"
    class="relative shrink-0"
    data-testid="global-job-tracker"
    :data-attention="presentation.attention"
    :data-read-state="readState"
    @keydown.esc.stop.prevent="closeWithFocus"
    @focusout="onFocusout"
  >
    <NPopover
      :show="expanded"
      trigger="click"
      placement="bottom-end"
      :flip="false"
      :to="false"
      :z-index="30"
      :show-arrow="false"
      :animated="false"
      raw
      @update:show="setExpanded"
    >
      <template #trigger>
        <NButton
          ref="trigger"
          data-testid="global-job-tracker-trigger"
          :secondary="!quiet"
          :quaternary="quiet"
          :circle="quiet"
          size="small"
          :type="attentionType"
          :aria-label="triggerLabel"
          :title="triggerLabel"
          :aria-expanded="expanded"
          :aria-controls="panelId"
          aria-haspopup="dialog"
          @keydown.down.prevent="setExpanded(true)"
        >
          <template #icon><component :is="attentionIcon" aria-hidden="true" /></template>
          <template v-if="!quiet">
            <span class="hidden sm:inline">{{ t('operations.widget') }}</span>
            <span class="tabular-nums sm:ml-2">{{ presentation.countLabel }}</span>
          </template>
          <IconCarbonCloudOffline
            v-if="readError"
            class="ml-1 text-lf-warning"
            aria-hidden="true"
          />
        </NButton>
      </template>
      <!-- 面板右缘跟随触发按钮（头像左侧），max-w 需扣掉头像与页边距宽度，窄屏才不会溢出视口左缘 -->
      <section
        :id="panelId"
        ref="panel"
        role="dialog"
        :aria-labelledby="titleId"
        :aria-describedby="statusId"
        tabindex="-1"
        data-testid="global-job-tracker-panel"
        class="flex max-h-[min(65dvh,calc(100dvh-5rem))] w-84 max-w-[calc(100vw-5rem)] flex-col overflow-hidden rounded-lf-card border border-lf-border-soft bg-lf-surface text-lf-text shadow-lg outline-none"
      >
        <div
          class="flex shrink-0 items-center justify-between gap-2 border-b border-lf-border-soft px-4 py-3"
        >
          <strong :id="titleId" class="text-sm">{{ t('operations.widget') }}</strong>
          <NButton
            quaternary
            circle
            size="tiny"
            :aria-label="t('operations.close')"
            :title="t('operations.close')"
            @click="closeWithFocus"
          >
            <template #icon><IconCarbonClose aria-hidden="true" /></template>
          </NButton>
        </div>
        <div
          :id="statusId"
          data-testid="global-job-tracker-status"
          class="shrink-0"
          aria-live="polite"
        >
          <div
            v-if="attentionLabels.length"
            class="flex flex-wrap gap-x-3 gap-y-1 border-b border-lf-border-soft px-4 py-2 text-xs"
          >
            <span v-if="presentation.failedCount" class="text-lf-danger">{{
              t('operations.trackerFailed', { count: presentation.failedCount })
            }}</span>
            <span v-if="presentation.needsActionCount" class="text-lf-warning">{{
              t('operations.trackerNeedsAction', { count: presentation.needsActionCount })
            }}</span>
            <span v-if="presentation.waitingRetryCount" class="text-lf-warning">{{
              t('operations.trackerWaitingRetry', { count: presentation.waitingRetryCount })
            }}</span>
          </div>
          <div
            v-if="readError"
            class="flex items-center gap-2 border-b border-lf-border-soft px-4 py-2 text-xs text-lf-warning"
          >
            <span class="min-w-0 flex-1">{{ readMessage }}</span>
            <NButton
              quaternary
              size="tiny"
              :loading="operations.summaryLoading"
              @click="operations.refresh()"
              >{{ t('operations.refresh') }}</NButton
            >
          </div>
        </div>
        <div
          data-testid="global-job-tracker-list"
          class="min-h-0 flex-1 overflow-y-auto overscroll-contain"
        >
          <div
            v-if="readState === 'loading'"
            class="p-6 text-center text-sm text-lf-text-muted"
            role="status"
          >
            {{ t('operations.loading') }}
          </div>
          <div
            v-for="task in presentation.displayed"
            :key="operationKey(task)"
            class="flex items-center gap-2 border-b border-lf-border-soft p-4 last:border-0"
          >
            <button
              type="button"
              class="min-w-0 flex-1 cursor-pointer rounded text-left focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-brand-500"
              @click="open(task)"
            >
              <div class="truncate text-sm font-medium" :title="task.project_name">
                {{ task.project_name }}
              </div>
              <div class="mt-1 text-xs break-words text-lf-text-muted">
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
          <NEmpty
            v-if="readState === 'ready' && !presentation.displayed.length"
            class="p-8"
            :description="t('operations.trackerEmpty')"
          />
        </div>
        <div
          class="flex shrink-0 flex-wrap items-center justify-between gap-2 border-t border-lf-border-soft p-3"
        >
          <NButton
            quaternary
            size="small"
            :disabled="!presentation.reminders.length"
            @click="clear"
            >{{ t('operations.clearCompleted') }}</NButton
          >
          <NButton secondary size="small" @click="viewAll">{{ t('operations.viewAll') }}</NButton>
        </div>
      </section>
    </NPopover>
  </div>
</template>
