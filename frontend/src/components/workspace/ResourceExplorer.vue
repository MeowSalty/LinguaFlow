<script setup lang="ts">
import {
  NAlert,
  NButton,
  NEmpty,
  NIcon,
  NModal,
  NUpload,
  useMessage,
  type UploadCustomRequestOptions,
} from 'naive-ui'
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import { type ApiSchemas } from '@/api/client'
import ResourceBreadcrumb from '@/components/workspace/ResourceBreadcrumb.vue'
import UploadPrecheckPanel from '@/components/workspace/UploadPrecheckPanel.vue'
import { useResourceViewStrategy } from '@/composables/workspace/useResourceViewStrategy'
import {
  useProjectWorkspaceStore,
  type PendingUploadItem,
  type ReplaceUploadResult,
} from '@/stores/projectWorkspace'

type Resource = ApiSchemas['Resource']
type IncrementalUpdateResponse = ApiSchemas['IncrementalUpdateResponse']

const props = defineProps<{
  projectId: number
}>()

const emit = defineEmits<{
  openSegments: [resource: Resource]
  conflict: [resource: Resource, file: File]
  incrementalResult: [result: IncrementalUpdateResponse]
  openEpubSegments: [resourceId: number, groupKey: string]
}>()

const message = useMessage()
const { t } = useI18n()
const workspace = useProjectWorkspaceStore()
const { currentStrategyName, toolbarMeta, activeViewComponent } = useResourceViewStrategy()

const dragOver = ref(false)
const uploadPrecheckVisible = ref(false)
const uploadConfirming = ref(false)
const pendingUploadTaskId = ref<string | null>(null)

// ── 计算属性 ──

const directories = computed(() =>
  workspace.currentDirectoryChildren.filter((child) => child.type === 'directory'),
)

const resourceItems = computed(() =>
  workspace.currentDirectoryChildren.filter((child) => child.type === 'resource'),
)

const isEmpty = computed(
  () =>
    !workspace.loadingResourceTree &&
    workspace.currentDirectoryChildren.length === 0 &&
    !workspace.isInEpubDirectory,
)

/** 当前目录中已选中的资源 ID 集合（用于快速查找） */
const selectedIdSet = computed(() => new Set(workspace.selectedResourceIds))

// ── 生命周期 ──

watch(
  () => props.projectId,
  (id) => {
    if (id) {
      void workspace.loadResourceTree(id)
    }
  },
  { immediate: true },
)

// ── 导航 ──

const handleNavigate = (path: string): void => {
  workspace.navigateTo(path)
}

const handleNavigateUp = (): void => {
  workspace.navigateUp()
}

// ── EPUB 章节操作 ──

const handleChapterClick = (groupKey: string): void => {
  const resourceId = workspace.epubDirectoryResourceId
  if (!resourceId) return
  emit('openEpubSegments', resourceId, groupKey)
}

const handleRefreshDirectory = async (): Promise<void> => {
  await workspace.loadResourceTree(props.projectId)
}

// ── 资源操作 ──

const chooseReplacementFile = (resourceId: number): void => {
  const input = document.createElement('input')
  input.type = 'file'
  input.onchange = () => {
    const file = input.files?.[0]
    if (file) {
      void doReplace(resourceId, file)
    }
  }
  input.click()
}

const doReplace = async (resourceId: number, file: File): Promise<void> => {
  try {
    await workspace.replaceResource(props.projectId, resourceId, file)
    message.success(t('workspace.messages.replaceSuccess'))
    await workspace.loadResourceTree(props.projectId)
  } catch (error) {
    console.error(error)
    message.error(workspace.actionError || t('workspace.messages.replaceFailed'))
  }
}

const chooseIncrementalUpdateFile = (resourceId: number): void => {
  const input = document.createElement('input')
  input.type = 'file'
  input.onchange = () => {
    const file = input.files?.[0]
    if (file) {
      void doIncrementalUpdate(resourceId, file)
    }
  }
  input.click()
}

const doIncrementalUpdate = async (resourceId: number, file: File): Promise<void> => {
  try {
    const result = await workspace.incrementalUpdateResource(props.projectId, resourceId, file)
    emit('incrementalResult', result)
    await workspace.loadResourceTree(props.projectId)
  } catch (error) {
    console.error(error)
    message.error(workspace.actionError || t('workspace.messages.incrementalUpdateFailed'))
  }
}

