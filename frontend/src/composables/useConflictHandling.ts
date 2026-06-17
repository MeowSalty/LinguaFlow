import { ref } from 'vue'
import { useMessage } from 'naive-ui'

import { type ApiSchemas } from '@/api/client'
import { useProjectWorkspaceStore } from '@/stores/projectWorkspace'
import { t } from '@/i18n'

type Resource = ApiSchemas['Resource']
type IncrementalUpdateResponse = ApiSchemas['IncrementalUpdateResponse']

export function useConflictHandling() {
  const message = useMessage()
  const workspace = useProjectWorkspaceStore()

  // ── 状态 ──
  const conflictDialogVisible = ref(false)
  const conflictResource = ref<Resource | null>(null)
  const conflictFile = ref<File | null>(null)
  const replacingResourceId = ref<number | null>(null)
  const incrementalResultVisible = ref(false)
  const incrementalResult = ref<IncrementalUpdateResponse | null>(null)

  // ── 方法 ──
  const handleExplorerConflict = (resource: Resource, file: File): void => {
    conflictResource.value = resource
    conflictFile.value = file
    conflictDialogVisible.value = true
  }

  const handleExplorerIncrementalResult = (result: IncrementalUpdateResponse): void => {
    incrementalResult.value = result
    incrementalResultVisible.value = true
  }

  const resetConflictState = (): void => {
    conflictResource.value = null
    conflictFile.value = null
  }

  const handleConflictReplace = async (
    projectId: number,
    reloadSegments: () => Promise<void>,
    loadResourceTree: (projectId: number) => Promise<void>,
  ): Promise<void> => {
    if (!conflictResource.value || !conflictFile.value) {
      return
    }

    conflictDialogVisible.value = false
    const resourceId = conflictResource.value.id
    const file = conflictFile.value
    resetConflictState()

    replacingResourceId.value = resourceId
    try {
      await workspace.replaceResource(projectId, resourceId, file)
      message.success(t('workspace.messages.replaceSuccess'))
      await loadResourceTree(projectId)
      if (workspace.activeResourceId === resourceId) {
        await reloadSegments()
      }
    } catch (error) {
      console.error(error)
      message.error(workspace.actionError || t('workspace.messages.replaceFailed'))
    } finally {
      replacingResourceId.value = null
    }
  }

  const handleConflictIncremental = async (
    projectId: number,
    reloadSegments: () => Promise<void>,
    loadResourceTree: (projectId: number) => Promise<void>,
  ): Promise<void> => {
    if (!conflictResource.value || !conflictFile.value) {
      return
    }

    conflictDialogVisible.value = false
    const resourceId = conflictResource.value.id
    const file = conflictFile.value
    resetConflictState()

    try {
      const result = await workspace.incrementalUpdateResource(projectId, resourceId, file)
      incrementalResult.value = result
      incrementalResultVisible.value = true
      await loadResourceTree(projectId)
      if (workspace.activeResourceId === resourceId) {
        await reloadSegments()
      }
    } catch (error) {
      console.error(error)
      message.error(workspace.actionError || t('workspace.messages.incrementalUpdateFailed'))
    }
  }

  const confirmIncrementalResult = (): void => {
    incrementalResultVisible.value = false
    incrementalResult.value = null
  }

  return {
    // 状态
    conflictDialogVisible,
    conflictResource,
    conflictFile,
    replacingResourceId,
    incrementalResultVisible,
    incrementalResult,
    // 方法
    handleExplorerConflict,
    handleExplorerIncrementalResult,
    resetConflictState,
    handleConflictReplace,
    handleConflictIncremental,
    confirmIncrementalResult,
  }
}
