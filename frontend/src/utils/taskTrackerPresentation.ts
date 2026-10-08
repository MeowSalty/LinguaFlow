import type { Operation, OperationsSummary } from '@/api/operations'
import { operationKey } from '@/utils/operationQuery'

type TrackerInput = {
  summary: OperationsSummary | null
  active: readonly Operation[]
  terminal: readonly Operation[]
  retainTerminal: boolean
  hiddenTerminalKeys: readonly string[]
}

export function taskTrackerPresentation(input: TrackerInput) {
  const counts = input.summary?.total
  const activeCount = counts
    ? counts.running + counts.pending + counts.paused + counts.waiting_retry + counts.needs_action
    : null
  const hidden = new Set(input.hiddenTerminalKeys)
  const reminders = input.retainTerminal
    ? input.terminal.filter((task) => !hidden.has(operationKey(task)))
    : []
  // Count reminders before truncation: active rows can fill all twenty visible slots.
  const failedCount = reminders.filter((task) => task.status === 'failed').length
  const needsActionCount = counts?.needs_action ?? null
  const waitingRetryCount = counts?.waiting_retry ?? null
  const attention = failedCount
    ? 'failed'
    : needsActionCount
      ? 'needs_action'
      : waitingRetryCount
        ? 'waiting_retry'
        : 'normal'

  return {
    activeCount,
    countLabel: activeCount === null ? '—' : activeCount > 99 ? '99+' : String(activeCount),
    reminders,
    displayed: [...input.active, ...reminders].slice(0, 20),
    failedCount,
    needsActionCount,
    waitingRetryCount,
    attention,
  }
}