const downloadResource = async (resource: Resource): Promise<void> => {
  try {
    const file = await workspace.downloadResource(props.projectId, resource.id)
    const url = URL.createObjectURL(file.blob)
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = file.filename || resource.name
    anchor.click()
    URL.revokeObjectURL(url)
  } catch (error) {
    console.error(error)
    message.error(workspace.actionError || t('workspace.messages.downloadFailed'))
  }
}

const downloadTranslatedResource = async (resource: Resource): Promise<void> => {
  try {
    const file = await workspace.downloadTranslatedResource(props.projectId, resource.id)
    const url = URL.createObjectURL(file.blob)
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = file.filename || `translated-${resource.name}`
    anchor.click()
    URL.revokeObjectURL(url)
  } catch (error) {
    console.error(error)
    message.error(workspace.actionError || t('workspace.messages.downloadFailed'))
  }
}

const deleteResource = async (resource: Resource): Promise<void> => {
  try {
    await workspace.deleteResource(props.projectId, resource.id)
    message.success(t('workspace.messages.deleteResourceSuccess'))
    await workspace.loadResourceTree(props.projectId)
  } catch (error) {
    console.error(error)
    message.error(workspace.actionError || t('workspace.messages.deleteResourceFailed'))
  }
}

// ── 上传 ──

const computeUploadPaths = (files: File[], directoryPrefix: string): string[] | undefined => {
  if (!directoryPrefix) {
    return undefined
  }

  return files.map((file) => {
    // webkitRelativePath 包含文件夹相对路径，如 "common.json" 或 "sub/common.json"
    const relativePath =
      (file as File & { webkitRelativePath?: string }).webkitRelativePath || file.name
    return directoryPrefix ? `${directoryPrefix}/${relativePath}` : relativePath
  })
}

const summarizeUploadName = (files: File[]): string =>
  files.length === 1 ? files[0]!.name : t('workspace.upload.batchName', { count: files.length })

const executeIncrementalUploadItems = async (
  items: PendingUploadItem[],
): Promise<import('@/stores/projectWorkspace').IncrementalUploadResult[]> => {
  const results: import('@/stores/projectWorkspace').IncrementalUploadResult[] = []

  for (const item of items) {
    const resourceId = item.precheck.existing_resource?.id
    if (!resourceId) {
      results.push({ item, error: t('workspace.uploadResult.details.missingExistingResource') })
      continue
    }

    try {
      const result = await workspace.incrementalUpdateResource(
        props.projectId,
        resourceId,
        item.file,
      )
      results.push({ item, result })
    } catch (error) {
      results.push({
        item,
        error:
          error instanceof Error ? error.message : t('workspace.messages.incrementalUpdateFailed'),
      })
    }
  }

  return results
}

const executeReplaceUploadItems = async (
  items: PendingUploadItem[],
): Promise<ReplaceUploadResult[]> => {
  const results: ReplaceUploadResult[] = []

  for (const item of items) {
    const resourceId = item.precheck.existing_resource?.id
    if (!resourceId) {
      results.push({ item, error: t('workspace.uploadResult.details.missingExistingResource') })
      continue
    }

    try {
      await workspace.replaceResource(props.projectId, resourceId, item.file)
      results.push({ item, result: true })
    } catch (error) {
      results.push({
        item,
        error: error instanceof Error ? error.message : t('workspace.messages.replaceFailed'),
      })
    }
  }

  return results
}

const executeUploadItems = async (items: PendingUploadItem[], taskId: string): Promise<void> => {
  const selectedItems = items.filter((item) => item.selected && item.strategy === 'create')
  const incrementalItems = items.filter((item) => item.strategy === 'incremental_update')
  const replaceItems = items.filter((item) => item.strategy === 'replace')
  const skippedItems = items.filter((item) => item.strategy === 'skip')

  workspace.updateUploadTaskStage(taskId, 'uploading')
  await workspace.uploadResources(
    props.projectId,
    selectedItems.map((item) => item.file),
    selectedItems.map((item) => item.path),
    taskId,
    skippedItems,
  )

  if (incrementalItems.length > 0 || replaceItems.length > 0) {
    workspace.updateUploadTaskStage(taskId, 'processing')
  }
  const incrementalResults = await executeIncrementalUploadItems(incrementalItems)
  const replaceResults = await executeReplaceUploadItems(replaceItems)
  const mergedResult = workspace.mergeLastUploadResult(incrementalResults, replaceResults)

  await workspace.loadResourceTree(props.projectId)
  if (mergedResult.summary.failed === mergedResult.summary.total) {
    workspace.updateUploadTaskStage(taskId, 'error', undefined, mergedResult.summary)
    message.error(t('workspace.messages.uploadFailed'))
  } else if (
    mergedResult.summary.failed > 0 ||
    mergedResult.summary.conflicts > 0 ||
    mergedResult.summary.skipped > 0
  ) {
    workspace.updateUploadTaskStage(taskId, 'partial', undefined, mergedResult.summary)
    message.warning(t('workspace.messages.uploadPartialSuccess', { ...mergedResult.summary }))
  } else {
    workspace.updateUploadTaskStage(taskId, 'complete', undefined, mergedResult.summary)
    message.success(t('workspace.messages.uploadSuccess'))
  }
}

