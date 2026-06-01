import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import {
  cancelTranslationJob as cancelTranslationJobRequest,
  createTranslationJob as createTranslationJobRequest,
  deleteProjectResource as deleteProjectResourceRequest,
  downloadProjectResource as downloadProjectResourceRequest,
  downloadTranslationJobResult as downloadTranslationJobResultRequest,
  type ApiSchemas,
  type DownloadFileResult,
  fetchProject,
  fetchProjectResources,
  fetchProjectResourceTree,
  fetchResourceSegments,
  fetchTranslationJob,
  fetchTranslationJobs,
  incrementalUpdateResource as incrementalUpdateResourceRequest,
  precheckProjectResources as precheckProjectResourcesRequest,
  replaceProjectResource as replaceProjectResourceRequest,
  retryTranslationJob as retryTranslationJobRequest,
  updateResourceSegment as updateResourceSegmentRequest,
  uploadProjectResourcesWithProgress,
} from '@/api/client'
import { t } from '@/i18n'

type Project = ApiSchemas['Project']
type Resource = ApiSchemas['Resource']
type ResourceTreeNode = ApiSchemas['ResourceTreeNode']
type Segment = ApiSchemas['Segment']
type TranslationJob = ApiSchemas['TranslationJob']
type CreateTranslationJobPayload = ApiSchemas['CreateTranslationJobRequest']
type SegmentUpdatePayload = ApiSchemas['ResourceSegmentUpdateRequest']
type ResourcePrecheckFileResult = ApiSchemas['ResourcePrecheckFileResult']
type ResourceUploadBatchResponse = ApiSchemas['ResourceUploadBatchResponse']

export interface BreadcrumbItem {
  label: string
  path: string
}

export interface DirectoryChild {
  type: 'directory' | 'resource'
  name: string
  path: string
  resource?: Resource
  childCount?: number
}

export interface UploadTask {
  id: string
  fileName: string
  stage: 'prechecking' | 'uploading' | 'processing' | 'complete' | 'partial' | 'error'
  progress: number
  errorMessage?: string
  summary?: UploadResultSummary
}

export type PendingUploadStrategy = 'create' | 'incremental_update' | 'replace' | 'skip'

export interface PendingUploadItem {
  id: string
  file: File
  path: string
  precheck: ResourcePrecheckFileResult
  selected: boolean
  strategy: PendingUploadStrategy
}

export interface IncrementalUploadResult {
  item: PendingUploadItem
  result?: ApiSchemas['IncrementalUpdateResponse']
  error?: string
}

export interface ReplaceUploadResult {
  item: PendingUploadItem
  result?: boolean
  error?: string
}

export interface UploadResultSummary {
  created: number
  incrementallyUpdated: number
  replaced: number
  conflicts: number
  failed: number
  skipped: number
  total: number
}

export interface UploadExecutionResult {
  response: ResourceUploadBatchResponse
  skippedItems: PendingUploadItem[]
  incrementalResults: IncrementalUploadResult[]
  replaceResults: ReplaceUploadResult[]
  summary: UploadResultSummary
}

export type ResourceStatusFilter = Resource['status'] | 'all'
export type SegmentStatusFilter = 'pending' | 'translated' | 'reviewed' | 'rejected' | 'all'
export type JobStatusFilter = TranslationJob['status'] | 'all'

const getErrorMessage = (error: unknown, fallback: string): string =>
  error instanceof Error ? error.message : fallback

const upsertById = <T extends { id: number }>(items: T[], item: T): T[] => [
  item,
  ...items.filter((current) => current.id !== item.id),
]

/**
 * 从资源树中定位指定路径的目录节点。
 * path 为空字符串时返回根节点。
 */
const findNodeByPath = (root: ResourceTreeNode, path: string): ResourceTreeNode | null => {
  if (!path) {
    return root
  }

  const parts = path.split('/')
  let node = root

  for (const part of parts) {
    const child = node.children?.find((c) => c.name === part && c.type === 'directory')
    if (!child) {
      return null
    }
    node = child
  }

  return node
}

const normalizeUploadPath = (path: string): string =>
  path.replaceAll('\\\\', '/').replace(/^\/+/, '').replace(/\/+/g, '/')

