import { defineStore } from 'pinia'
import { computed, onScopeDispose, ref, watch } from 'vue'

import {
  type ApiSchemas,
  type DownloadFileResult,
  deleteProjectResource as deleteProjectResourceRequest,
  downloadProjectResource as downloadProjectResourceRequest,
  downloadResourceResult as downloadResourceResultRequest,
  fetchProjectResources,
  fetchProjectResourceTree,
  precheckProjectResources as precheckProjectResourcesRequest,
  uploadProjectResourcesWithProgress,
} from '@/api/client'
import { t } from '@/i18n'
import { extractErrorMessage } from '@/utils/errors'
import {
  captureSession,
  assertSessionCurrent,
  isSessionCurrent,
  onSessionChange,
} from '@/api/session-context'
import { isAccessDenied } from '@/api/utils'
import { getStorageContractGate } from '@/utils/storage-contract'

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
  descendantResourceIds?: number[]
}

export interface UploadTask {
  id: string
  fileName: string
  /** 任务包含的文件数（拖拽 / 批量选择会合并为一个批次任务） */
  fileCount: number
  stage: 'prechecking' | 'uploading' | 'processing' | 'complete' | 'partial' | 'error'
  progress: number
  errorMessage?: string
  summary?: UploadResultSummary
}

export type PendingUploadStrategy = 'create' | 'source_update' | 'skip'

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

const collectDescendantResources = (node: ResourceTreeNode): Resource[] => {
  const result: Resource[] = []

  const walk = (current: ResourceTreeNode): void => {
    if (current.type === 'resource' && current.resource) {
      result.push(current.resource)
    }
    for (const child of current.children ?? []) {
      walk(child)
    }
  }

  walk(node)
  return result
}