const beginUpload = async (
  files: File[],
  paths: string[] | undefined,
  displayName: string,
  callbacks?: Pick<UploadCustomRequestOptions, 'onFinish' | 'onError'>,
): Promise<void> => {
  if (files.length === 0) {
    callbacks?.onFinish?.()
    return
  }

  const taskId = workspace.addUploadTask(displayName)
  workspace.updateUploadTaskStage(taskId, 'prechecking')

  try {
    const items = await workspace.precheckUploadResources(props.projectId, files, paths)
    workspace.setPendingUploadItems(items)

    if (items.some((item) => item.precheck.action !== 'create')) {
      pendingUploadTaskId.value = taskId
      uploadPrecheckVisible.value = true
      callbacks?.onFinish?.()
      return
    }

    await executeUploadItems(items, taskId)
    callbacks?.onFinish?.()
  } catch (error) {
    console.error(error)
    message.error(workspace.actionError || t('workspace.messages.uploadFailed'))
    workspace.updateUploadTaskStage(
      taskId,
      'error',
      workspace.actionError || t('workspace.messages.uploadFailed'),
    )
    callbacks?.onError?.()
  }
}

const confirmPrecheckedUpload = async (): Promise<void> => {
  const taskId = pendingUploadTaskId.value
  if (!taskId) {
    return
  }

  uploadConfirming.value = true
  try {
    await executeUploadItems(workspace.pendingUploadItems, taskId)
    uploadPrecheckVisible.value = false
    pendingUploadTaskId.value = null
    workspace.clearPendingUploadItems()
  } catch (error) {
    console.error(error)
    message.error(workspace.actionError || t('workspace.messages.uploadFailed'))
  } finally {
    uploadConfirming.value = false
  }
}

const cancelPrecheckedUpload = (): void => {
  if (pendingUploadTaskId.value) {
    workspace.removeUploadTask(pendingUploadTaskId.value)
  }
  pendingUploadTaskId.value = null
  uploadPrecheckVisible.value = false
  workspace.clearPendingUploadItems()
}

const handleUpload = async ({
  file,
  onFinish,
  onError,
}: UploadCustomRequestOptions): Promise<void> => {
  if (!file.file) {
    onError()
    return
  }

  const files = [file.file]
  const paths = computeUploadPaths(files, workspace.currentPath)
  await beginUpload(files, paths, file.name, { onFinish, onError })
}

// ── 拖拽上传 ──

const handleDragOver = (event: DragEvent): void => {
  event.preventDefault()
  dragOver.value = true
}

const handleDragLeave = (): void => {
  dragOver.value = false
}

const handleDrop = async (event: DragEvent): Promise<void> => {
  event.preventDefault()
  dragOver.value = false

  const items = event.dataTransfer?.items
  if (!items) {
    return
  }

  const collectedFiles: { file: File; relativePath: string }[] = []

  const traverseEntry = (entry: FileSystemEntry, basePath: string): Promise<void> =>
    new Promise((resolve) => {
      if (entry.isFile) {
        ;(entry as FileSystemFileEntry).file((file) => {
          const relativePath = basePath ? `${basePath}/${entry.name}` : entry.name
          collectedFiles.push({ file, relativePath })
          resolve()
        })
      } else if (entry.isDirectory) {
        const reader = (entry as FileSystemDirectoryEntry).createReader()
        reader.readEntries(async (entries) => {
          const childPath = basePath ? `${basePath}/${entry.name}` : entry.name
          for (const child of entries) {
            await traverseEntry(child, childPath)
          }
          resolve()
        })
      } else {
        resolve()
      }
    })

  const promises: Promise<void>[] = []
  for (let i = 0; i < items.length; i++) {
    const entry = items[i]?.webkitGetAsEntry?.()
    if (entry) {
      promises.push(traverseEntry(entry, ''))
    }
  }
  await Promise.all(promises)

  if (collectedFiles.length === 0) {
    return
  }

  const files = collectedFiles.map((item) => item.file)
  const currentPrefix = workspace.currentPath
  const paths = collectedFiles.map((item) =>
    currentPrefix ? `${currentPrefix}/${item.relativePath}` : item.relativePath,
  )

  await beginUpload(files, paths, summarizeUploadName(files))
}

