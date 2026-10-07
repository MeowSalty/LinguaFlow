import { defineStore } from 'pinia'
import { onScopeDispose, shallowRef } from 'vue'
import {
  captureSession,
  isSessionCurrent,
  onSessionChange,
  StaleSessionError,
} from '@/api/session-context'
import { taskHistoryKey, TaskHistoryApiError, type TaskHistoryTarget } from '@/api/task-history'

export class TaskMutationBusyError extends TaskHistoryApiError {
  constructor() {
    super(409, 'task_busy')
  }
}
/** Shared by every view, including batches. Acquisition is synchronous and all-or-nothing. */
export const useTaskMutationsStore = defineStore('taskMutations', () => {
  const pending = shallowRef(new Map<string, symbol>())
  const isPending = (target: TaskHistoryTarget): boolean =>
    pending.value.has(taskHistoryKey(target))
  onScopeDispose(
    onSessionChange(() => {
      pending.value = new Map()
    }),
  )
  const run = async <T>(
    targets: TaskHistoryTarget[],
    action: string,
    work: () => Promise<T>,
  ): Promise<T> => {
    const keys = [...new Set(targets.map(taskHistoryKey))]
    if (keys.some((key) => pending.value.has(key))) throw new TaskMutationBusyError()
    const session = captureSession(),
      owner = Symbol(action)
    pending.value = new Map([...pending.value, ...keys.map((key) => [key, owner] as const)])
    try {
      const result = await work()
      if (!isSessionCurrent(session)) throw new StaleSessionError()
      return result
    } finally {
      const next = new Map(pending.value)
      for (const key of keys) if (next.get(key) === owner) next.delete(key)
      pending.value = next
    }
  }
  return { isPending, run }
})
