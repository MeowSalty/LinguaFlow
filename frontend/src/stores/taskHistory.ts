import { defineStore } from 'pinia'
import { onScopeDispose, ref, shallowRef, watch } from 'vue'
import { ApiError } from '@/api/utils'
import { captureSession, isSessionCurrent, onSessionChange } from '@/api/session-context'
import {
  batchDeleteTaskHistory,
  deleteTaskHistory,
  taskHistoryErrorMessage,
  taskHistoryKey,
  validTaskHistoryTarget,
  type TaskHistoryItem,
  type TaskHistoryResult,
  type TaskHistoryTarget,
  TaskHistoryApiError,
} from '@/api/task-history'
import { useOperationsStore } from './operations'
import { useTaskMutationsStore } from './taskMutations'
import { useGlobalJobTrackerStore } from './globalJobTracker'
import { useJobStore } from './job'
import { useGlossaryStore } from './glossary'
import { usePreferencesStore } from './preferences'
import { useStatsStore } from './stats'
import { t } from '@/i18n'

export const useTaskHistoryStore = defineStore('taskHistory', () => {
  const operations = useOperationsStore(),
    mutations = useTaskMutationsStore()
  const tracker = useGlobalJobTrackerStore(),
    jobs = useJobStore(),
    glossary = useGlossaryStore()
  const preferences = usePreferencesStore(),
    stats = useStatsStore()
  const confirming = ref(false),
    submitting = ref(false)
  const targets = shallowRef<TaskHistoryItem[]>([])
  const results = shallowRef<TaskHistoryResult[]>([])
  const error = ref<string | null>(null)
  const lastRemoved = shallowRef<TaskHistoryTarget[]>([]),
    removalRevision = ref(0)
  let generation = 0
  const reset = () => {
    generation++
    confirming.value = false
    submitting.value = false
    targets.value = []
    results.value = []
    error.value = null
    lastRemoved.value = []
  }
  onScopeDispose(onSessionChange(reset))
  const changed = () => {
    return targets.value.some((target) => {
      const latest = operations.getTaskCapability(target)
      return (
        mutations.isPending(target) ||
        (latest && (latest.can_delete !== true || latest.status !== target.status))
      )
    })
  }
  watch(
    () => operations.capabilityRevision,
    () => {
      if (confirming.value && !submitting.value && changed()) {
        confirming.value = false
        error.value = t('taskHistoryErrors.changed')
      }
    },
  )
  const requestDelete = (items: TaskHistoryItem[]): void => {
    if (submitting.value) return
    if (
      !items.length ||
      items.length > 100 ||
      items.some(
        (item) =>
          !validTaskHistoryTarget(item) ||
          item.can_delete !== true ||
          !['completed', 'failed', 'cancelled'].includes(item.status) ||
          mutations.isPending(item),
      ) ||
      new Set(items.map(taskHistoryKey)).size !== items.length
    ) {
      error.value = t('taskHistoryErrors.changed')
      return
    }
    targets.value = items.map((item) => ({ ...item }))
    error.value = null
    results.value = []
    confirming.value = true
  }
  const cancel = (): void => {
    if (!submitting.value) confirming.value = false
  }
  const clearResults = (): void => {
    if (!submitting.value) {
      results.value = []
      error.value = null
    }
  }
  const removeSnapshots = (removed: TaskHistoryTarget[], announce: boolean): void => {
    if (!removed.length) return
    // Dependent stores are fenced before revision subscribers can start fresh reads.
    jobs.removeHistories(removed)
    if (
      removed.some(
        (target) => target.kind === 'translation' && String(tracker.drawerJobId) === target.id,
      )
    )
      tracker.closeDetail()
    glossary.removeTaskHistories(removed)
    preferences.removeHiddenTerminals(removed.map((target) => `${target.kind}:${target.id}`))
    stats.invalidate(false)
    operations.invalidateTasks(removed, false)
    if (announce) {
      lastRemoved.value = removed.map(({ kind, id, project_id }) => ({ kind, id, project_id }))
      removalRevision.value++
    }
  }
  const reconcile = async (
    outcomes: TaskHistoryResult[],
    current: () => boolean,
    viewCurrent: () => boolean,
  ): Promise<void> => {
    const removed = outcomes.filter((item) => ['deleted', 'not_found'].includes(item.status))
    // Fence confirmed removals immediately, before any independent verification awaits.
    removeSnapshots(removed, true)
    const others = outcomes.filter((item) => !['deleted', 'not_found'].includes(item.status))
    // Bounded verification; a lost response must never become an automatic write retry.
    const unreadable: TaskHistoryTarget[] = []
    const verificationSignal = AbortSignal.timeout(15_000)
    let next = 0,
      readFailed = false
    await Promise.all(
      Array.from({ length: Math.min(4, others.length) }, async () => {
        while (next < others.length && current()) {
          const target = others[next++]!
          try {
            const detail =
              target.kind === 'translation'
                ? await operations.queryTranslation(target.id, verificationSignal)
                : await operations.querySync(target.project_id, target.id, verificationSignal)
            if (!current()) return
            operations.projectTask(
              { task_type: target.kind, task_id: target.id, project_id: target.project_id },
              detail,
            )
          } catch (cause) {
            if (!current()) return
            if (cause instanceof ApiError && [403, 404].includes(cause.status ?? 0))
              unreadable.push(target)
            else readFailed = true
          }
        }
      }),
    )
    if (!current()) return
    removeSnapshots(unreadable, true)
    // One refresh for the entire batch, irrespective of per-item results.
    if (!removed.length && !unreadable.length) operations.invalidateReads()
    else {
      stats.publishInvalidation()
      operations.publishHistoryChange()
    }
    let timeout: ReturnType<typeof setTimeout> | undefined
    const refreshed = await Promise.race([
      Promise.allSettled([operations.refresh(), jobs.refreshHistory()]).then((results) =>
        results.every((result) => result.status === 'fulfilled'),
      ),
      new Promise<boolean>((resolve) => {
        timeout = setTimeout(() => resolve(false), 15_000)
      }),
    ]).finally(() => clearTimeout(timeout))
    if (!current() || !viewCurrent()) return
    if (
      readFailed ||
      !refreshed ||
      operations.listError ||
      operations.summaryError ||
      operations.filteredSummaryError ||
      jobs.jobsError
    )
      error.value = removed.some((item) => item.status === 'deleted')
        ? t('taskHistoryErrors.refreshFailed')
        : t('taskHistoryErrors.requestFailed')
  }
  const submit = async (): Promise<void> => {
    if (!confirming.value || submitting.value) return
    if (changed()) {
      confirming.value = false
      error.value = t('taskHistoryErrors.changed')
      return
    }
    const session = captureSession(),
      request = ++generation
    const current = () => isSessionCurrent(session) && request === generation
    const selected = targets.value.map(({ kind, id, project_id }) => ({ kind, id, project_id }))
    submitting.value = true
    error.value = null
    try {
      const outcome = await mutations.run(
        selected,
        'delete',
        async (): Promise<TaskHistoryResult[] | null> => {
          let outcome: TaskHistoryResult[]
          try {
            if (selected.length === 1) {
              await deleteTaskHistory(selected[0]!)
              outcome = [{ ...selected[0]!, status: 'deleted' }]
            } else {
              const batch = await batchDeleteTaskHistory(selected)
              outcome = batch.items
              if (batch.contractError) error.value = t('taskHistoryErrors.contract')
            }
          } catch (cause) {
            if (!current()) return null
            if (cause instanceof ApiError && [400, 401].includes(cause.status ?? 0)) {
              error.value = taskHistoryErrorMessage(cause)
              confirming.value = false
              return null
            }
            const code = cause instanceof TaskHistoryApiError ? cause.error_code : undefined
            const status: TaskHistoryResult['status'] =
              cause instanceof ApiError
                ? cause.status === 404
                  ? 'not_found'
                  : cause.status === 403
                    ? 'forbidden'
                    : code === 'task_not_terminal'
                      ? 'not_terminal'
                      : code === 'task_busy'
                        ? 'busy'
                        : code === 'task_cleanup_deferred'
                          ? 'deferred'
                          : 'unknown'
                : 'unknown'
            outcome = selected.map((target) => ({ ...target, status }))
            if (status === 'unknown') error.value = t('taskHistoryErrors.resultUnknown')
          }
          return outcome
        },
      )
      if (!current() || !outcome) return
      results.value = outcome
      confirming.value = false
      submitting.value = false
      await reconcile(outcome, () => isSessionCurrent(session), current)
    } catch (cause) {
      if (current()) error.value = taskHistoryErrorMessage(cause)
    } finally {
      if (current()) submitting.value = false
    }
  }
  return {
    confirming,
    submitting,
    targets,
    results,
    error,
    lastRemoved,
    removalRevision,
    requestDelete,
    cancel,
    submit,
    clearResults,
  }
})