// ── 视图 Props / Events 桥接 ──

/** 传递给当前活跃视图组件的 props */
const currentViewProps = computed(() => {
  if (currentStrategyName.value === 'epub-directory') {
    return {
      loading: workspace.epubDirectoryLoading,
      chapters: workspace.epubDirectoryChapters,
      selectedGroupKeys: workspace.epubSelectedGroupKeys,
    }
  }
  return {
    directories: directories.value,
    resourceItems: resourceItems.value,
    selectedIdSet: selectedIdSet.value,
    replacingResourceIds: workspace.replacingResourceIds,
    incrementalUpdatingIds: workspace.incrementalUpdatingIds,
    downloadingKeys: workspace.downloadingKeys,
    deletingResourceIds: workspace.deletingResourceIds,
  }
})

/** 传递给当前活跃视图组件的事件处理器 */
const currentViewEvents = computed(() => {
  if (currentStrategyName.value === 'epub-directory') {
    return {
      click: handleChapterClick,
      toggleSelect: (groupKey: string) => workspace.toggleEpubGroupSelection(groupKey),
    }
  }
  return {
    navigate: handleNavigate,
    openSegments: (r: Resource) => emit('openSegments', r),
    openEpubDirectory: (r: Resource) => emit('openSegments', r),
    replace: (r: Resource) => chooseReplacementFile(r.id),
    incrementalUpdate: (r: Resource) => chooseIncrementalUpdateFile(r.id),
    download: (r: Resource) => void downloadResource(r),
    downloadTranslated: (r: Resource) => void downloadTranslatedResource(r),
    delete: (r: Resource) => void deleteResource(r),
    toggleSelect: (r: Resource) => workspace.toggleResourceSelection(r.id),
    toggleSelectAll: (): void => {
      const allIds = resourceItems.value.map((item) => item.resource!.id)
      const selectedSet = new Set(workspace.selectedResourceIds)
      const allSelected = allIds.length > 0 && allIds.every((id) => selectedSet.has(id))
      if (allSelected) {
        const removeSet = new Set(allIds)
        workspace.setSelectedResourceIds(
          workspace.selectedResourceIds.filter((id: number) => !removeSet.has(id)),
        )
      } else {
        const merged = new Set([...workspace.selectedResourceIds, ...allIds])
        workspace.setSelectedResourceIds([...merged])
      }
    },
  }
})
</script>

