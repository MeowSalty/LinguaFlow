import { defineStore } from 'pinia'
import { computed } from 'vue'
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
  SegmentGroup,
} from './resource'

export type { SegmentStatusFilter } from './segment'
export type { ResourceSegmentGroup } from './segment'
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
    totalTranslatedSegments,
    totalApprovedSegments,
    // EPUB 虚拟目录
    epubDirectoryResourceId,
    epubDirectoryResourceName,
    epubDirectoryChapters,
    epubDirectoryLoading,
    isInEpubDirectory,
    epubDirectoryBreadcrumbSuffix,
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
    // EPUB 章节导航状态
    segmentGroups,
    loadingSegmentGroups,
    segmentGroupsError,
    epubActiveGroupKey,
    epubActiveGroupTitle,
    epubSelectedGroupKeys,
    // isEpubResource 由下方跨域计算属性覆盖，不再从 segmentStore 导出
    epubChapterCount,
    isInChapterView,
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

  const runningJobCount = computed(
    () => jobs.value.filter((job) => job.status === 'pending' || job.status === 'running').length,
  )

  const actionError = computed(
    () => resourceStore.actionError ?? segmentStore.actionError ?? jobStore.actionError ?? null,
  )

  /**
   * 当前激活资源是否为 EPUB（基于 resource.format 判断，立即可用）
   *
   * 覆盖 segmentStore 中基于 segmentGroups 数据的 isEpubResource，
   * 避免 resetEpubState() 清空 segmentGroups 后误判为非 EPUB。
   */
  const isEpubResource = computed(() => activeResource.value?.format === 'epub')

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
    downloadTranslatedResource,
    toggleResourceSelection,
    setSelectedResourceIds,
    clearSelectedResources,
    enterEpub,
    exitEpub,
    refreshEpubChapters,
  } = resourceStore

  // ── 直接委托的段落方法 ──
  const { loadSegments, updateSegment } = segmentStore

  // ── 直接委托的 EPUB 方法 ──
  const {
    loadSegmentGroups,
    enterChapter,
    exitChapter,
    toggleEpubGroupSelection,
    refreshChapterGroups,
    resetEpubState,
  } = segmentStore

  // ── 直接委托的任务方法 ──
  const { loadJobs, loadJobDetail, createJob, cancelJob, retryJob } = jobStore

  // ── 协调跨域操作 ──

  /** 设置当前激活资源并清空段落 */
  const setActiveResource = (resourceId: number | null): void => {
    resourceStore.setActiveResource(resourceId, () => {
      segmentStore.resetSegments()
      segmentStore.resetEpubState()
    })
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

  /**
   * 加载 EPUB 资源的章节数据
   */
  const loadEpubData = async (projectId: number, resourceId: number): Promise<void> => {
    await loadSegmentGroups(projectId, resourceId)
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
    resourceFormatFilter,
    segmentSearch,
    segmentStatusFilter,
    jobStatusFilter,
    // 段落进度缓存
    segmentProgressCache,
    // EPUB 章节导航
    segmentGroups,
    loadingSegmentGroups,
    segmentGroupsError,
    epubActiveGroupKey,
    epubActiveGroupTitle,
    epubSelectedGroupKeys,
    isEpubResource,
    epubChapterCount,
    isInChapterView,
    // EPUB 虚拟目录
    epubDirectoryResourceId,
    epubDirectoryResourceName,
    epubDirectoryChapters,
    epubDirectoryLoading,
    isInEpubDirectory,
    epubDirectoryBreadcrumbSuffix,
    // 计算属性
    availableFormats,
    readyResourceCount,
    totalSegmentCount,
    totalTranslatedSegments,
    totalApprovedSegments,
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
    downloadTranslatedResource,
    setActiveResource,
    // EPUB
    loadSegmentGroups,
    loadEpubData,
    enterChapter,
    exitChapter,
    toggleEpubGroupSelection,
    refreshChapterGroups,
    resetEpubState,
    // EPUB 虚拟目录
    enterEpub,
    exitEpub,
    refreshEpubChapters,
    toggleResourceSelection,
    setSelectedResourceIds,
    clearSelectedResources,
    reset,
  }
})
