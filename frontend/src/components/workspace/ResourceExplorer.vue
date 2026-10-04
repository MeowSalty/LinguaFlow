<script setup lang="ts">
import {
  NAlert,
  NButton,
  NDrawer,
  NDrawerContent,
  NEmpty,
  NIcon,
  useDialog,
  useMessage,
} from 'naive-ui'
import { computed, onScopeDispose, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import { type ApiSchemas } from '@/api/client'
import { isDownloadTranslatedError } from '@/api/projects'
import { storageErrorMessage } from '@/api/storage-errors'
import { captureSession, isSessionCurrent, sessionGeneration } from '@/api/session-context'
import DirectoryView from '@/components/workspace/DirectoryView.vue'
import ResourceBreadcrumb from '@/components/workspace/ResourceBreadcrumb.vue'
import UploadPrecheckPanel from '@/components/workspace/UploadPrecheckPanel.vue'
import { useProjectWorkspaceStore, type PendingUploadItem } from '@/stores/projectWorkspace'
import { isCapabilityBlocked } from '@/utils/secureContext'
import { DRAWER_WIDTH } from '@/components/common/uiConstants'
import SourceUpdateDrawer from '@/components/storage/SourceUpdateDrawer.vue'
import ResourceStorageDrawer from '@/components/storage/ResourceStorageDrawer.vue'
import { storageManifestWriteAllowed } from '@/utils/storage-contract'
import { useConflictHandling } from '@/composables/useConflictHandling'

type Resource = ApiSchemas['Resource']

const props = defineProps<{
  projectId: number
  beforeSavedContent?: () => Promise<boolean>
}>()

const emit = defineEmits<{
  openSegments: [resource: Resource]
}>()

const message = useMessage()
const dialog = useDialog()
const { t } = useI18n()
const workspace = useProjectWorkspaceStore()
const sourceQueue = useConflictHandling()
const sourceFile = ref<File | null>(null)
const sourceState = ref('idle')
const queueActive = ref(false)
const onSourceState = (state: string) => {
  sourceState.value = state
  if (queueActive.value && ['failed', 'cancelled', 'expired'].includes(state))
    sourceQueue.settle('failed')
}
const openQueuedSource = () => {
  if (['previewing', 'submitting', 'tracking', 'unknown'].includes(sourceState.value)) {
    sourceDrawerVisible.value = true
    return
  }
  sourceQueue.openNext()
  if (!sourceQueue.conflictResource.value) return
  queueActive.value = true
  sourceResource.value = sourceQueue.conflictResource.value
  sourceFile.value = sourceQueue.conflictFile.value
  sourceDrawerVisible.value = true
}
const skipQueuedSource = () => {
  sourceQueue.settle('skipped')
  sourceDrawerVisible.value = false
  queueActive.value = false
}

// ── 安全上下文：非 HTTPS 环境下拦截文件上传 ──

/**
 * 若当前处于非安全上下文，弹出简短提示并返回 true（调用方应中止上传）。
 * 完整指引已在启动期 Notification 中给出。
 */
const blockUploadIfInsecure = (): boolean => {
  if (!isCapabilityBlocked('fileUpload')) {
    return false
  }

  message.warning(t('secureContext.uploadBlockedHint'))
  return true
}

const dragOver = ref(false)
const uploadPrecheckVisible = ref(false)
const uploadConfirming = ref(false)
const pendingUploadTaskId = ref<string | null>(null)
let contextRevision = 0
const captureContext = () => {
  const projectId = props.projectId
  const revision = contextRevision
  const session = captureSession()
  return {
    projectId,
    current: () =>
      revision === contextRevision && projectId === props.projectId && isSessionCurrent(session),
  }
}
watch(
  () => [props.projectId, sessionGeneration.value],
  () => {
    contextRevision++
    uploadPrecheckVisible.value = false
    uploadConfirming.value = false
    pendingUploadTaskId.value = null
    sourceDrawerVisible.value = false
    storageDrawerVisible.value = false
    sourceResource.value = null
    storageResource.value = null
    sourceQueue.resetConflictState()
    sourceFile.value = null
    queueActive.value = false
  },
  { flush: 'sync' },
)
onScopeDispose(() => contextRevision++)

// ── 计算属性 ──

const directories = computed(() =>
  workspace.currentDirectoryChildren.filter((child) => child.type === 'directory'),
)

const resourceItems = computed(() =>
  workspace.currentDirectoryChildren.filter((child) => child.type === 'resource'),
)

const isEmpty = computed(
  () => !workspace.loadingResourceTree && workspace.currentDirectoryChildren.length === 0,
)

/** 当前目录中已选中的资源 ID 集合（用于快速查找） */
const selectedIdSet = computed(() => new Set(workspace.selectedResourceIds))

// ── 导航 ──

const handleNavigate = (path: string): void => {
  workspace.navigateTo(path)
}

const handleNavigateUp = (): void => {
  workspace.navigateUp()
}

// ── 工具栏动作 ──

const handleRefreshDirectory = async (): Promise<void> => {
  await workspace.loadResourceTree(props.projectId)
}

// ── 资源操作 ──

const sourceResource = ref<Resource | null>(null)
const sourceDrawerVisible = ref(false)
const storageResource = ref<Resource | null>(null)
const storageDrawerVisible = ref(false)
const manifestWritable = computed(() => storageManifestWriteAllowed(workspace.project))
const openSourceUpdate = (resource: Resource): void => {
  if (['previewing', 'submitting', 'tracking', 'unknown'].includes(sourceState.value)) {
    sourceDrawerVisible.value = true
    return
  }
  queueActive.value = false
  sourceFile.value = null
  sourceResource.value = resource
  sourceDrawerVisible.value = true
}
const openResourceStorage = (resource: Resource): void => {
  storageResource.value = resource
  storageDrawerVisible.value = true
}

const downloadResource = async (resource: Resource): Promise<void> => {
  const context = captureContext()
  try {
    const file = await workspace.downloadResource(context.projectId, resource.id)
    if (!context.current()) return
    const url = URL.createObjectURL(file.blob)
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = file.filename || resource.name
    anchor.click()
    URL.revokeObjectURL(url)
  } catch (error) {
    if (!context.current()) return
    console.error(error)
    message.error(workspace.actionError || t('workspace.messages.downloadFailed'))
  }
}

const downloadResourceResult = async (resource: Resource): Promise<void> => {
  const context = captureContext()
  if (props.beforeSavedContent && !(await props.beforeSavedContent())) return
  if (!context.current()) return
  try {
    const file = await workspace.downloadResourceResult(context.projectId, resource.id)
    if (!context.current()) return
    const url = URL.createObjectURL(file.blob)
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = file.filename || `translated-${resource.name}`
    anchor.click()
    URL.revokeObjectURL(url)
  } catch (error) {
    if (!context.current()) return
    console.error(error)
    // 409 = 无已翻译段落，或译文标签结构预检失败（detail 含缺陷段落编号与原因），
    // 信息量超出瞬时 toast 的可读范围，改用对话框完整展示
    if (isDownloadTranslatedError(error) && error.status === 409) {
      dialog.error({
        title: t('api.errors.downloadTranslatedFailed'),
        content: storageErrorMessage(error),
        positiveText: t('common.close'),
      })
      return
    }
    message.error(workspace.actionError || t('workspace.messages.downloadFailed'))
  }
}

const deleteResource = async (resource: Resource): Promise<void> => {
  const context = captureContext()
  if (!manifestWritable.value) {
    message.warning(t('sourceStorage.maintenanceUnknown'))
    return
  }
  if (props.beforeSavedContent && !(await props.beforeSavedContent())) return
  if (!context.current()) return
  try {
    await workspace.deleteResource(context.projectId, resource.id)
    if (!context.current()) return
    message.success(t('workspace.messages.deleteResourceSuccess'))
    await workspace.loadResourceTree(props.projectId)
  } catch (error) {
    if (!context.current()) return
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

const executeUploadItems = async (items: PendingUploadItem[], taskId: string): Promise<void> => {
  const context = captureContext()
  if (!manifestWritable.value) throw new Error(t('sourceStorage.maintenanceUnknown'))
  const selectedItems = items.filter((item) => item.selected && item.strategy === 'create')
  const updateItems = items.filter((item) => item.selected && item.strategy === 'source_update')
  if (updateItems.some((item) => !item.precheck.existing_resource)) {
    throw new Error(t('sourceStorage.batchChanged'))
  }
  const skippedItems = items.filter((item) => item.strategy === 'skip' || !item.selected)
  workspace.updateUploadTaskStage(taskId, 'uploading')
  const result = await workspace.uploadResources(
    context.projectId,
    selectedItems.map((item) => item.file),
    selectedItems.map((item) => item.path),
    taskId,
    skippedItems,
  )
  if (!context.current()) return
  for (const item of updateItems) {
    const resource = item.precheck.existing_resource
    if (resource) sourceQueue.handleExplorerConflict(resource, item.file)
  }
  await workspace.loadResourceTree(context.projectId)
  if (!context.current()) return
  if (result.summary.failed === result.summary.total && result.summary.total > 0) {
    workspace.updateUploadTaskStage(taskId, 'error', undefined, result.summary)
    message.error(t('workspace.messages.uploadFailed'))
  } else if (
    result.summary.failed > 0 ||
    result.summary.conflicts > 0 ||
    result.summary.skipped > 0
  ) {
    workspace.updateUploadTaskStage(taskId, 'partial', undefined, result.summary)
    message.warning(t('workspace.messages.uploadPartialSuccess', { ...result.summary }))
  } else if (updateItems.length === 0) {
    workspace.updateUploadTaskStage(taskId, 'complete', undefined, result.summary)
    message.success(t('workspace.messages.uploadSuccess'))
  }
  if (updateItems.length > 0) {
    workspace.updateUploadTaskStage(taskId, 'partial', t('sourceStorage.queueOpen'), result.summary)
    openQueuedSource()
  }
}

/** 打开文件选择器，多选文件作为一个批次上传（与拖拽上传共用同一批处理流程） */
const chooseUploadFiles = (): void => {
  const context = captureContext()
  if (!manifestWritable.value) {
    message.warning(t('sourceStorage.maintenanceUnknown'))
    return
  }
  if (blockUploadIfInsecure()) return
  const input = document.createElement('input')
  input.type = 'file'
  input.multiple = true
  input.onchange = () => {
    if (!context.current()) return
    const files = Array.from(input.files ?? [])
    if (files.length === 0) return
    const paths = computeUploadPaths(files, workspace.currentPath)
    void beginUpload(files, paths, summarizeUploadName(files))
  }
  input.click()
}

const beginUpload = async (
  files: File[],
  paths: string[] | undefined,
  displayName: string,
): Promise<void> => {
  const context = captureContext()
  if (blockUploadIfInsecure()) {
    return
  }

  if (!manifestWritable.value) {
    message.warning(t('sourceStorage.maintenanceUnknown'))
    return
  }
  if (files.length === 0) {
    return
  }

  const taskId = workspace.addUploadTask(displayName, files.length)
  workspace.updateUploadTaskStage(taskId, 'prechecking')

  try {
    const items = await workspace.precheckUploadResources(context.projectId, files, paths)
    if (!context.current()) return
    workspace.setPendingUploadItems(items)

    if (items.some((item) => item.precheck.action !== 'create')) {
      pendingUploadTaskId.value = taskId
      uploadPrecheckVisible.value = true
      return
    }

    await executeUploadItems(items, taskId)
  } catch (error) {
    if (!context.current()) return
    console.error(error)
    message.error(workspace.actionError || t('workspace.messages.uploadFailed'))
    workspace.updateUploadTaskStage(
      taskId,
      'error',
      workspace.actionError || t('workspace.messages.uploadFailed'),
    )
  }
}

const confirmPrecheckedUpload = async (): Promise<void> => {
  if (uploadConfirming.value) return
  const context = captureContext()
  const taskId = pendingUploadTaskId.value
  if (!taskId) {
    return
  }

  uploadConfirming.value = true
  try {
    await executeUploadItems(workspace.pendingUploadItems, taskId)
    if (!context.current()) return
    uploadPrecheckVisible.value = false
    pendingUploadTaskId.value = null
    workspace.clearPendingUploadItems()
  } catch (error) {
    if (!context.current()) return
    console.error(error)
    message.error(workspace.actionError || t('workspace.messages.uploadFailed'))
  } finally {
    if (context.current()) uploadConfirming.value = false
  }
}

const cancelPrecheckedUpload = (): void => {
  if (uploadConfirming.value) return
  if (pendingUploadTaskId.value) {
    workspace.removeUploadTask(pendingUploadTaskId.value)
  }
  pendingUploadTaskId.value = null
  uploadPrecheckVisible.value = false
  workspace.clearPendingUploadItems()
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
  const context = captureContext()
  const currentPrefix = workspace.currentPath
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
  if (!context.current()) return

  if (collectedFiles.length === 0) {
    return
  }

  const files = collectedFiles.map((item) => item.file)
  const paths = collectedFiles.map((item) =>
    currentPrefix ? `${currentPrefix}/${item.relativePath}` : item.relativePath,
  )

  await beginUpload(files, paths, summarizeUploadName(files))
}
</script>

<template>
  <div class="space-y-3" @dragover="handleDragOver" @dragleave="handleDragLeave" @drop="handleDrop">
    <div
      class="flex flex-wrap items-center gap-2.5 rounded-lf-card border border-lf-border-soft bg-lf-surface-muted/50 px-3 py-2"
    >
      <NButton
        v-if="workspace.currentPath"
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
      <div v-if="workspace.currentPath" class="h-4 border-l border-lf-border-soft" />
      <ResourceBreadcrumb
        class="min-w-0 flex-1"
        :items="workspace.breadcrumbs"
        :project-name="workspace.project?.name ?? ''"
        @navigate="handleNavigate"
      />
      <div class="flex shrink-0 items-center gap-1.5">
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
        <NButton
          type="primary"
          size="small"
          strong
          :loading="workspace.hasActiveUploads"
          :disabled="!manifestWritable"
          @click="chooseUploadFiles"
        >
          <template #icon>
            <NIcon size="16"><IconCarbonUpload /></NIcon>
          </template>
          {{ t('workspace.resource.actions.upload') }}
        </NButton>
      </div>
    </div>

    <NAlert v-if="!manifestWritable" type="info" :bordered="false">{{
      t('sourceStorage.maintenanceUnknown')
    }}</NAlert>
    <SourceUpdateDrawer
      v-if="sourceResource"
      v-model:show="sourceDrawerVisible"
      :project-id="projectId"
      :resource="sourceResource"
      :file="sourceFile"
      :before-saved-content="beforeSavedContent"
      @state="onSourceState"
      @completed="queueActive && sourceQueue.settle('completed')"
    />
    <NAlert v-if="sourceQueue.queue.value.length" type="info" :bordered="false">
      {{ t('sourceStorage.queue', sourceQueue.summary.value) }}
      <div class="mt-2 flex gap-2">
        <NButton
          v-if="sourceQueue.summary.value.pending > 0"
          :disabled="sourceDrawerVisible"
          @click="openQueuedSource"
          >{{ t('sourceStorage.queueOpen') }}</NButton
        >
        <NButton
          v-if="
            queueActive &&
            !['previewing', 'submitting', 'tracking', 'unknown', 'completed'].includes(sourceState)
          "
          @click="skipQueuedSource"
          >{{ t('sourceStorage.queueSkip') }}</NButton
        >
      </div>
    </NAlert>
    <ResourceStorageDrawer
      v-if="storageResource"
      v-model:show="storageDrawerVisible"
      :project-id="projectId"
      :project="workspace.project"
      :resource="storageResource"
      :before-saved-content="beforeSavedContent"
      @changed="handleRefreshDirectory"
    />
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
        class="flex items-center justify-center rounded-lf-card border-2 border-dashed border-brand-500/45 bg-lf-brand-soft/80 py-8"
      >
        <div class="text-center">
          <div
            class="mx-auto flex h-12 w-12 items-center justify-center rounded-lf-ctl bg-brand-50 text-brand-600 shadow-sm shadow-lf-shadow"
          >
            <NIcon size="26"><IconCarbonUpload /></NIcon>
          </div>
          <p class="mt-3 text-sm font-medium text-brand-700">
            {{ t('workspace.explorer.dropToUpload') }}
          </p>
        </div>
      </div>
    </Transition>

    <!-- 加载状态 -->
    <div
      v-if="workspace.loadingResourceTree"
      class="flex items-center justify-center rounded-lf-card border border-dashed border-lf-border-soft bg-lf-surface-muted/60 px-6 py-8 text-center"
    >
      <div
        class="flex h-12 w-12 items-center justify-center rounded-lf-ctl bg-lf-surface-elevated text-brand-600 shadow-sm shadow-lf-shadow"
      >
        <NIcon size="24" class="animate-spin"><IconCarbonCircleDash /></NIcon>
      </div>
    </div>

    <!-- 空状态 -->
    <div
      v-else-if="isEmpty && !dragOver"
      class="rounded-lf-card border border-dashed border-lf-border-soft bg-lf-surface-muted/60 px-6 py-8"
    >
      <NEmpty :description="t('workspace.explorer.emptyDirectory')">
        <template #extra>
          <div class="flex flex-col items-center gap-3">
            <p class="max-w-md text-center text-xs leading-5 text-lf-text-subtle">
              {{ t('workspace.explorer.dropHint') }}
            </p>
            <NButton type="primary" :disabled="!manifestWritable" @click="chooseUploadFiles">
              <template #icon>
                <NIcon><IconCarbonUpload /></NIcon>
              </template>
              {{ t('workspace.resource.actions.uploadFirst') }}
            </NButton>
          </div>
        </template>
      </NEmpty>
    </div>

    <!-- 资源目录视图 -->
    <DirectoryView
      v-else
      :directories="directories"
      :resource-items="resourceItems"
      :selected-id-set="selectedIdSet"
      :replacing-resource-ids="workspace.replacingResourceIds"
      :incremental-updating-ids="workspace.incrementalUpdatingIds"
      :downloading-keys="workspace.downloadingKeys"
      :deleting-resource-ids="workspace.deletingResourceIds"
      @navigate="handleNavigate"
      @open-segments="(r) => emit('openSegments', r)"
      :manifest-writable="manifestWritable"
      @source-update="openSourceUpdate"
      @storage="openResourceStorage"
      @download="(r) => void downloadResource(r)"
      @download-translated="(r) => void downloadResourceResult(r)"
      @delete="(r) => void deleteResource(r)"
      @toggle-select="(r) => workspace.toggleResourceSelection(r.id)"
      @set-selection="(ids, selected) => workspace.setResourceSelection(ids, selected)"
    />

    <NDrawer
      v-model:show="uploadPrecheckVisible"
      :width="DRAWER_WIDTH.xl"
      placement="right"
      :mask-closable="false"
      :close-on-esc="!uploadConfirming"
    >
      <NDrawerContent :native-scrollbar="false">
        <template #header>
          <DrawerHeader
            :title="t('workspace.uploadPrecheck.drawerTitle')"
            :subtitle="t('workspace.uploadPrecheck.drawerSubtitle')"
          />
        </template>
        <UploadPrecheckPanel
          :items="workspace.pendingUploadItems"
          :loading="uploadConfirming"
          @confirm="confirmPrecheckedUpload"
          @cancel="cancelPrecheckedUpload"
          @update-selected="workspace.setPendingUploadItemSelected"
          @update-strategy="workspace.setPendingUploadItemStrategy"
          @update-all-creatable="workspace.setAllCreatablePendingUploadItemsSelected"
        />
      </NDrawerContent>
    </NDrawer>
  </div>
</template>