<template>
  <div class="space-y-4" @dragover="handleDragOver" @dragleave="handleDragLeave" @drop="handleDrop">
    <!-- 资源路径 + 操作栏：平铺式工具栏 -->
    <div
      class="flex items-center gap-3 rounded-xl border border-lf-border-soft bg-lf-surface px-4 py-2.5"
    >
      <NButton
        v-if="toolbarMeta.showBackButton"
        quaternary
        circle
        size="small"
        class="shrink-0 text-lf-text-muted hover:text-lf-text-strong"
        :title="t('workspace.explorer.backToParent')"
        :aria-label="t('workspace.explorer.backToParent')"
        @click="handleNavigateUp"
      >
        <template #icon>
          <NIcon size="16"><IconCarbonArrowUp /></NIcon>
        </template>
      </NButton>
      <div v-if="toolbarMeta.showDivider" class="h-4 border-l border-lf-border-soft" />
      <ResourceBreadcrumb
        class="min-w-0 flex-1"
        :items="workspace.breadcrumbs"
        :project-name="workspace.project?.name ?? ''"
        :epub-directory-active="toolbarMeta.epubDirectoryActive"
        @navigate="handleNavigate"
      />
      <div class="flex shrink-0 items-center gap-1.5">
        <span v-if="toolbarMeta.epubDirectoryActive" class="text-xs text-lf-text-muted">
          {{ workspace.epubDirectoryChapters.length }} {{ t('workspace.epub.chapters') }}
        </span>
        <template v-if="toolbarMeta.showRefreshButton">
          <NButton
            quaternary
            circle
            size="small"
            class="text-lf-text-muted hover:text-lf-text-strong"
            :loading="workspace.loadingResourceTree"
            :title="t('workspace.explorer.refreshDirectory')"
            :aria-label="t('workspace.explorer.refreshDirectory')"
            @click="handleRefreshDirectory"
          >
            <template #icon>
              <NIcon size="16"><IconCarbonRenew /></NIcon>
            </template>
          </NButton>
        </template>
        <NUpload
          v-if="toolbarMeta.showUploadButton"
          multiple
          :show-file-list="false"
          :custom-request="handleUpload"
        >
          <NButton type="primary" size="small" strong :loading="workspace.hasActiveUploads">
            <template #icon>
              <NIcon size="16"><IconCarbonUpload /></NIcon>
            </template>
            {{ t('workspace.resource.actions.upload') }}
          </NButton>
        </NUpload>
      </div>
    </div>

    <!-- 错误提示 -->
    <NAlert v-if="workspace.resourceTreeError" type="error" :bordered="false">
      {{ workspace.resourceTreeError }}
    </NAlert>

    <!-- 拖拽上传覆盖层 -->
    <Transition
      enter-active-class="transition-opacity duration-200"
      leave-active-class="transition-opacity duration-200"
      enter-from-class="opacity-0"
      leave-to-class="opacity-0"
    >
      <div
        v-if="dragOver"
        class="flex items-center justify-center rounded-xl border-2 border-dashed border-brand-500/45 bg-lf-brand-soft/80 py-12 dark:border-brand-500/55 dark:bg-lf-brand-soft/70"
      >
        <div class="text-center">
          <div
            class="mx-auto flex h-12 w-12 items-center justify-center rounded-xl bg-brand-50 text-brand-600 shadow-sm shadow-lf-shadow dark:bg-brand-500/15 dark:text-brand-100"
          >
            <NIcon size="26"><IconCarbonUpload /></NIcon>
          </div>
          <p class="mt-3 text-sm font-medium text-brand-700 dark:text-brand-100">
            {{ t('workspace.explorer.dropToUpload') }}
          </p>
        </div>
      </div>
    </Transition>

    <!-- 加载状态 -->
    <div
      v-if="workspace.loadingResourceTree"
      class="flex items-center justify-center rounded-xl border border-dashed border-lf-border-soft bg-lf-surface-muted/60 px-6 py-12 text-center"
    >
      <div
        class="flex h-12 w-12 items-center justify-center rounded-xl bg-lf-surface-elevated text-brand-600 shadow-sm shadow-lf-shadow dark:text-brand-100"
      >
        <NIcon size="24" class="animate-spin"><IconCarbonCircleDash /></NIcon>
      </div>
    </div>

    <!-- 空状态 -->
    <div
      v-else-if="isEmpty && !dragOver"
      class="rounded-xl border border-dashed border-lf-border-soft bg-lf-surface-muted/60 px-6 py-12"
    >
      <NEmpty :description="t('workspace.explorer.emptyDirectory')">
        <template #extra>
          <div class="flex flex-col items-center gap-3">
            <p class="max-w-md text-center text-xs leading-5 text-lf-text-muted">
              {{ t('workspace.explorer.dropHint') }}
            </p>
            <NUpload multiple :show-file-list="false" :custom-request="handleUpload">
              <NButton type="primary">
                <template #icon>
                  <NIcon><IconCarbonUpload /></NIcon>
                </template>
                {{ t('workspace.resource.actions.uploadFirst') }}
              </NButton>
            </NUpload>
          </div>
        </template>
      </NEmpty>
    </div>

    <!-- 动态视图组件：根据策略模式切换 DirectoryView / EpubDirectoryView -->
    <component
      :is="activeViewComponent"
      v-else
      v-bind="currentViewProps"
      v-on="currentViewEvents"
    />

    <NModal
      v-model:show="uploadPrecheckVisible"
      preset="card"
      :title="t('workspace.uploadPrecheck.modalTitle')"
      :style="{ width: 'min(1120px, calc(100vw - 32px))' }"
      :mask-closable="false"
    >
      <UploadPrecheckPanel
        :items="workspace.pendingUploadItems"
        :loading="uploadConfirming"
        @confirm="confirmPrecheckedUpload"
        @cancel="cancelPrecheckedUpload"
        @update-selected="workspace.setPendingUploadItemSelected"
        @update-strategy="workspace.setPendingUploadItemStrategy"
        @update-all-creatable="workspace.setAllCreatablePendingUploadItemsSelected"
      />
    </NModal>
  </div>
</template>
