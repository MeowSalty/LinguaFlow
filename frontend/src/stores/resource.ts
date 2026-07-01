import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import {
  type ApiSchemas,
  type DownloadFileResult,
  deleteProjectResource as deleteProjectResourceRequest,
  downloadProjectResource as downloadProjectResourceRequest,
  downloadTranslatedResource as downloadTranslatedResourceRequest,
  fetchProjectResources,
  fetchProjectResourceTree,
  incrementalUpdateResource as incrementalUpdateResourceRequest,
  precheckProjectResources as precheckProjectResourcesRequest,
  replaceProjectResource as replaceProjectResourceRequest,
  uploadProjectResourcesWithProgress,
} from '@/api/client'
import { fetchSegmentGroups, type ResourceSegmentGroup } from '@/api/epub'
import { t } from '@/i18n'

// Re-export SegmentGroup 类型供外部使用
export type { ResourceSegmentGroup as SegmentGroup }

type Resource = ApiSchemas['Resource']
type ResourceTreeNode = ApiSchemas['ResourceTreeNode']
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

const getErrorMessage = (error: unknown, fallback: string): string =>
  error instanceof Error ? error.message : fallback

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

export const useResourceStore = defineStore('resource', () => {
  // ── 资源目录树 ──
  const resourceTree = ref<ResourceTreeNode | null>(null)
  const currentPath = ref('')
  const loadingResourceTree = ref(false)
  const resourceTreeError = ref<string | null>(null)

  // ── 资源列表（用于段落 Tab 筛选器和兼容旧逻辑） ──
  const resources = ref<Resource[]>([])
  const selectedResourceIds = ref<number[]>([])
  const activeResourceId = ref<number | null>(null)
  const resourcesCursor = ref<string | null>(null)

  // ── 加载状态 ──
  const loadingResources = ref(false)
  const uploadTasks = ref<UploadTask[]>([])
  const pendingUploadItems = ref<PendingUploadItem[]>([])
  const lastUploadResult = ref<UploadExecutionResult | null>(null)
  const replacingResourceIds = ref<number[]>([])
  const incrementalUpdatingIds = ref<number[]>([])
  const deletingResourceIds = ref<number[]>([])
  const downloadingKeys = ref<string[]>([])

  // ── 错误状态 ──
  const resourcesError = ref<string | null>(null)
  const actionError = ref<string | null>(null)

  // ── 筛选器 ──
  const resourceSearch = ref('')
  const resourceFormatFilter = ref<string>('all')

  // ── EPUB 虚拟目录导航状态 ──

  /** 当前 EPUB 资源 ID（进入 EPUB 时设置，退出时清空） */
  const epubDirectoryResourceId = ref<number | null>(null)

  /** 当前 EPUB 资源名称 */
  const epubDirectoryResourceName = ref<string>('')

  /** 当前 EPUB 的章节列表 */
  const epubDirectoryChapters = ref<ResourceSegmentGroup[]>([])

  /** EPUB 章节列表加载状态 */
  const epubDirectoryLoading = ref(false)

  /** 是否处于 EPUB 虚拟目录中 */
  const isInEpubDirectory = computed(() => epubDirectoryResourceId.value !== null)

  /** 面包屑末尾追加的 EPUB 名称（仅在 EPUB 目录中时非空） */
  const epubDirectoryBreadcrumbSuffix = computed(() =>
    isInEpubDirectory.value ? epubDirectoryResourceName.value : '',
  )

  // ── 计算属性：资源树导航 ──

  /** 面包屑路径列表 */
  const breadcrumbs = computed<BreadcrumbItem[]>(() => {
    const items: BreadcrumbItem[] = []

    if (currentPath.value) {
      const parts = currentPath.value.split('/')
      for (const [index, part] of parts.entries()) {
        items.push({
          label: part,
          path: parts.slice(0, index + 1).join('/'),
        })
      }
    }

    // EPUB 虚拟目录模式下，追加 EPUB 名称到面包屑末尾
    if (isInEpubDirectory.value && epubDirectoryResourceName.value) {
      items.push({
        label: epubDirectoryResourceName.value,
        path: currentPath.value, // 点击返回 EPUB 所在目录
      })
    }

    return items
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

  // ── Actions：资源选择 ──

  /** 切换单个资源的选中状态 */
  const toggleResourceSelection = (id: number): void => {
    const index = selectedResourceIds.value.indexOf(id)
    if (index === -1) {
      selectedResourceIds.value = [...selectedResourceIds.value, id]
    } else {
      const copy = [...selectedResourceIds.value]
      copy.splice(index, 1)
      selectedResourceIds.value = copy
    }
  }

  /** 设置选中的资源 ID 列表 */
  const setSelectedResourceIds = (ids: number[]): void => {
    selectedResourceIds.value = [...ids]
  }

  /** 清除所有选中 */
  const clearSelectedResources = (): void => {
    selectedResourceIds.value = []
  }

  const availableFormats = computed<string[]>(() =>
    [...new Set(resources.value.map((resource) => resource.format).filter(Boolean))].sort(),
  )
  const totalSegmentCount = computed(() =>
    resources.value.reduce((total, resource) => total + resource.total_segments, 0),
  )
  const totalTranslatedSegments = computed(() =>
    resources.value.reduce((total, resource) => total + resource.translated_segments, 0),
  )
  const totalApprovedSegments = computed(() =>
    resources.value.reduce((total, resource) => total + resource.approved_segments, 0),
  )
  const hasActiveUploads = computed(() =>
    uploadTasks.value.some((task) =>
      ['prechecking', 'uploading', 'processing'].includes(task.stage),
    ),
  )

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

  /** 进入 EPUB 虚拟目录 */
  const enterEpub = async (
    projectId: number,
    resource: { id: number; name: string },
  ): Promise<void> => {
    epubDirectoryResourceId.value = resource.id
    epubDirectoryResourceName.value = resource.name
    epubDirectoryLoading.value = true
    try {
      const response = await fetchSegmentGroups(projectId, resource.id)
      epubDirectoryChapters.value = response.items
    } finally {
      epubDirectoryLoading.value = false
    }
  }

  /** 退出 EPUB 虚拟目录 */
  const exitEpub = (): void => {
    console.debug('[resourceStore] exitEpub called')
    epubDirectoryResourceId.value = null
    epubDirectoryResourceName.value = ''
    epubDirectoryChapters.value = []
  }

  /** 刷新当前 EPUB 的章节列表 */
  const refreshEpubChapters = async (projectId: number): Promise<void> => {
    if (epubDirectoryResourceId.value === null) return
    epubDirectoryLoading.value = true
    try {
      const response = await fetchSegmentGroups(projectId, epubDirectoryResourceId.value)
      epubDirectoryChapters.value = response.items
    } finally {
      epubDirectoryLoading.value = false
    }
  }

  /** 导航到指定目录路径 */
  const navigateTo = (path: string): void => {
    // 如果当前在 EPUB 虚拟目录中，先退出
    // 面包屑组件已通过 isEpubSuffixItem 阻止了 EPUB 末尾项的点击，
    // 所以此处无需额外判断，直接退出 EPUB 并导航到目标路径
    if (isInEpubDirectory.value) {
      exitEpub()
    }
    currentPath.value = path
  }

  /** 返回上级目录 */
  const navigateUp = (): void => {
    // 如果在 EPUB 虚拟目录中，退出 EPUB 而不是目录上移
    if (isInEpubDirectory.value) {
      exitEpub()
      return
    }

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
          summary.failed === summary.total
            ? 'error'
            : summary.failed > 0 || summary.conflicts > 0 || summary.skipped > 0
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

  /** 设置当前激活资源，可选通过回调重置关联段落 */
  const setActiveResource = (resourceId: number | null, resetSegments?: () => void): void => {
    activeResourceId.value = resourceId
    resetSegments?.()
  }

  const replaceResource = async (
    projectId: number,
    resourceId: number,
    file: File,
    resetSegments?: () => void,
  ): Promise<void> => {
    replacingResourceIds.value = [...replacingResourceIds.value, resourceId]
    actionError.value = null

    try {
      const resource = await replaceProjectResourceRequest(projectId, resourceId, file)
      resources.value = resources.value.map((item) => (item.id === resource.id ? resource : item))
      if (activeResourceId.value === resourceId) {
        resetSegments?.()
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
    resetSegments?: () => void,
  ): Promise<ApiSchemas['IncrementalUpdateResponse']> => {
    incrementalUpdatingIds.value = [...incrementalUpdatingIds.value, resourceId]
    actionError.value = null

    try {
      const result = await incrementalUpdateResourceRequest(projectId, resourceId, file)
      resources.value = resources.value.map((item) =>
        item.id === result.resource.id ? result.resource : item,
      )
      if (activeResourceId.value === resourceId) {
        resetSegments?.()
      }
      return result
    } catch (error) {
      actionError.value = getErrorMessage(error, t('api.errors.incrementalUpdateFailed'))
      throw error
    } finally {
      incrementalUpdatingIds.value = incrementalUpdatingIds.value.filter((id) => id !== resourceId)
    }
  }

  const deleteResource = async (
    projectId: number,
    resourceId: number,
    resetSegments?: () => void,
  ): Promise<void> => {
    deletingResourceIds.value = [...deletingResourceIds.value, resourceId]
    actionError.value = null

    try {
      await deleteProjectResourceRequest(projectId, resourceId)
      resources.value = resources.value.filter((resource) => resource.id !== resourceId)
      selectedResourceIds.value = selectedResourceIds.value.filter((id) => id !== resourceId)
      if (activeResourceId.value === resourceId) {
        activeResourceId.value = resources.value[0]?.id ?? null
        resetSegments?.()
      }
    } catch (error) {
      actionError.value = getErrorMessage(error, t('api.errors.deleteResourceFailed'))
      throw error
    } finally {
      deletingResourceIds.value = deletingResourceIds.value.filter((id) => id !== resourceId)
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

  const downloadTranslatedResource = async (
    projectId: number,
    resourceId: number,
  ): Promise<DownloadFileResult> => {
    const key = `resource:${resourceId}:translated`
    downloadingKeys.value = [...downloadingKeys.value, key]
    actionError.value = null

    try {
      return await downloadTranslatedResourceRequest(projectId, resourceId)
    } catch (error) {
      actionError.value = getErrorMessage(error, t('api.errors.downloadTranslatedResourceFailed'))
      throw error
    } finally {
      downloadingKeys.value = downloadingKeys.value.filter((item) => item !== key)
    }
  }

  // ── 工具方法 ──

  const reset = (): void => {
    resourceTree.value = null
    currentPath.value = ''
    loadingResourceTree.value = false
    resourceTreeError.value = null
    resources.value = []
    selectedResourceIds.value = []
    activeResourceId.value = null
    resourcesCursor.value = null
    resourcesError.value = null
    resourceSearch.value = ''
    resourceFormatFilter.value = 'all'
    clearAllUploadTasks()
    clearPendingUploadItems()
    lastUploadResult.value = null
    incrementalUpdatingIds.value = []
    actionError.value = null
    exitEpub()
  }

  return {
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
    resourcesCursor,
    loadingResources,
    resourcesError,
    resourceSearch,
    resourceFormatFilter,
    // 上传
    uploadTasks,
    pendingUploadItems,
    lastUploadResult,
    hasActiveUploads,
    replacingResourceIds,
    incrementalUpdatingIds,
    deletingResourceIds,
    downloadingKeys,
    actionError,
    // 计算属性
    availableFormats,
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
    // Actions
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
    setActiveResource,
    replaceResource,
    incrementalUpdateResource,
    deleteResource,
    downloadResource,
    downloadTranslatedResource,
    toggleResourceSelection,
    setSelectedResourceIds,
    clearSelectedResources,
    enterEpub,
    exitEpub,
    refreshEpubChapters,
    reset,
  }
})
