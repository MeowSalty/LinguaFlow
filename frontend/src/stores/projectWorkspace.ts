import { defineStore } from 'pinia'
import { computed, watch } from 'vue'
import { storeToRefs } from 'pinia'

import { useProjectStore } from './project'
import { useResourceStore } from './resource'
import { useSegmentStore } from './segment'
import { useTranslationJobStore } from './translationJob'

// ── 重新导出所有类型，保持向后兼容 ──
export type {
  BreadcrumbItem,
  DirectoryChild,
  UploadTask,
  PendingUploadStrategy,
  PendingUploadItem,
  IncrementalUploadResult,
  ReplaceUploadResult,
  UploadResultSummary,
  UploadExecutionResult,
  ResourceStatusFilter,
} from './resource'

export type { SegmentStatusFilter } from './segment'
export type { JobStatusFilter } from './translationJob'

export const useProjectWorkspaceStore = defineStore('projectWorkspace', () => {
  const projectStore = useProjectStore()
  const resourceStore = useResourceStore()
  const segmentStore = useSegmentStore()
  const jobStore = useTranslationJobStore()

  // ── 重新导出项目 Store 的响应式状态 ──
  const { project, loadingProject, projectError } = storeToRefs(projectStore)

  // ── 重新导出资源 Store 的响应式状态 ──
  const {
    resourceTree,
    currentPath,
    loadingResourceTree,
    resourceTreeError,
    breadcrumbs,
    currentDirectoryNode,
    currentDirectoryChildren,
    currentDirectoryResources,
    resources,
    selectedResourceIds,
    activeResourceId,
    activeResource,
    selectedResources,
    resourcesCursor,
    loadingResources,
    resourcesError,
    resourceSearch,
    resourceStatusFilter,
    resourceFormatFilter,
    uploadTasks,
    pendingUploadItems,
    lastUploadResult,
    hasActiveUploads,
    replacingResourceIds,
    incrementalUpdatingIds,
    deletingResourceIds,
    downloadingKeys,
    availableFormats,
    readyResourceCount,
    totalSegmentCount,
  } = storeToRefs(resourceStore)

  // ── 重新导出段落 Store 的响应式状态 ──
  const {
    segments,
    segmentsCursor,
    loadingSegments,
    segmentsError,
    editingSegmentIds,
    segmentSearch,
    segmentStatusFilter,
    segmentProgressCache,
    translatedSegmentCount,
  } = storeToRefs(segmentStore)

  // ── 重新导出任务 Store 的响应式状态 ──
  const {
    jobs,
    selectedJob,
    jobsCursor,
    loadingJobs,
    loadingJobDetail,
    jobsError,
    jobDetailError,
    creatingJob,
    cancellingJobIds,
    retryingJobIds,
    jobStatusFilter,
  } = storeToRefs(jobStore)

  // ── 跨域计算属性 ──

  /** 项目级翻译进度百分比 */
  const translationProgress = computed(() => {
    if (totalSegmentCount.value === 0) return 0
    return Math.round((translatedSegmentCount.value / totalSegmentCount.value) * 100)
  })

  const runningJobCount = computed(
    () => jobs.value.filter((job) => job.status === 'pending' || job.status === 'running').length,
  )

  const actionError = computed(
    () => resourceStore.actionError ?? segmentStore.actionError ?? jobStore.actionError ?? null,
  )

  // ── 监听目录变化，自动预加载当前目录下资源的段落进度 ──
  watch(currentPath, () => {
    if (projectStore._currentProjectId) {
      const resourceIds = currentDirectoryResources.value
        .filter((r) => r.total_segments > 0)
        .map((r) => r.id)
      void segmentStore.preloadDirectoryProgress(projectStore._currentProjectId, resourceIds)
    }
  })

  // ── 直接委托的项目方法 ──
  const { loadProject } = projectStore

  // ── 直接委托的资源方法 ──
  const {
    loadResourceTree,
    navigateTo,
    navigateUp,
    syncResourcesFromTree,
    loadResources,
    addUploadTask,
    updateUploadTaskProgress,
    updateUploadTaskStage,
    removeUploadTask,
    clearCompletedUploadTasks,
    clearAllUploadTasks,
    precheckUploadResources,
    setPendingUploadItems,
    clearPendingUploadItems,
    setPendingUploadItemSelected,
    setPendingUploadItemStrategy,
    setAllCreatablePendingUploadItemsSelected,
    mergeLastUploadResult,
    uploadResources,
    downloadResource,
    downloadJobResult,
  } = resourceStore

  // ── 直接委托的段落方法 ──
  const { loadSegments, updateSegment, getResourceProgress } = segmentStore

  // ── 直接委托的任务方法 ──
  const { loadJobs, loadJobDetail, createJob, cancelJob, retryJob } = jobStore

  // ── 协调跨域操作 ──

  /** 设置当前激活资源并清空段落 */
  const setActiveResource = (resourceId: number | null): void => {
    resourceStore.setActiveResource(resourceId, segmentStore.resetSegments)
  }

  /** 替换资源并清空关联段落 */
  const replaceResource = async (
    projectId: number,
    resourceId: number,
    file: File,
  ): Promise<void> => {
    return resourceStore.replaceResource(projectId, resourceId, file, segmentStore.resetSegments)
  }

  /** 增量更新资源并清空关联段落 */
  const incrementalUpdateResource = async (projectId: number, resourceId: number, file: File) => {
    return resourceStore.incrementalUpdateResource(
      projectId,
      resourceId,
      file,
      segmentStore.resetSegments,
    )
  }

  /** 删除资源并清空关联段落 */
  const deleteResource = async (projectId: number, resourceId: number): Promise<void> => {
    return resourceStore.deleteResource(projectId, resourceId, segmentStore.resetSegments)
  }

  /** 为当前目录下的资源预加载段落进度（协调资源与段落 Store） */
  const preloadDirectoryProgress = async (projectId: number): Promise<void> => {
    const resourceIds = currentDirectoryResources.value
      .filter((r) => r.total_segments > 0)
      .map((r) => r.id)
    return segmentStore.preloadDirectoryProgress(projectId, resourceIds)
  }

  // ── 重置所有子 Store ──
  const reset = (): void => {
    projectStore.reset()
    resourceStore.reset()
    segmentStore.reset()
    jobStore.reset()
  }

  return {
    // 项目
    project,
    // 资源树
    resourceTree,
    currentPath,
    loadingResourceTree,
    resourceTreeError,
    breadcrumbs,
    currentDirectoryNode,
    currentDirectoryChildren,
    currentDirectoryResources,
    // 资源列表
    resources,
    selectedResourceIds,
    activeResourceId,
    activeResource,
    selectedResources,
    // 段落 & 任务
    segments,
    jobs,
    selectedJob,
    // 游标
    resourcesCursor,
    segmentsCursor,
    jobsCursor,
    // 加载状态
    loadingProject,
    loadingResources,
    loadingSegments,
    loadingJobs,
    loadingJobDetail,
    uploadTasks,
    pendingUploadItems,
    lastUploadResult,
    hasActiveUploads,
    replacingResourceIds,
    incrementalUpdatingIds,
    deletingResourceIds,
    editingSegmentIds,
    creatingJob,
    cancellingJobIds,
    retryingJobIds,
    downloadingKeys,
    // 错误
    projectError,
    resourcesError,
    segmentsError,
    jobsError,
    jobDetailError,
    actionError,
    // 筛选器
    resourceSearch,
    resourceStatusFilter,
    resourceFormatFilter,
    segmentSearch,
    segmentStatusFilter,
    jobStatusFilter,
    // 段落进度缓存
    segmentProgressCache,
    getResourceProgress,
    translatedSegmentCount,
    translationProgress,
    // 计算属性
    availableFormats,
    readyResourceCount,
    totalSegmentCount,
    runningJobCount,
    // Actions
    loadProject,
    loadResourceTree,
    navigateTo,
    navigateUp,
    syncResourcesFromTree,
    loadResources,
    loadSegments,
    loadJobs,
    loadJobDetail,
    addUploadTask,
    updateUploadTaskProgress,
    updateUploadTaskStage,
    removeUploadTask,
    clearCompletedUploadTasks,
    clearAllUploadTasks,
    precheckUploadResources,
    setPendingUploadItems,
    clearPendingUploadItems,
    setPendingUploadItemSelected,
    setPendingUploadItemStrategy,
    setAllCreatablePendingUploadItemsSelected,
    mergeLastUploadResult,
    uploadResources,
    replaceResource,
    incrementalUpdateResource,
    deleteResource,
    updateSegment,
    createJob,
    cancelJob,
    retryJob,
    downloadResource,
    downloadJobResult,
    setActiveResource,
    preloadDirectoryProgress,
    reset,
  }
})
