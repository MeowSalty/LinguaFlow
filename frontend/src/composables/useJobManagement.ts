import type { Ref } from 'vue'

import { useJobColumns } from './useJobColumns'
import { useJobActions } from './useJobActions'

export type { JobTargetMode, JobFormModel } from './useJobActions'

export function useJobManagement(
  projectId: Ref<number | null>,
  onJobCreated?: () => Promise<void>,
) {
  const columns = useJobColumns()
  const actions = useJobActions(projectId, onJobCreated)
  return { ...columns, ...actions }
}