const buildUploadSummary = (
  response: ResourceUploadBatchResponse,
  skippedItems: PendingUploadItem[] = [],
  incrementalResults: IncrementalUploadResult[] = [],
  replaceResults: ReplaceUploadResult[] = [],
): UploadResultSummary => ({
  created: response.items.filter((item) => item.action === 'created').length,
  incrementallyUpdated: incrementalResults.filter((item) => item.result && !item.error).length,
  replaced: replaceResults.filter((item) => item.result && !item.error).length,
  conflicts: response.items.filter((item) => item.action === 'conflict').length,
  failed:
    response.items.filter((item) => item.action === 'failed').length +
    incrementalResults.filter((item) => item.error).length +
    replaceResults.filter((item) => item.error).length,
  skipped: skippedItems.length,
  total:
    response.items.length + skippedItems.length + incrementalResults.length + replaceResults.length,
})

export const useProjectWorkspaceStore = defineStore('projectWorkspace', () => {
  // ── 项目 ──
  const project = ref<Project | null>(null)

  // ── 资源目录树 ──
  const resourceTree = ref<ResourceTreeNode | null>(null)
  const currentPath = ref('')
  const loadingResourceTree = ref(false)
  const resourceTreeError = ref<string | null>(null)

  // ── 资源列表（用于段落 Tab 筛选器和兼容旧逻辑） ──
  const resources = ref<Resource[]>([])
  const selectedResourceIds = ref<number[]>([])
  const activeResourceId = ref<number | null>(null)
  const segments = ref<Segment[]>([])
  const jobs = ref<TranslationJob[]>([])
  const selectedJob = ref<TranslationJob | null>(null)

  const resourcesCursor = ref<string | null>(null)
  const segmentsCursor = ref<string | null>(null)
  const jobsCursor = ref<string | null>(null)

  // ── 加载状态 ──
  const loadingProject = ref(false)
  const loadingResources = ref(false)
  const loadingSegments = ref(false)
  const loadingJobs = ref(false)
  const loadingJobDetail = ref(false)
  const uploadTasks = ref<UploadTask[]>([])
  const pendingUploadItems = ref<PendingUploadItem[]>([])
  const lastUploadResult = ref<UploadExecutionResult | null>(null)
  const replacingResourceIds = ref<number[]>([])
  const incrementalUpdatingIds = ref<number[]>([])
  const deletingResourceIds = ref<number[]>([])
  const editingSegmentIds = ref<number[]>([])
  const creatingJob = ref(false)
  const cancellingJobIds = ref<number[]>([])
  const retryingJobIds = ref<number[]>([])
  const downloadingKeys = ref<string[]>([])

  // ── 错误状态 ──
  const projectError = ref<string | null>(null)
  const resourcesError = ref<string | null>(null)
  const segmentsError = ref<string | null>(null)
  const jobsError = ref<string | null>(null)
  const jobDetailError = ref<string | null>(null)
  const actionError = ref<string | null>(null)

  // ── 筛选器 ──
  const resourceSearch = ref('')
  const resourceStatusFilter = ref<ResourceStatusFilter>('all')
  const resourceFormatFilter = ref<string>('all')
  const segmentSearch = ref('')
  const segmentStatusFilter = ref<SegmentStatusFilter>('all')
  const jobStatusFilter = ref<JobStatusFilter>('all')

  // ── 计算属性：资源树导航 ──

  /** 面包屑路径列表 */
  const breadcrumbs = computed<BreadcrumbItem[]>(() => {
    if (!currentPath.value) {
      return []
    }

    const parts = currentPath.value.split('/')
    return parts.map((part, index) => ({
      label: part,
      path: parts.slice(0, index + 1).join('/'),
    }))
  })

  /** 当前目录的树节点 */
  const currentDirectoryNode = computed<ResourceTreeNode | null>(() => {
    if (!resourceTree.value) {
      return null
    }

    return findNodeByPath(resourceTree.value, currentPath.value)
  })

  /** 当前目录的子项列表（目录在前，资源在后） */
  const currentDirectoryChildren = computed<DirectoryChild[]>(() => {
    const node = currentDirectoryNode.value
    if (!node?.children) {
      return []
    }

    const directories: DirectoryChild[] = node.children
      .filter((c) => c.type === 'directory')
      .sort((a, b) => a.name.localeCompare(b.name))
      .map((c) => ({
        type: 'directory' as const,
        name: c.name,
        path: c.path,
        childCount: c.children?.length ?? 0,
      }))

    const resourceItems: DirectoryChild[] = node.children
      .filter((c) => c.type === 'resource' && c.resource)
      .sort((a, b) => a.name.localeCompare(b.name))
      .map((c) => ({
        type: 'resource' as const,
        name: c.name,
        path: c.path,
        resource: c.resource,
      }))

    return [...directories, ...resourceItems]
  })

  /** 当前目录中的资源列表（用于选择器等场景） */
  const currentDirectoryResources = computed<Resource[]>(() =>
    currentDirectoryChildren.value
      .filter((child) => child.type === 'resource' && child.resource)
      .map((child) => child.resource!),
  )

  // ── 计算属性：资源统计 ──

  const activeResource = computed<Resource | null>(
    () => resources.value.find((resource) => resource.id === activeResourceId.value) ?? null,
  )
  const selectedResources = computed<Resource[]>(() =>
    resources.value.filter((resource) => selectedResourceIds.value.includes(resource.id)),
  )
  const availableFormats = computed<string[]>(() =>
    [...new Set(resources.value.map((resource) => resource.format).filter(Boolean))].sort(),
  )
  const readyResourceCount = computed(
    () => resources.value.filter((resource) => resource.status === 'ready').length,
  )
  const totalSegmentCount = computed(() =>
    resources.value.reduce((total, resource) => total + resource.total_segments, 0),
  )
  const runningJobCount = computed(
    () => jobs.value.filter((job) => job.status === 'pending' || job.status === 'running').length,
  )
  const hasActiveUploads = computed(() =>
    uploadTasks.value.some((task) =>
      ['prechecking', 'uploading', 'processing'].includes(task.stage),
    ),
  )

  // ── Actions：项目 ──

  const loadProject = async (projectId: number): Promise<void> => {
    loadingProject.value = true
    projectError.value = null

    try {
      project.value = await fetchProject(projectId)
    } catch (error) {
      projectError.value = getErrorMessage(error, t('api.errors.fetchProjectFailed'))
    } finally {
      loadingProject.value = false
    }
  }

  // ── Actions：资源树 ──

  const loadResourceTree = async (projectId: number): Promise<void> => {
    loadingResourceTree.value = true
    resourceTreeError.value = null

    try {
      const response = await fetchProjectResourceTree(projectId)
      resourceTree.value = response.root
    } catch (error) {
      resourceTreeError.value = getErrorMessage(error, t('api.errors.fetchResourceTreeFailed'))
    } finally {
      loadingResourceTree.value = false
    }
  }

  /** 导航到指定目录路径 */
  const navigateTo = (path: string): void => {
    currentPath.value = path
  }

  /** 返回上级目录 */
  const navigateUp = (): void => {
    const parts = currentPath.value.split('/')
    parts.pop()
    currentPath.value = parts.join('/')
  }

  /** 刷新资源树后同步更新扁平资源列表 */
  const syncResourcesFromTree = (): void => {
    if (!resourceTree.value) {
      resources.value = []
      return
    }

    const flat: Resource[] = []

    const walk = (node: ResourceTreeNode): void => {
      if (node.type === 'resource' && node.resource) {
        flat.push(node.resource)
      }
      if (node.children) {
        for (const child of node.children) {
          walk(child)
        }
      }
    }

    walk(resourceTree.value)
    resources.value = flat

    // 同步选中和激活状态
    selectedResourceIds.value = selectedResourceIds.value.filter((id) =>
      resources.value.some((resource) => resource.id === id),
    )
    if (
      activeResourceId.value &&
      !resources.value.some((item) => item.id === activeResourceId.value)
    ) {
      activeResourceId.value = resources.value[0]?.id ?? null
    }
  }

  // ── Actions：资源列表（保留用于段落 Tab 和筛选） ──

  const loadResources = async (projectId: number, append = false): Promise<void> => {
    loadingResources.value = true
    resourcesError.value = null

    try {
      const response = await fetchProjectResources(projectId, {
        status: resourceStatusFilter.value === 'all' ? undefined : resourceStatusFilter.value,
        format: resourceFormatFilter.value === 'all' ? undefined : resourceFormatFilter.value,
        search: resourceSearch.value.trim() || undefined,
        cursor: append ? (resourcesCursor.value ?? undefined) : undefined,
        limit: 50,
      })
      resources.value = append ? [...resources.value, ...response.items] : response.items
      resourcesCursor.value = null

      if (!activeResourceId.value && resources.value[0]) {
        activeResourceId.value = resources.value[0].id
      }

      selectedResourceIds.value = selectedResourceIds.value.filter((id) =>
        resources.value.some((resource) => resource.id === id),
      )
      if (
        activeResourceId.value &&
        !resources.value.some((item) => item.id === activeResourceId.value)
      ) {
        activeResourceId.value = resources.value[0]?.id ?? null
      }
    } catch (error) {
      resourcesError.value = getErrorMessage(error, t('api.errors.fetchResourcesFailed'))
    } finally {
      loadingResources.value = false
    }
  }

  // ── Actions：段落 ──

  const loadSegments = async (
    projectId: number,
    resourceId: number,
    append = false,
  ): Promise<void> => {
    loadingSegments.value = true
    segmentsError.value = null

    try {
      const response = await fetchResourceSegments(projectId, resourceId, {
        status: segmentStatusFilter.value === 'all' ? undefined : segmentStatusFilter.value,
        search: segmentSearch.value.trim() || undefined,
        cursor: append ? (segmentsCursor.value ?? undefined) : undefined,
        limit: 50,
      })
      segments.value = append ? [...segments.value, ...response.items] : response.items
      segmentsCursor.value = response.next_cursor ?? null
    } catch (error) {
      segmentsError.value = getErrorMessage(error, t('api.errors.fetchSegmentsFailed'))
    } finally {
      loadingSegments.value = false
    }
  }

  // ── Actions：翻译任务 ──

  const loadJobs = async (projectId: number, append = false): Promise<void> => {
    loadingJobs.value = true
    jobsError.value = null

    try {
      const response = await fetchTranslationJobs(projectId, {
        status: jobStatusFilter.value === 'all' ? undefined : jobStatusFilter.value,
        cursor: append ? (jobsCursor.value ?? undefined) : undefined,
        limit: 50,
      })
      jobs.value = append ? [...jobs.value, ...response.items] : response.items
      jobsCursor.value = response.next_cursor ?? null
    } catch (error) {
      jobsError.value = getErrorMessage(error, t('api.errors.fetchTranslationJobsFailed'))
    } finally {
      loadingJobs.value = false
    }
  }

  const loadJobDetail = async (translationJobId: number): Promise<TranslationJob> => {
    loadingJobDetail.value = true
    jobDetailError.value = null

    try {
      const job = await fetchTranslationJob(translationJobId)
      selectedJob.value = job
      jobs.value = jobs.value.map((item) => (item.id === job.id ? job : item))
      return job
    } catch (error) {
      jobDetailError.value = getErrorMessage(error, t('api.errors.fetchTranslationJobFailed'))
      throw error
    } finally {
      loadingJobDetail.value = false
    }
  }

  // ── Actions：上传 ──

  const addUploadTask = (fileName: string): string => {
    const id = crypto.randomUUID()
    uploadTasks.value = [...uploadTasks.value, { id, fileName, stage: 'uploading', progress: 0 }]
    return id
  }

  const updateUploadTaskProgress = (taskId: string, progress: number): void => {
    uploadTasks.value = uploadTasks.value.map((task) =>
      task.id === taskId ? { ...task, progress } : task,
    )
  }

  const updateUploadTaskStage = (
    taskId: string,
    stage: UploadTask['stage'],
    errorMessage?: string,
    summary?: UploadResultSummary,
  ): void => {
    uploadTasks.value = uploadTasks.value.map((task) =>
      task.id === taskId ? { ...task, stage, errorMessage, summary } : task,
    )
  }

  const removeUploadTask = (taskId: string): void => {
    uploadTasks.value = uploadTasks.value.filter((task) => task.id !== taskId)
  }

  const clearCompletedUploadTasks = (): void => {
    uploadTasks.value = uploadTasks.value.filter((task) => task.stage !== 'complete')
  }

  const clearAllUploadTasks = (): void => {
    uploadTasks.value = []
  }

  const precheckUploadResources = async (
    projectId: number,
    files: File[],
    paths?: string[],
  ): Promise<PendingUploadItem[]> => {
    const normalizedPaths = files.map((file, index) =>
      normalizeUploadPath(paths?.[index] ?? file.name),
    )
    const response = await precheckProjectResourcesRequest(projectId, normalizedPaths)

    return files.map((file, index) => {
      const path = normalizedPaths[index] ?? file.name
      const precheck = response.items[index] ?? {
        path,
        action: 'create' as const,
      }

      return {
        id: crypto.randomUUID(),
        file,
        path,
        precheck,
        selected: precheck.action !== 'duplicate',
        strategy:
          precheck.action === 'create'
            ? 'create'
            : precheck.action === 'conflict'
              ? 'incremental_update'
              : 'skip',
      }
    })
  }

  const setPendingUploadItems = (items: PendingUploadItem[]): void => {
    pendingUploadItems.value = items
  }

  const clearPendingUploadItems = (): void => {
    pendingUploadItems.value = []
  }

  const setPendingUploadItemSelected = (itemId: string, selected: boolean): void => {
    pendingUploadItems.value = pendingUploadItems.value.map((item) =>
      item.id === itemId ? { ...item, selected, strategy: selected ? 'create' : 'skip' } : item,
    )
  }

  const setPendingUploadItemStrategy = (itemId: string, strategy: PendingUploadStrategy): void => {
    pendingUploadItems.value = pendingUploadItems.value.map((item) =>
      item.id === itemId ? { ...item, strategy, selected: strategy !== 'skip' } : item,
    )
  }

  const setAllCreatablePendingUploadItemsSelected = (selected: boolean): void => {
    pendingUploadItems.value = pendingUploadItems.value.map((item) =>
      item.precheck.action === 'create'
        ? { ...item, selected, strategy: selected ? 'create' : 'skip' }
        : item,
    )
  }

  const mergeLastUploadResult = (
    incrementalResults: IncrementalUploadResult[],
    replaceResults: ReplaceUploadResult[] = [],
  ): UploadExecutionResult => {
    const baseResult = lastUploadResult.value ?? {
      response: { items: [] },
      skippedItems: [],
      incrementalResults: [],
      replaceResults: [],
      summary: buildUploadSummary({ items: [] }),
    }
    const mergedIncrementalResults = [...baseResult.incrementalResults, ...incrementalResults]
    const mergedReplaceResults = [...baseResult.replaceResults, ...replaceResults]
    const summary = buildUploadSummary(
      baseResult.response,
      baseResult.skippedItems,
      mergedIncrementalResults,
      mergedReplaceResults,
    )
    const result = {
      ...baseResult,
      incrementalResults: mergedIncrementalResults,
      replaceResults: mergedReplaceResults,
      summary,
    }
    lastUploadResult.value = result
    return result
  }

  const uploadResources = async (
    projectId: number,
    files: File[],
    paths?: string[],
    taskId?: string,
    skippedItems: PendingUploadItem[] = [],
  ): Promise<UploadExecutionResult> => {
    const emptyResponse: ResourceUploadBatchResponse = { items: [] }
    if (files.length === 0) {
      const summary = buildUploadSummary(emptyResponse, skippedItems)
      const result = {
        response: emptyResponse,
        skippedItems,
        incrementalResults: [],
        replaceResults: [],
        summary,
      }
      lastUploadResult.value = result
      return result
    }

    actionError.value = null

    try {
      const response = await uploadProjectResourcesWithProgress(projectId, files, paths, {
        onProgress: (percent) => {
          if (taskId) {
            updateUploadTaskProgress(taskId, percent)
          }
        },
        onServerProcessing: () => {
          if (taskId) {
            updateUploadTaskStage(taskId, 'processing')
          }
        },
      })
      const createdResources = response.items
        .filter((item) => item.action === 'created' && item.resource)
        .map((item) => item.resource!)
      resources.value = [...createdResources, ...resources.value]
      if (!activeResourceId.value && createdResources[0]) {
        activeResourceId.value = createdResources[0].id
      }
      const summary = buildUploadSummary(response, skippedItems)
      const result = { response, skippedItems, incrementalResults: [], replaceResults: [], summary }
      lastUploadResult.value = result
      if (taskId) {
        updateUploadTaskStage(
          taskId,
          summary.failed > 0 || summary.conflicts > 0 || summary.skipped > 0
            ? 'partial'
            : 'complete',
          undefined,
          summary,
        )
      }
      return result
    } catch (error) {
      const message = getErrorMessage(error, t('api.errors.uploadResourcesFailed'))
      actionError.value = message
      if (taskId) {
        updateUploadTaskStage(taskId, 'error', message)
      }
      throw error
    }
  }

  // ── Actions：资源操作 ──

  const replaceResource = async (
    projectId: number,
    resourceId: number,
    file: File,
  ): Promise<void> => {
    replacingResourceIds.value = [...replacingResourceIds.value, resourceId]
    actionError.value = null

    try {
      const resource = await replaceProjectResourceRequest(projectId, resourceId, file)
      resources.value = resources.value.map((item) => (item.id === resource.id ? resource : item))
      if (activeResourceId.value === resourceId) {
        segments.value = []
        segmentsCursor.value = null
      }
    } catch (error) {
      actionError.value = getErrorMessage(error, t('api.errors.replaceResourceFailed'))
      throw error
    } finally {
      replacingResourceIds.value = replacingResourceIds.value.filter((id) => id !== resourceId)
    }
  }

  const incrementalUpdateResource = async (
    projectId: number,
    resourceId: number,
    file: File,
  ): Promise<ApiSchemas['IncrementalUpdateResponse']> => {
    incrementalUpdatingIds.value = [...incrementalUpdatingIds.value, resourceId]
    actionError.value = null

    try {
      const result = await incrementalUpdateResourceRequest(projectId, resourceId, file)
      resources.value = resources.value.map((item) =>
        item.id === result.resource.id ? result.resource : item,
      )
      if (activeResourceId.value === resourceId) {
        segments.value = []
        segmentsCursor.value = null
      }
      return result
    } catch (error) {
      actionError.value = getErrorMessage(error, t('api.errors.incrementalUpdateFailed'))
      throw error
    } finally {
      incrementalUpdatingIds.value = incrementalUpdatingIds.value.filter((id) => id !== resourceId)
    }
  }

  const deleteResource = async (projectId: number, resourceId: number): Promise<void> => {
    deletingResourceIds.value = [...deletingResourceIds.value, resourceId]
    actionError.value = null

    try {
      await deleteProjectResourceRequest(projectId, resourceId)
      resources.value = resources.value.filter((resource) => resource.id !== resourceId)
      selectedResourceIds.value = selectedResourceIds.value.filter((id) => id !== resourceId)
      if (activeResourceId.value === resourceId) {
        activeResourceId.value = resources.value[0]?.id ?? null
        segments.value = []
        segmentsCursor.value = null
      }
    } catch (error) {
      actionError.value = getErrorMessage(error, t('api.errors.deleteResourceFailed'))
      throw error
    } finally {
      deletingResourceIds.value = deletingResourceIds.value.filter((id) => id !== resourceId)
    }
  }

  const updateSegment = async (
    projectId: number,
    resourceId: number,
    segmentId: number,
    payload: SegmentUpdatePayload,
  ): Promise<Segment> => {
    editingSegmentIds.value = [...editingSegmentIds.value, segmentId]
    actionError.value = null

    try {
      const segment = await updateResourceSegmentRequest(projectId, resourceId, segmentId, payload)
      segments.value = segments.value.map((item) => (item.id === segment.id ? segment : item))
      return segment
    } catch (error) {
      actionError.value = getErrorMessage(error, t('api.errors.updateSegmentFailed'))
      throw error
    } finally {
      editingSegmentIds.value = editingSegmentIds.value.filter((id) => id !== segmentId)
    }
  }

  // ── Actions：翻译任务操作 ──

  const createJob = async (
    projectId: number,
    payload: CreateTranslationJobPayload,
  ): Promise<TranslationJob> => {
    creatingJob.value = true
    actionError.value = null

    try {
      const job = await createTranslationJobRequest(projectId, payload)
      jobs.value = upsertById(jobs.value, job)
      return job
    } catch (error) {
      actionError.value = getErrorMessage(error, t('api.errors.createTranslationJobFailed'))
      throw error
    } finally {
      creatingJob.value = false
    }
  }

  const cancelJob = async (translationJobId: number): Promise<void> => {
    cancellingJobIds.value = [...cancellingJobIds.value, translationJobId]
    actionError.value = null

    try {
      const job = await cancelTranslationJobRequest(translationJobId)
      jobs.value = jobs.value.map((item) => (item.id === job.id ? job : item))
    } catch (error) {
      actionError.value = getErrorMessage(error, t('api.errors.cancelTranslationJobFailed'))
      throw error
    } finally {
      cancellingJobIds.value = cancellingJobIds.value.filter((id) => id !== translationJobId)
    }
  }

  const retryJob = async (translationJobId: number): Promise<void> => {
    retryingJobIds.value = [...retryingJobIds.value, translationJobId]
    actionError.value = null

    try {
      const job = await retryTranslationJobRequest(translationJobId)
      jobs.value = jobs.value.map((item) => (item.id === job.id ? job : item))
    } catch (error) {
      actionError.value = getErrorMessage(error, t('api.errors.retryTranslationJobFailed'))
      throw error
    } finally {
      retryingJobIds.value = retryingJobIds.value.filter((id) => id !== translationJobId)
    }
  }

  // ── Actions：下载 ──

  const downloadResource = async (
    projectId: number,
    resourceId: number,
  ): Promise<DownloadFileResult> => {
    const key = `resource:${resourceId}`
    downloadingKeys.value = [...downloadingKeys.value, key]
    actionError.value = null

    try {
      return await downloadProjectResourceRequest(projectId, resourceId)
    } catch (error) {
      actionError.value = getErrorMessage(error, t('api.errors.downloadResourceFailed'))
      throw error
    } finally {
      downloadingKeys.value = downloadingKeys.value.filter((item) => item !== key)
    }
  }

  const downloadJobResult = async (
    translationJobId: number,
    resourceId?: number,
  ): Promise<DownloadFileResult> => {
    const key = `job:${translationJobId}:${resourceId ?? 'all'}`
    downloadingKeys.value = [...downloadingKeys.value, key]
    actionError.value = null

    try {
      return await downloadTranslationJobResultRequest(translationJobId, resourceId)
    } catch (error) {
      actionError.value = getErrorMessage(error, t('api.errors.downloadTranslationJobFailed'))
      throw error
    } finally {
      downloadingKeys.value = downloadingKeys.value.filter((item) => item !== key)
    }
  }

  // ── Actions：工具方法 ──

  const setActiveResource = (resourceId: number | null): void => {
    activeResourceId.value = resourceId
    segments.value = []
    segmentsCursor.value = null
  }

  const reset = (): void => {
    project.value = null
    resourceTree.value = null
    currentPath.value = ''
    loadingResourceTree.value = false
    resourceTreeError.value = null
    resources.value = []
    selectedResourceIds.value = []
    activeResourceId.value = null
    segments.value = []
    jobs.value = []
    selectedJob.value = null
    resourcesCursor.value = null
    segmentsCursor.value = null
    jobsCursor.value = null
    projectError.value = null
    resourcesError.value = null
    segmentsError.value = null
    jobsError.value = null
    jobDetailError.value = null
    actionError.value = null
    clearAllUploadTasks()
    clearPendingUploadItems()
    lastUploadResult.value = null
    incrementalUpdatingIds.value = []
    resourceSearch.value = ''
    resourceStatusFilter.value = 'all'
    resourceFormatFilter.value = 'all'
    segmentSearch.value = ''
    segmentStatusFilter.value = 'all'
    jobStatusFilter.value = 'all'
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
    reset,
  }
})