const collectDescendantResourceIds = (node: ResourceTreeNode): number[] =>
  collectDescendantResources(node).map((resource) => resource.id)

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
  let treeRequestRevision = 0
  let listRequestRevision = 0
  let projectContextRevision = 0
  let resourceSnapshotRevision = 0
  let activeProjectId: number | null = null
  let listTarget: string | null = null
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

  const clearResourceList = (): void => {
    resources.value = []
    resourcesCursor.value = null
    selectedResourceIds.value = []
    activeResourceId.value = null
  }
  const activateProject = (projectId: number): void => {
    if (activeProjectId === projectId) return
    activeProjectId = projectId
    projectContextRevision++
    treeRequestRevision++
    listRequestRevision++
    resourceSnapshotRevision++
    resourceTree.value = null
    currentPath.value = ''
    resourceTreeError.value = resourcesError.value = null
    loadingResourceTree.value = loadingResources.value = false
    listTarget = null
    clearResourceList()
  }
  // List responses are tied to the requested filter, not whichever filter is visible later.
  watch(
    [resourceSearch, resourceFormatFilter],
    () => {
      if (listTarget === null) return
      listRequestRevision++
      resourceSnapshotRevision++
      loadingResources.value = false
      resourcesError.value = null
      listTarget = null
      clearResourceList()
    },
    { flush: 'sync' },
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
        descendantResourceIds: collectDescendantResourceIds(c),
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

  /** 增量设置一组资源的选中状态 */
  const setResourceSelection = (ids: number[], selected: boolean): void => {
    if (ids.length === 0) {
      return
    }

    const nextIds = new Set(selectedResourceIds.value)
    for (const id of ids) {
      if (selected) {
        nextIds.add(id)
      } else {
        nextIds.delete(id)
      }
    }
    selectedResourceIds.value = [...nextIds]
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

  /** 刷新资源树后同步更新扁平资源列表 */
  const syncResourcesFromTree = (): void => {
    resources.value = resourceTree.value ? collectDescendantResources(resourceTree.value) : []

    const resourceIdSet = new Set(resources.value.map((resource) => resource.id))
    selectedResourceIds.value = selectedResourceIds.value.filter((id) => resourceIdSet.has(id))
    if (activeResourceId.value && !resourceIdSet.has(activeResourceId.value)) {
      activeResourceId.value = resources.value[0]?.id ?? null
    }
  }

  const loadResourceTree = async (projectId: number): Promise<void> => {
    activateProject(projectId)
    const revision = ++treeRequestRevision
    const contextRevision = projectContextRevision
    const snapshotRevision = ++resourceSnapshotRevision
    // A tree refresh produces the flat list too; a prior list fetch cannot overwrite it.
    listRequestRevision++
    loadingResources.value = false
    listTarget = null
    const session = captureSession()
    const current = () =>
      revision === treeRequestRevision &&
      contextRevision === projectContextRevision &&
      activeProjectId === projectId &&
      isSessionCurrent(session)
    loadingResourceTree.value = true
    resourceTreeError.value = null

    try {
      const response = await fetchProjectResourceTree(projectId)
      if (!current()) return
      resourceTree.value = response.root
      if (snapshotRevision === resourceSnapshotRevision) {
        syncResourcesFromTree()
        resourcesCursor.value = null
        resourcesError.value = null
      }
    } catch (error) {
      if (!current()) return
      if (isAccessDenied(error)) {
        resourceTree.value = null
        currentPath.value = ''
        if (snapshotRevision === resourceSnapshotRevision) clearResourceList()
      }
      resourceTreeError.value = extractErrorMessage(error, t('api.errors.fetchResourceTreeFailed'))
    } finally {
      if (current()) loadingResourceTree.value = false
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

  // ── Actions：资源列表（保留用于段落 Tab 和筛选） ──

  const loadResources = async (projectId: number, append = false): Promise<void> => {
    activateProject(projectId)
    const format = resourceFormatFilter.value
    const search = resourceSearch.value.trim()
    const target = JSON.stringify([projectId, format, search])
    const canAppend = append && listTarget === target
    if (
      (listTarget !== null && listTarget !== target) ||
      (listTarget === null && (format !== 'all' || search))
    )
      clearResourceList()
    listTarget = target
    const revision = ++listRequestRevision
    const contextRevision = projectContextRevision
    const snapshotRevision = ++resourceSnapshotRevision
    const session = captureSession()
    const current = () =>
      revision === listRequestRevision &&
      snapshotRevision === resourceSnapshotRevision &&
      contextRevision === projectContextRevision &&
      activeProjectId === projectId &&
      isSessionCurrent(session)
    loadingResources.value = true
    resourcesError.value = null

    try {
      const response = await fetchProjectResources(projectId, {
        format: format === 'all' ? undefined : format,
        search: search || undefined,
        cursor: canAppend ? (resourcesCursor.value ?? undefined) : undefined,
        limit: 50,
      })
      if (!current()) return
      resources.value = canAppend ? [...resources.value, ...response.items] : response.items
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
      if (!current()) return
      if (isAccessDenied(error)) {
        clearResourceList()
        // The tree is another snapshot of this same project's resource collection.
        treeRequestRevision++
        loadingResourceTree.value = false
        resourceTree.value = null
        currentPath.value = ''
      }
      resourcesError.value = extractErrorMessage(error, t('api.errors.fetchResourcesFailed'))
    } finally {
      if (current()) loadingResources.value = false
    }
  }

  // ── Actions：上传 ──

  const uploadBatches = new Map<
    string,
    { projectId: number; files: File[]; paths: string[]; key: string }
  >()

  const addUploadTask = (fileName: string, fileCount = 1): string => {
    const id = crypto.randomUUID()
    uploadTasks.value = [
      ...uploadTasks.value,
      { id, fileName, fileCount, stage: 'uploading', progress: 0 },
    ]
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
    uploadBatches.delete(taskId)
    uploadTasks.value = uploadTasks.value.filter((task) => task.id !== taskId)
  }

  const clearCompletedUploadTasks = (): void => {
    uploadTasks.value = uploadTasks.value.filter((task) => task.stage !== 'complete')
  }

  const clearAllUploadTasks = (): void => {
    uploadTasks.value = []
    uploadBatches.clear()
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
              ? getStorageContractGate('sourceUpdate').available
                ? 'source_update'
                : 'skip'
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
    const session = captureSession()
    if (activeProjectId === null) activateProject(projectId)
    const contextRevision = projectContextRevision
    const current = () =>
      isSessionCurrent(session) &&
      activeProjectId === projectId &&
      contextRevision === projectContextRevision
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
      if (current()) lastUploadResult.value = result
      return result
    }

    const batchId = taskId ?? crypto.randomUUID()
    const intendedPaths = files.map((file, index) => paths?.[index] ?? file.name)
    let batch = uploadBatches.get(batchId)
    if (
      batch &&
      (batch.projectId !== projectId ||
        batch.files.length !== files.length ||
        files.some(
          (file, index) =>
            file !== batch!.files[index] || intendedPaths[index] !== batch!.paths[index],
        ))
    ) {
      throw new Error(t('sourceStorage.batchChanged'))
    }
    batch ??= { projectId, files: [...files], paths: [...intendedPaths], key: crypto.randomUUID() }
    uploadBatches.set(batchId, batch)
    if (current()) actionError.value = null

    try {
      const response = await uploadProjectResourcesWithProgress(
        projectId,
        batch.files,
        batch.paths,
        {
          idempotencyKey: batch.key,
          signal: session.signal,
          onProgress: (percent) => {
            if (taskId && current()) {
              updateUploadTaskProgress(taskId, percent)
            }
          },
          onServerProcessing: () => {
            if (taskId && current()) {
              updateUploadTaskStage(taskId, 'processing')
            }
          },
        },
      )
      assertSessionCurrent(session)
      const summary = buildUploadSummary(response, skippedItems)
      const result = { response, skippedItems, incrementalResults: [], replaceResults: [], summary }
      if (!current()) return result
      // Publishing new objects supersedes resource snapshots requested before the upload finished.
      resourceSnapshotRevision++
      listRequestRevision++
      treeRequestRevision++
      loadingResources.value = loadingResourceTree.value = false
      const createdResources = response.items
        .filter((item) => item.action === 'created' && item.resource)
        .map((item) => item.resource!)
      const createdById = new Map(createdResources.map((resource) => [resource.id, resource]))
      resources.value = [
        ...createdById.values(),
        ...resources.value.filter((resource) => !createdById.has(resource.id)),
      ]
      if (!activeResourceId.value && createdResources[0]) {
        activeResourceId.value = createdResources[0].id
      }
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
      assertSessionCurrent(session)
      const message = extractErrorMessage(error, t('api.errors.uploadResourcesFailed'))
      if (current()) {
        actionError.value = message
        if (taskId) updateUploadTaskStage(taskId, 'error', message)
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
      actionError.value = extractErrorMessage(error, t('api.errors.deleteResourceFailed'))
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
      actionError.value = extractErrorMessage(error, t('api.errors.downloadResourceFailed'))
      throw error
    } finally {
      downloadingKeys.value = downloadingKeys.value.filter((item) => item !== key)
    }
  }

  const downloadResourceResult = async (
    projectId: number,
    resourceId: number,
  ): Promise<DownloadFileResult> => {
    const key = `resource:${resourceId}:translated`
    downloadingKeys.value = [...downloadingKeys.value, key]
    actionError.value = null

    try {
      return await downloadResourceResultRequest(projectId, resourceId)
    } catch (error) {
      actionError.value = extractErrorMessage(error, t('api.errors.downloadResourceResultFailed'))
      throw error
    } finally {
      downloadingKeys.value = downloadingKeys.value.filter((item) => item !== key)
    }
  }

  // ── 工具方法 ──

  const reset = (): void => {
    projectContextRevision++
    resourceSnapshotRevision++
    activeProjectId = null
    listTarget = null
    treeRequestRevision++
    listRequestRevision++
    resourceTree.value = null
    currentPath.value = ''
    loadingResourceTree.value = false
    resourceTreeError.value = null
    resources.value = []
    selectedResourceIds.value = []
    activeResourceId.value = null
    resourcesCursor.value = null
    resourcesError.value = null
    loadingResources.value = false
    resourceSearch.value = ''
    resourceFormatFilter.value = 'all'
    clearAllUploadTasks()
    clearPendingUploadItems()
    lastUploadResult.value = null
    incrementalUpdatingIds.value = []
    actionError.value = null
  }

  onScopeDispose(onSessionChange(reset))
  onScopeDispose(reset)

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
    deleteResource,
    downloadResource,
    downloadResourceResult,
    toggleResourceSelection,
    setSelectedResourceIds,
    setResourceSelection,
    clearSelectedResources,
    reset,
  }
})
