<script setup lang="ts">
import { computed, nextTick, onMounted, onBeforeUnmount, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NCollapse, NCollapseItem, NModal } from 'naive-ui'
import { taskHistoryKey } from '@/api/task-history'
import { useTaskHistoryStore } from '@/stores/taskHistory'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const history = useTaskHistoryStore()
const cancelButton = ref<{ $el: HTMLButtonElement } | null>(null)
const showResults = ref(false)
let returnFocus: HTMLElement | null = null
let lastTrigger: HTMLElement | null = null
const rememberTrigger = (event: Event): void => {
  const trigger =
    event.target instanceof Element
      ? event.target.closest<HTMLElement>('[data-task-history-trigger]')
      : null
  if (trigger) lastTrigger = trigger
}
onMounted(() => {
  document.addEventListener('pointerdown', rememberTrigger, true)
  document.addEventListener('focusin', rememberTrigger, true)
})
onBeforeUnmount(() => {
  document.removeEventListener('pointerdown', rememberTrigger, true)
  document.removeEventListener('focusin', rememberTrigger, true)
})
const show = computed(() => history.confirming || showResults.value)
const counts = computed(() => ({
  translation: history.targets.filter((item) => item.kind === 'translation').length,
  glossary_sync: history.targets.filter((item) => item.kind === 'glossary_sync').length,
}))
const resultCounts = computed(() => ({
  deleted: history.results.filter((item) => item.status === 'deleted').length,
  missing: history.results.filter((item) => item.status === 'not_found').length,
  remaining: history.results.filter((item) => !['deleted', 'not_found'].includes(item.status))
    .length,
}))
const close = (): void => {
  if (history.submitting) return
  history.cancel()
  showResults.value = false
}
const restoreFocus = (): void => {
  const target = returnFocus?.isConnected
    ? returnFocus
    : document.querySelector<HTMLElement>('[data-task-history-focus], main h1, main button')
  if (target && !target.hasAttribute('tabindex') && target.tagName === 'H1') target.tabIndex = -1
  target?.focus()
}
watch(
  () => history.confirming,
  (value) => {
    if (!value) return
    returnFocus = lastTrigger?.isConnected
      ? lastTrigger
      : document.activeElement instanceof HTMLElement
        ? document.activeElement
        : null
    showResults.value = false
    void nextTick(() => cancelButton.value?.$el?.focus())
  },
  { flush: 'sync' },
)
watch(
  () => history.results,
  (items) => {
    if (items.length) showResults.value = true
  },
)
watch(
  () => history.error,
  (error) => {
    if (error && !history.confirming) showResults.value = true
  },
)
watch(
  () => history.removalRevision,
  () => {
    const id = route.query.task_id ?? route.query.job_id
    const kind = route.query.task_type ?? (route.query.job_id ? 'translation' : null)
    if (
      !id ||
      !kind ||
      !history.lastRemoved.some(
        (target) =>
          target.kind === kind &&
          target.id === String(id) &&
          (route.query.project_id == null || target.project_id === Number(route.query.project_id)),
      )
    )
      return
    const query = { ...route.query }
    delete query.task_id
    delete query.job_id
    void router.replace({ path: route.path, query })
  },
)
</script>

<template>
  <NModal
    :show="show"
    preset="card"
    :title="
      history.confirming
        ? t(history.targets.length === 1 ? 'taskHistory.title' : 'taskHistory.batchTitle', {
            count: history.targets.length,
          })
        : t('taskHistory.resultsTitle')
    "
    class="w-[min(36rem,calc(100vw-2rem))]!"
    :auto-focus="false"
    :mask-closable="!history.submitting"
    :close-on-esc="!history.submitting"
    :closable="!history.submitting"
    @update:show="
      (value) => {
        if (!value) close()
      }
    "
    @after-enter="cancelButton?.$el?.focus()"
    @after-leave="restoreFocus"
  >
    <div class="max-h-[65dvh] space-y-4 overflow-y-auto text-sm leading-6">
      <template v-if="history.confirming">
        <p>{{ t('taskHistory.impact') }}</p>
        <p class="text-xs text-lf-text-muted">{{ t('taskHistory.preserved') }}</p>
        <p
          v-if="
            history.targets.some((item) => item.kind === 'translation' && item.status === 'failed')
          "
          class="text-xs text-lf-text-muted"
        >
          {{ t('taskHistory.retryLost') }}
        </p>
        <p class="font-medium">{{ t('taskHistory.breakdown', counts) }}</p>
        <NCollapse :default-expanded-names="history.targets.length === 1 ? ['targets'] : []">
          <NCollapseItem name="targets" :title="t('taskHistory.showTargets')">
            <ul class="space-y-2">
              <li
                v-for="target in history.targets"
                :key="taskHistoryKey(target)"
                class="break-words rounded-lg bg-lf-surface-muted px-3 py-2"
              >
                <span class="block font-medium">{{
                  target.project_name || `#${target.project_id}`
                }}</span>
                <span class="text-xs text-lf-text-muted"
                  >{{ t(`operations.${target.kind}`) }} #{{ target.id }}</span
                >
              </li>
            </ul>
          </NCollapseItem>
        </NCollapse>
      </template>
      <template v-else>
        <p v-if="history.results.length" role="status">
          {{ t('taskHistory.resultsSummary', resultCounts) }}
        </p>
        <p v-if="history.results.length" class="text-xs text-lf-text-muted">
          {{ t('taskHistory.resultHint') }}
        </p>
        <ul class="space-y-2">
          <li
            v-for="result in history.results"
            :key="taskHistoryKey(result)"
            class="flex flex-wrap justify-between gap-2 border-b border-lf-border-soft py-2"
          >
            <span
              >{{ t(`operations.${result.kind}`) }} #{{ result.id }} · #{{
                result.project_id
              }}</span
            >
            <span
              :class="
                ['deleted', 'not_found'].includes(result.status)
                  ? 'text-lf-text-muted'
                  : 'text-lf-warning'
              "
              >{{ t(`taskHistory.results.${result.status}`) }}</span
            >
          </li>
        </ul>
      </template>
      <NAlert v-if="history.error" type="warning" :bordered="false">{{ history.error }}</NAlert>
    </div>
    <template #footer>
      <div class="flex flex-wrap justify-end gap-2">
        <NButton ref="cancelButton" :disabled="history.submitting" @click="close">{{
          t(history.confirming ? 'common.cancel' : 'globalJobTracker.close')
        }}</NButton>
        <NButton
          v-if="history.confirming"
          type="error"
          :loading="history.submitting"
          :disabled="history.submitting || !history.targets.length"
          @click="history.submit()"
          >{{ t('taskHistory.confirm', { count: history.targets.length }) }}</NButton
        >
      </div>
    </template>
  </NModal>
</template>
