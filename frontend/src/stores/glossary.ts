import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import {
  type ApiSchemas,
  analyzeGlossarySyncImpact,
  cancelGlossarySyncTask,
  createGlossaryEntry as createGlossaryEntryRequest,
  deleteGlossaryEntry as deleteGlossaryEntryRequest,
  executeGlossarySync,
  exportGlossaryCSV as exportGlossaryCSVRequest,
  fetchGlossaryEntries,
  getGlossarySyncTaskStatus,
  importGlossaryCSV as importGlossaryCSVRequest,
  updateGlossaryEntry as updateGlossaryEntryRequest,
} from '@/api/client'
import { t } from '@/i18n'

type GlossaryEntry = ApiSchemas['GlossaryEntry']
type CreateGlossaryEntryPayload = ApiSchemas['CreateGlossaryEntryRequest']
type UpdateGlossaryEntryPayload = ApiSchemas['UpdateGlossaryEntryRequest']
type UpdateGlossaryEntryResponse = ApiSchemas['UpdateGlossaryEntryResponse']
type GlossaryImportResult = ApiSchemas['GlossaryImportResult']
type SyncImpactResponse = ApiSchemas['GlossarySyncImpactResponse']
type SyncExecuteRequest = ApiSchemas['GlossarySyncExecuteRequest']
type SyncTaskStatusResponse = ApiSchemas['GlossarySyncTaskStatusResponse']

