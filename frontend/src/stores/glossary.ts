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

/** 批量同步队列项（精简术语等多条 target 变更时使用） */
export type GlossarySyncQueueItem = {
  entryId: number
  source: string
  oldTarget: string
  newTarget: string
}

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

  // 批量同步队列
  const syncQueue = ref<GlossarySyncQueueItem[]>([])
  const syncQueueTotal = ref(0)
  const syncQueueCurrent = ref(0) // 当前处理到第几项（1-based，用于展示）
  const syncQueueSyncedAny = ref(false) // 队列中是否至少有一次成功同步
  const syncAdvancing = ref(false) // 队列推进中（防连点重入）
  let syncImpactGeneration = 0 // impact 请求代数，丢弃过期响应

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

  /** 重置当前条目的同步执行态（不影响队列元数据） */
  const resetCurrentSyncState = (
    entryId: number,
    source: string,
    oldTarget: string,
    newTarget: string,
  ): void => {
    syncImpactGeneration += 1
    syncStep.value = 'impact'
    syncEntryId.value = entryId
    syncSource.value = source
    syncOldTarget.value = oldTarget
    syncNewTarget.value = newTarget
    syncImpactData.value = null
    syncImpactLoading.value = false
    syncImpactError.value = null
    syncSelectedResourceIds.value = []
    syncResult.value = null
    syncError.value = null
    syncTaskId.value = null
    syncStatusUrl.value = null
    syncTaskStatus.value = 'pending'
    syncProcessed.value = 0
    syncTotal.value = 0
    syncPollingFailCount.value = 0
  }

  /**
   * 打开同步对话框并自动触发影响分析。
   * 由 GlossaryDrawer 保存成功后调用（单条，会清空队列）。
   */
  const openSyncDialog = async (
    projectId: number,
    entryId: number,
    source: string,
    oldTarget: string,
    newTarget: string,
  ): Promise<void> => {
    stopSyncPolling()
    syncAdvancing.value = false
    syncQueue.value = []
    syncQueueTotal.value = 0
    syncQueueCurrent.value = 0
    syncQueueSyncedAny.value = false

    syncDialogVisible.value = true
    resetCurrentSyncState(entryId, source, oldTarget, newTarget)
    await loadSyncImpact(projectId)
  }

  /**
   * 打开批量同步队列：按序对多条术语译文变更做影响分析 / 同步。
   * 精简术语 apply 后若存在 target_changed 时调用。
   */
  const openSyncQueue = async (
    projectId: number,
    items: GlossarySyncQueueItem[],
  ): Promise<void> => {
    if (items.length === 0) return

    stopSyncPolling()
    syncAdvancing.value = false
    syncQueue.value = [...items]
    syncQueueTotal.value = items.length
    syncQueueCurrent.value = 0
    syncQueueSyncedAny.value = false
    syncDialogVisible.value = true

    await openNextSyncFromQueue(projectId)
  }

  const openNextSyncFromQueue = async (projectId: number): Promise<boolean> => {
    const next = syncQueue.value.shift()
    if (!next) return false

    stopSyncPolling()
    syncQueueCurrent.value = syncQueueTotal.value - syncQueue.value.length
    resetCurrentSyncState(next.entryId, next.source, next.oldTarget, next.newTarget)
    syncDialogVisible.value = true
    await loadSyncImpact(projectId)
    return true
  }

  /**
   * 结束当前术语的同步交互，若队列中还有下一项则自动打开。
   * 队列耗尽时仅关闭对话框，完整清理由对话框 watch 负责（以便读取同步成功标记）。
   * @returns advanced — 已打开下一项；done — 队列结束；busy — 正在推进中已忽略
   */
  const finishCurrentSyncAndAdvance = async (
    projectId: number,
  ): Promise<'advanced' | 'done' | 'busy'> => {
    if (syncAdvancing.value || syncImpactLoading.value) {
      return 'busy'
    }

    if (syncStep.value === 'result') {
      syncQueueSyncedAny.value = true
    }

    stopSyncPolling()
    syncAdvancing.value = true

    try {
      if (syncQueue.value.length > 0) {
        await openNextSyncFromQueue(projectId)
        return 'advanced'
      }

      // 仅隐藏对话框，保留 syncStep / syncQueueSyncedAny 供关闭监听读取
      syncDialogVisible.value = false
      return 'done'
    } finally {
      syncAdvancing.value = false
    }
  }

  const loadSyncImpact = async (projectId: number): Promise<void> => {
    if (!syncEntryId.value) return

    const entryId = syncEntryId.value
    const oldTarget = syncOldTarget.value
    const newTarget = syncNewTarget.value
    const generation = ++syncImpactGeneration

    syncImpactLoading.value = true
    syncImpactError.value = null

    try {
      const data = await analyzeGlossarySyncImpact(projectId, entryId, {
        old_target: oldTarget,
        new_target: newTarget,
      })
      if (generation !== syncImpactGeneration || syncEntryId.value !== entryId) {
        return
      }
      syncImpactData.value = data
      // 默认全选所有资源
      syncSelectedResourceIds.value = data.resources.map((r) => r.resource_id)
    } catch (err) {
      if (generation !== syncImpactGeneration || syncEntryId.value !== entryId) {
        return
      }
      syncImpactError.value =
        err instanceof Error ? err.message : t('workspace.glossary.sync.impactLoadFailed')
    } finally {
      if (generation === syncImpactGeneration) {
        syncImpactLoading.value = false
      }
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
    syncQueue.value = []
    syncQueueTotal.value = 0
    syncQueueCurrent.value = 0
    syncQueueSyncedAny.value = false
    syncAdvancing.value = false
    syncImpactGeneration += 1
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
    syncQueueTotal,
    syncQueueCurrent,
    syncQueueSyncedAny,
    syncAdvancing,
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
    openSyncQueue,
    finishCurrentSyncAndAdvance,
    loadSyncImpact,
    submitSync,
    startSyncPolling,
    stopSyncPolling,
    cancelSyncTask,
    closeSyncDialog,
  }
})
