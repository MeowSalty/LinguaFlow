import { computed, shallowRef } from 'vue'
import type { ApiSchemas } from '@/api/client'

export type SourceQueueStatus = 'pending' | 'completed' | 'failed' | 'skipped'
interface SourceQueueItem {
  resource: ApiSchemas['Resource']
  file: File
  status: SourceQueueStatus
}
/** Source updates keep their own confirmation queue and never become skipped uploads. */
export function useConflictHandling() {
  const queue = shallowRef<SourceQueueItem[]>([])
  const activeIndex = shallowRef(0)
  const conflictResource = computed(() => queue.value[activeIndex.value]?.resource ?? null)
  const conflictFile = computed(() => queue.value[activeIndex.value]?.file ?? null)
  const conflictDialogVisible = shallowRef(false)
  const summary = computed(() => ({
    pending: queue.value.filter((item) => item.status === 'pending').length,
    completed: queue.value.filter((item) => item.status === 'completed').length,
    failed: queue.value.filter((item) => item.status === 'failed').length,
    skipped: queue.value.filter((item) => item.status === 'skipped').length,
  }))
  const handleExplorerConflict = (resource: ApiSchemas['Resource'], file: File): void => {
    queue.value = [...queue.value, { resource, file, status: 'pending' }]
  }
  const openNext = () => {
    const index = queue.value.findIndex((item) => item.status === 'pending')
    if (index < 0) {
      conflictDialogVisible.value = false
      return
    }
    activeIndex.value = index
    conflictDialogVisible.value = true
  }
  const settle = (status: Exclude<SourceQueueStatus, 'pending'>) => {
    queue.value = queue.value.map((item, index) =>
      index === activeIndex.value ? { ...item, status } : item,
    )
  }
  const resetConflictState = (): void => {
    queue.value = []
    activeIndex.value = 0
    conflictDialogVisible.value = false
  }
  return {
    queue,
    summary,
    conflictResource,
    conflictFile,
    conflictDialogVisible,
    handleExplorerConflict,
    openNext,
    settle,
    resetConflictState,
  }
}