export const useGlossaryStore = defineStore('glossary', () => {
  const items = ref<GlossaryEntry[]>([])

  const loading = ref(false)
  const creating = ref(false)
  const updating = ref(false)
  const deletingEntryIds = ref<number[]>([])

  const error = ref<string | null>(null)
  const createError = ref<string | null>(null)
  const updateError = ref<string | null>(null)
  const deleteError = ref<string | null>(null)

  const importing = ref(false)
  const importError = ref<string | null>(null)
  const importResult = ref<GlossaryImportResult | null>(null)

  const searchQuery = ref('')

  // ── 同步相关状态 ──
  type SyncStep = 'impact' | 'executing' | 'result' | 'cancelled' | 'error'

  const syncDialogVisible = ref(false) // 同步对话框是否可见
  const syncStep = ref<SyncStep>('impact') // 当前步骤
  const syncEntryId = ref<number | null>(null) // 当前同步的术语条目 ID
  const syncSource = ref('') // 术语源文（用于展示）
  const syncOldTarget = ref('') // 旧译文
  const syncNewTarget = ref('') // 新译文

  // 影响分析
  const syncImpactLoading = ref(false) // 影响分析加载中
  const syncImpactError = ref<string | null>(null)
  const syncImpactData = ref<SyncImpactResponse | null>(null)
  const syncSelectedResourceIds = ref<number[]>([]) // 用户选中的资源 ID

  // 执行进度
  const syncTaskId = ref<string | null>(null) // 当前任务 ID
  const syncStatusUrl = ref<string | null>(null) // 后端返回的状态查询 URL
  const syncTaskStatus = ref<SyncTaskStatusResponse['status']>('pending')
  const syncProcessed = ref(0)
  const syncTotal = ref(0)
  const syncPollingTimer = ref<ReturnType<typeof setInterval> | null>(null)
  const syncPollingFailCount = ref(0) // 连续轮询失败计数

  // 结果
  const syncResult = ref<SyncTaskStatusResponse['result'] | null>(null)
  const syncError = ref<string | null>(null) // 任务失败时的错误信息

  const filteredItems = computed(() => {
    const query = searchQuery.value.trim().toLowerCase()

    return items.value.filter((entry) => {
      if (query.length === 0) {
        return true
      }

      return (
        entry.source.toLowerCase().includes(query) ||
        entry.target.toLowerCase().includes(query) ||
        (entry.notes?.toLowerCase().includes(query) ?? false)
      )
    })
  })

  const entryCount = computed(() => items.value.length)

  /** 同步进度百分比（0-100） */
  const syncProgress = computed(() => {
    if (syncTotal.value === 0) return 0
    return Math.round((syncProcessed.value / syncTotal.value) * 100)
  })

  /** 是否有影响分析数据 */
  const hasSyncImpact = computed(() => Boolean(syncImpactData.value))

  /** 已选中的资源数量 */
  const syncSelectedResourceCount = computed(() => syncSelectedResourceIds.value.length)

  const loadEntries = async (projectId: number): Promise<void> => {
    loading.value = true
    error.value = null

    try {
      const response = await fetchGlossaryEntries(projectId)
      items.value = response.items
    } catch (loadError) {
      error.value =
        loadError instanceof Error ? loadError.message : t('api.errors.fetchGlossaryFailed')
    } finally {
      loading.value = false
    }
  }

  const createEntry = async (
    projectId: number,
    payload: CreateGlossaryEntryPayload,
  ): Promise<GlossaryEntry> => {
    creating.value = true
    createError.value = null

    try {
      const entry = await createGlossaryEntryRequest(projectId, payload)
      items.value = [...items.value, entry]
      return entry
    } catch (submitError) {
      createError.value =
        submitError instanceof Error
          ? submitError.message
          : t('api.errors.createGlossaryEntryFailed')
      throw submitError
    } finally {
      creating.value = false
    }
  }

  const updateEntry = async (
    projectId: number,
    entryId: number,
    payload: UpdateGlossaryEntryPayload,
  ): Promise<UpdateGlossaryEntryResponse> => {
    updating.value = true
    updateError.value = null

    try {
      const response = await updateGlossaryEntryRequest(projectId, entryId, payload)
      items.value = items.value.map((item) => (item.id === response.id ? response : item))
      return response
    } catch (submitError) {
      updateError.value =
        submitError instanceof Error
          ? submitError.message
          : t('api.errors.updateGlossaryEntryFailed')
      throw submitError
    } finally {
      updating.value = false
    }
  }

  const deleteEntry = async (projectId: number, entryId: number): Promise<void> => {
    deletingEntryIds.value = [...deletingEntryIds.value, entryId]
    deleteError.value = null

    try {
      await deleteGlossaryEntryRequest(projectId, entryId)
      items.value = items.value.filter((item) => item.id !== entryId)
    } catch (submitError) {
      deleteError.value =
        submitError instanceof Error
          ? submitError.message
          : t('api.errors.deleteGlossaryEntryFailed')
      throw submitError
    } finally {
      deletingEntryIds.value = deletingEntryIds.value.filter((id) => id !== entryId)
    }
  }

  const importCSV = async (projectId: number, file: File): Promise<GlossaryImportResult> => {
    importing.value = true
    importError.value = null
    importResult.value = null

    try {
      const result = await importGlossaryCSVRequest(projectId, file)
      importResult.value = result
      await loadEntries(projectId)
      return result
    } catch (submitError) {
      importError.value =
        submitError instanceof Error ? submitError.message : t('api.errors.importGlossaryFailed')
      throw submitError
    } finally {
      importing.value = false
    }
  }

  const exportCSV = async (projectId: number): Promise<void> => {
    try {
      const blob = await exportGlossaryCSVRequest(projectId)
      const url = URL.createObjectURL(blob)
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = 'glossary.csv'
      anchor.click()
      URL.revokeObjectURL(url)
    } catch (submitError) {
      const errorMessage =
        submitError instanceof Error ? submitError.message : t('api.errors.exportGlossaryFailed')
      throw new Error(errorMessage)
    }
  }

  // ── 同步辅助函数 ──

  const SYNC_POLL_INTERVAL = 500 // 500ms
  const SYNC_MAX_POLL_FAILURES = 10 // 连续失败上限
  const SYNC_TERMINAL_STATUSES: SyncTaskStatusResponse['status'][] = [
    'completed',
    'failed',
    'cancelled',
  ]

  /**
   * 从 status_url 中解析 projectId。
   * status_url 格式示例: /api/projects/123/sync-tasks/xxx
   */
  const extractProjectIdFromStatusUrl = (statusUrl: string): number | null => {
    const match = statusUrl.match(/\/projects\/(\d+)\//)
    return match ? Number(match[1]) : null
  }

  // ── 同步方法 ──

  /**
   * 打开同步对话框并自动触发影响分析。
   * 由 GlossaryDrawer 保存成功后调用。
   */
  const openSyncDialog = async (
    projectId: number,
    entryId: number,
    source: string,
    oldTarget: string,
    newTarget: string,
  ): Promise<void> => {
    syncDialogVisible.value = true
    syncStep.value = 'impact'
    syncEntryId.value = entryId
    syncSource.value = source
    syncOldTarget.value = oldTarget
    syncNewTarget.value = newTarget
    syncImpactData.value = null
    syncSelectedResourceIds.value = []
    syncResult.value = null
    syncError.value = null
    syncTaskId.value = null
    syncStatusUrl.value = null
    syncPollingFailCount.value = 0

    await loadSyncImpact(projectId)
  }

  const loadSyncImpact = async (projectId: number): Promise<void> => {
    if (!syncEntryId.value) return

    syncImpactLoading.value = true
    syncImpactError.value = null

    try {
      const data = await analyzeGlossarySyncImpact(projectId, syncEntryId.value, {
        old_target: syncOldTarget.value,
        new_target: syncNewTarget.value,
      })
      syncImpactData.value = data
      // 默认全选所有资源
      syncSelectedResourceIds.value = data.resources.map((r) => r.resource_id)
    } catch (err) {
      syncImpactError.value =
        err instanceof Error ? err.message : t('workspace.glossary.sync.impactLoadFailed')
    } finally {
      syncImpactLoading.value = false
    }
  }

  const submitSync = async (projectId: number, mode: 'all' | 'selected'): Promise<void> => {
    if (!syncEntryId.value) return

    syncStep.value = 'executing'
    syncProcessed.value = 0
    syncTotal.value = 0
    syncError.value = null
    syncPollingFailCount.value = 0

    try {
      const payload: SyncExecuteRequest = {
        old_target: syncOldTarget.value,
        new_target: syncNewTarget.value,
        ...(mode === 'selected' && syncSelectedResourceIds.value.length > 0
          ? { resource_ids: syncSelectedResourceIds.value }
          : {}),
      }

      const response = await executeGlossarySync(projectId, syncEntryId.value, payload)
      syncTaskId.value = response.task_id
      syncStatusUrl.value = response.status_url
      syncTaskStatus.value = response.status

      // 开始轮询
      startSyncPolling()
    } catch (err) {
      syncError.value =
        err instanceof Error ? err.message : t('workspace.glossary.sync.executeFailed')
      syncStep.value = 'error'
    }
  }

  const startSyncPolling = (): void => {
    stopSyncPolling()
    syncPollingFailCount.value = 0

    syncPollingTimer.value = setInterval(async () => {
      if (!syncStatusUrl.value || !syncTaskId.value) {
        stopSyncPolling()
        return
      }

      try {
        const projectId = extractProjectIdFromStatusUrl(syncStatusUrl.value)
        if (projectId === null) {
          stopSyncPolling()
          return
        }

        const status = await getGlossarySyncTaskStatus(projectId, syncTaskId.value)
        syncTaskStatus.value = status.status
        syncProcessed.value = status.processed
        syncTotal.value = status.total
        syncPollingFailCount.value = 0

        if (SYNC_TERMINAL_STATUSES.includes(status.status)) {
          stopSyncPolling()

          if (status.status === 'completed') {
            syncResult.value = status.result ?? null
            syncStep.value = 'result'
            const pid = extractProjectIdFromStatusUrl(syncStatusUrl.value)
            if (pid) await loadEntries(pid)
          } else if (status.status === 'cancelled') {
            syncStep.value = 'cancelled'
            const pid = extractProjectIdFromStatusUrl(syncStatusUrl.value)
            if (pid) await loadEntries(pid)
          } else if (status.status === 'failed') {
            syncError.value = status.error ?? t('workspace.glossary.sync.taskFailed')
            syncStep.value = 'error'
          }
        }
      } catch (err) {
        syncPollingFailCount.value++
        console.warn(
          `Sync task polling error (${syncPollingFailCount.value}/${SYNC_MAX_POLL_FAILURES}):`,
          err,
        )

        if (syncPollingFailCount.value >= SYNC_MAX_POLL_FAILURES) {
          stopSyncPolling()
          syncError.value = t('workspace.glossary.sync.networkError')
          syncStep.value = 'error'
        }
      }
    }, SYNC_POLL_INTERVAL)
  }

  const stopSyncPolling = (): void => {
    if (syncPollingTimer.value) {
      clearInterval(syncPollingTimer.value)
      syncPollingTimer.value = null
    }
  }

  const cancelSyncTask = async (projectId: number): Promise<void> => {
    if (!syncTaskId.value) return

    try {
      await cancelGlossarySyncTask(projectId, syncTaskId.value)
      // 取消成功后等待轮询检测到 cancelled 状态
    } catch (err) {
      console.warn('Cancel sync task failed:', err)
    }
  }

  const closeSyncDialog = (): void => {
    stopSyncPolling()
    syncDialogVisible.value = false
    syncStep.value = 'impact'
    syncEntryId.value = null
    syncSource.value = ''
    syncOldTarget.value = ''
    syncNewTarget.value = ''
    syncImpactData.value = null
    syncImpactLoading.value = false
    syncImpactError.value = null
    syncSelectedResourceIds.value = []
    syncTaskId.value = null
    syncStatusUrl.value = null
    syncTaskStatus.value = 'pending'
    syncProcessed.value = 0
    syncTotal.value = 0
    syncPollingFailCount.value = 0
    syncResult.value = null
    syncError.value = null
  }

  const reset = (): void => {
    items.value = []
    loading.value = false
    creating.value = false
    updating.value = false
    deletingEntryIds.value = []
    error.value = null
    createError.value = null
    updateError.value = null
    deleteError.value = null
    importing.value = false
    importError.value = null
    importResult.value = null
    searchQuery.value = ''
    closeSyncDialog()
  }

  return {
    items,
    loading,
    creating,
    updating,
    deletingEntryIds,
    error,
    createError,
    updateError,
    deleteError,
    importing,
    importError,
    importResult,
    searchQuery,
    filteredItems,
    entryCount,
    loadEntries,
    createEntry,
    updateEntry,
    deleteEntry,
    importCSV,
    exportCSV,
    reset,
    // 同步状态
    syncDialogVisible,
    syncStep,
    syncSource,
    syncOldTarget,
    syncNewTarget,
    syncImpactLoading,
    syncImpactError,
    syncImpactData,
    syncSelectedResourceIds,
    syncTaskId,
    syncStatusUrl,
    syncTaskStatus,
    syncProcessed,
    syncTotal,
    syncProgress,
    syncResult,
    syncError,
    hasSyncImpact,
    syncSelectedResourceCount,
    // 同步方法
    openSyncDialog,
    loadSyncImpact,
    submitSync,
    startSyncPolling,
    stopSyncPolling,
    cancelSyncTask,
    closeSyncDialog,
  }
})
