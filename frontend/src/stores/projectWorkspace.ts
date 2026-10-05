import { defineStore } from 'pinia'
import { computed } from 'vue'
import { storeToRefs } from 'pinia'

import { useProjectStore } from './project'
import { useResourceStore } from './resource'
import { useSegmentStore } from './segment'
import { useJobStore } from './job'
import { captureSession, assertSessionCurrent, isSessionCurrent } from '@/api/session-context'
import { storageActionAllowed, type StorageProjectAction } from '@/utils/storage-contract'
import { storageRequestError } from '@/api/storage-errors'
import { useOrganizationsStore } from './organizations'
import { hasWorkspaceDrafts } from '@/utils/workspace-draft-state'

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
} from './resource'

export type {
  SegmentStatusFilter,
  SegmentQualityIssuesFilter,
  SegmentQualitySeverityFilter,
  SegmentQualityCodeFilter,
  SegmentSearchFieldFilter,
} from './segment'
export type { ResourceSegmentGroup } from './segment'
export type { JobStatusFilter } from './job'

export const useProjectWorkspaceStore = defineStore('projectWorkspace', () => {
  const projectStore = useProjectStore()
  const resourceStore = useResourceStore()
  const segmentStore = useSegmentStore()
  const jobStore = useJobStore()

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
    totalSegmentCount,
    totalTranslatedSegments,
    totalApprovedSegments,
  } = storeToRefs(resourceStore)

  // ── 重新导出段落 Store 的响应式状态 ──
  const {
    segments,
    contentWriteRevision,
    segmentsCursor,
    segmentsPrevCursor,
    loadingSegmentsUp,
    segmentsTotal,
    loadingSegments,
    segmentsError,
    editingSegmentIds,
    segmentSearch,
    segmentStatusFilter,
    segmentQualityIssuesFilter,
    segmentQualitySeverityFilter,
    segmentQualityCodeFilter,
    segmentSearchFieldFilter,
    segmentSearchCaseSensitive,
    segmentSearchMatchMode,
    segmentSearchWholeWord,
    lastSearchReplaceOperationId,
    segmentProgressCache,
    // 搜索定位（独立面板）状态
    searchResults,
    searchResultsCursor,
    searchResultsTotal,
    loadingSearchResults,
    searchResultsError,
    searchActiveResultId,
    searchActiveResultField,
    searchJumpSeq,
    jumpingToSegmentCount,
    // EPUB 章节导航状态
    segmentGroups,
    loadingSegmentGroups,
    segmentGroupsError,
    epubActiveGroupKey,
    epubActiveGroupTitle,
    epubSelectedGroupKeys,
    chapterMultiSelect,
    // isEpubResource 由下方跨域计算属性覆盖，不再从 segmentStore 导出
    epubChapterCount,
    isInChapterView,
  } = storeToRefs(segmentStore)

  // ── 重新导出任务 Store 的响应式状态 ──
  const {
    jobs,
    jobsCursor,
    loadingJobs,
    jobsError,
    creatingJob,
    cancellingJobIds,
    retryingJobIds,
    pausingJobIds,
    resumingJobIds,
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
    downloadResource,
    downloadResourceResult,
    toggleResourceSelection,
    setSelectedResourceIds,
    setResourceSelection,
    clearSelectedResources,
  } = resourceStore

  // ── 直接委托的段落方法 ──
  const {
    loadSegments,
    loadSegmentsAround,
    loadMoreSegmentsUp,
    updateSegment,
    setIssueDisposition,
    loadSearchResults,
    jumpToSegment,
    resetSearchResults,
  } = segmentStore

  // ── 直接委托的 EPUB 方法 ──
  const {
    loadSegmentGroups,
    enterChapter,
    exitChapter,
    toggleEpubGroupSelection,
    enterChapterMultiSelect,
    exitChapterMultiSelect,
    selectAllEpubGroups,
    clearEpubGroupSelection,
    refreshChapterGroups,
    mergeSearchReplaceItems,
    resetEpubState,
  } = segmentStore

  // ── 直接委托的任务方法 ──
  const { loadJobs, createJob, cancelJob, retryJob, pauseJob, resumeJob } = jobStore

  // ── 协调跨域操作 ──

  const prepareStorageWrite = async (
    projectId: number,
    action: StorageProjectAction,
  ): Promise<void> => {
    const previous = project.value
    if (
      previous?.id !== projectId ||
      projectError.value ||
      loadingProject.value ||
      !storageActionAllowed(previous, action)
    )
      throw storageRequestError({ status: 409 }, { error_code: 'storage_maintenance' })
    const session = captureSession()
    if (previous.owner_org_id) {
      const organizations = useOrganizationsStore()
      await organizations.refresh()
      assertSessionCurrent(session)
      if (organizations.error || !organizations.canWrite(previous.owner_org_id))
        throw storageRequestError({ status: 403 })
    }
    await loadProject(projectId)
    assertSessionCurrent(session)
    if (
      project.value?.id !== projectId ||
      projectError.value ||
      !storageActionAllowed(project.value, action) ||
      project.value.storage_generation !== previous.storage_generation
    )
      throw storageRequestError({ status: 409 }, { error_code: 'storage_generation_conflict' })
  }

  const uploadResources = async (...args: Parameters<typeof resourceStore.uploadResources>) => {
    await prepareStorageWrite(args[0], 'upload')
    return resourceStore.uploadResources(...args)
  }

  const refreshAfterSourceUpdate = async (
    projectId: number,
    resourceId: number,
    beforeSavedContent: () => Promise<boolean>,
  ): Promise<'refreshed' | 'deferred' | 'failed'> => {
    const session = captureSession()
    const current = () => isSessionCurrent(session) && project.value?.id === projectId
    if (!current()) return 'deferred'
    try {
      if (!(await beforeSavedContent()) || !current()) return 'deferred'
      await Promise.all([loadProject(projectId), loadResourceTree(projectId)])
      if (!current()) return 'deferred'
      if (projectError.value || resourceTreeError.value) return 'failed'
      if (activeResourceId.value !== resourceId) return 'refreshed'
      const revision = contentWriteRevision.value
      const canApply = () =>
        current() &&
        activeResourceId.value === resourceId &&
        contentWriteRevision.value === revision &&
        !hasWorkspaceDrafts(projectId, resourceId)
      if (!canApply()) return 'deferred'
      segmentStore.resetSearchResults()
      segmentStore.lastSearchReplaceOperationId = null
      if (isEpubResource.value) {
        await segmentStore.loadSegmentGroups(projectId, resourceId, canApply)
        if (!canApply()) return 'deferred'
        if (segmentGroupsError.value) return 'failed'
        if (!segmentGroups.value.some((group) => group.group_key === epubActiveGroupKey.value))
          segmentStore.exitChapter()
        const valid = new Set(segmentGroups.value.map((group) => group.group_key))
        segmentStore.epubSelectedGroupKeys = new Set(
          [...epubSelectedGroupKeys.value].filter((key) => valid.has(key)),
        )
      }
      await segmentStore.loadSegments(
        projectId,
        resourceId,
        false,
        epubActiveGroupKey.value ?? undefined,
        canApply,
      )
      if (!canApply()) return 'deferred'
      if (segmentsError.value) return 'failed'
      contentWriteRevision.value++
      return 'refreshed'
    } catch {
      return current() ? 'failed' : 'deferred'
    }
  }

  /** 设置当前激活资源并清空段落 */
  const setActiveResource = (resourceId: number | null): void => {
    resourceStore.setActiveResource(resourceId, () => {
      segmentStore.resetSegments()
      segmentStore.resetEpubState()
    })
  }

  /** 删除资源并清空关联段落 */
  const deleteResource = async (projectId: number, resourceId: number): Promise<void> => {
    await prepareStorageWrite(projectId, 'delete')
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
    prepareStorageWrite,
    refreshAfterSourceUpdate,
    contentWriteRevision,
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
    // 游标
    resourcesCursor,
    segmentsCursor,
    segmentsPrevCursor,
    loadingSegmentsUp,
    segmentsTotal,
    jobsCursor,
    // 加载状态
    loadingProject,
    loadingResources,
    loadingSegments,
    loadingJobs,
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
    pausingJobIds,
    resumingJobIds,
    downloadingKeys,
    // 错误
    projectError,
    resourcesError,
    segmentsError,
    jobsError,
    actionError,
    // 筛选器
    resourceSearch,
    resourceFormatFilter,
    segmentSearch,
    segmentStatusFilter,
    segmentQualityIssuesFilter,
    segmentQualitySeverityFilter,
    segmentQualityCodeFilter,
    segmentSearchFieldFilter,
    segmentSearchCaseSensitive,
    segmentSearchMatchMode,
    segmentSearchWholeWord,
    lastSearchReplaceOperationId,
    jobStatusFilter,
    // 段落进度缓存
    segmentProgressCache,
    // 搜索定位（独立面板）
    searchResults,
    searchResultsCursor,
    searchResultsTotal,
    loadingSearchResults,
    searchResultsError,
    searchActiveResultId,
    searchActiveResultField,
    searchJumpSeq,
    jumpingToSegmentCount,
    // EPUB 章节导航
    segmentGroups,
    loadingSegmentGroups,
    segmentGroupsError,
    epubActiveGroupKey,
    epubActiveGroupTitle,
    epubSelectedGroupKeys,
    chapterMultiSelect,
    isEpubResource,
    epubChapterCount,
    isInChapterView,
    // 计算属性
    availableFormats,
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
    loadSegmentsAround,
    loadMoreSegmentsUp,
    loadJobs,
    loadSearchResults,
    jumpToSegment,
    resetSearchResults,
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
    deleteResource,
    updateSegment,
    setIssueDisposition,
    createJob,
    cancelJob,
    retryJob,
    pauseJob,
    resumeJob,
    downloadResource,
    downloadResourceResult,
    setActiveResource,
    // EPUB
    loadSegmentGroups,
    loadEpubData,
    enterChapter,
    exitChapter,
    toggleEpubGroupSelection,
    enterChapterMultiSelect,
    exitChapterMultiSelect,
    selectAllEpubGroups,
    clearEpubGroupSelection,
    refreshChapterGroups,
    mergeSearchReplaceItems,
    resetEpubState,
    toggleResourceSelection,
    setSelectedResourceIds,
    setResourceSelection,
    clearSelectedResources,
    reset,
  }
})
