<script setup lang="ts">
import {
  NAlert,
  NButton,
  NEmpty,
  NIcon,
  NUpload,
  useMessage,
  type UploadCustomRequestOptions,
} from 'naive-ui'
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import { isResourceConflictError, type ApiSchemas } from '@/api/client'
import DirectoryItem from '@/components/workspace/DirectoryItem.vue'
import ResourceBreadcrumb from '@/components/workspace/ResourceBreadcrumb.vue'
import ResourceItem from '@/components/workspace/ResourceItem.vue'
import { useProjectWorkspaceStore } from '@/stores/projectWorkspace'

type Resource = ApiSchemas['Resource']
type IncrementalUpdateResponse = ApiSchemas['IncrementalUpdateResponse']

const props = defineProps<{
  projectId: number
}>()

const emit = defineEmits<{
  openSegments: [resource: Resource]
  conflict: [resource: Resource, file: File]
  incrementalResult: [result: IncrementalUpdateResponse]
}>()

const message = useMessage()
const { t } = useI18n()
const workspace = useProjectWorkspaceStore()

const dragOver = ref(false)

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
    workspace.currentDirectoryChildren.length === 0,
)

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
    const relativePath = (file as File & { webkitRelativePath?: string }).webkitRelativePath || file.name
    return directoryPrefix ? `${directoryPrefix}/${relativePath}` : relativePath
  })
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
  const taskId = workspace.addUploadTask(file.name)

  try {
    await workspace.uploadResources(props.projectId, files, paths, taskId)
    message.success(t('workspace.messages.uploadSuccess'))
    onFinish()
    workspace.updateUploadTaskStage(taskId, 'complete', undefined)
    setTimeout(() => workspace.removeUploadTask(taskId), 3000)
    await workspace.loadResourceTree(props.projectId)
  } catch (error) {
    if (isResourceConflictError(error)) {
      workspace.removeUploadTask(taskId)
      emit('conflict', error.conflictData.existing_resource, file.file)
      onFinish()
    } else {
      console.error(error)
      message.error(workspace.actionError || t('workspace.messages.uploadFailed'))
      onError()
    }
  }
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

  const taskId = workspace.addUploadTask(
    collectedFiles.length === 1 ? collectedFiles[0]!.file.name : `${collectedFiles.length} files`,
  )

  try {
    await workspace.uploadResources(props.projectId, files, paths, taskId)
    message.success(t('workspace.messages.uploadSuccess'))
    workspace.updateUploadTaskStage(taskId, 'complete', undefined)
    setTimeout(() => workspace.removeUploadTask(taskId), 3000)
    await workspace.loadResourceTree(props.projectId)
  } catch (error) {
    if (isResourceConflictError(error)) {
      workspace.removeUploadTask(taskId)
      emit('conflict', error.conflictData.existing_resource, files[0]!)
    } else {
      console.error(error)
      message.error(workspace.actionError || t('workspace.messages.uploadFailed'))
      workspace.updateUploadTaskStage(taskId, 'error', workspace.actionError || t('workspace.messages.uploadFailed'))
    }
  }
}
</script>

<template>
  <div
    class="space-y-4"
    @dragover="handleDragOver"
    @dragleave="handleDragLeave"
    @drop="handleDrop"
  >
    <!-- 面包屑 + 操作栏 -->
    <div class="flex flex-col gap-3 xl:flex-row xl:items-center xl:justify-between">
      <ResourceBreadcrumb
        :items="workspace.breadcrumbs"
        :project-name="workspace.project?.name ?? ''"
        @navigate="handleNavigate"
      />
      <div class="flex flex-wrap items-center gap-2">
        <NButton
          v-if="workspace.currentPath"
          quaternary
          size="small"
          @click="workspace.navigateUp()"
        >
          <template #icon>
            <NIcon><IconLucideArrowLeft /></NIcon>
          </template>
          {{ t('workspace.explorer.backToParent') }}
        </NButton>
        <NButton
          secondary
          size="small"
          :loading="workspace.loadingResourceTree"
          @click="workspace.loadResourceTree(props.projectId)"
        >
          {{ t('workspace.actions.refresh') }}
        </NButton>
        <NUpload multiple :show-file-list="false" :custom-request="handleUpload">
          <NButton type="primary" size="small" :loading="workspace.hasActiveUploads">
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
        class="flex items-center justify-center rounded-xl border-2 border-dashed border-blue-400 bg-blue-50/50 py-12 dark:bg-blue-500/5"
      >
        <div class="text-center">
          <NIcon size="32" class="text-blue-400"><IconLucideUpload /></NIcon>
          <p class="mt-2 text-sm text-blue-500">{{ t('workspace.explorer.dropToUpload') }}</p>
        </div>
      </div>
    </Transition>

    <!-- 上传进度卡片 -->
    <TransitionGroup
      enter-active-class="transition-all duration-300 ease-out"
      leave-active-class="transition-all duration-200 ease-in"
      enter-from-class="opacity-0 -translate-y-2"
      leave-to-class="opacity-0 -translate-y-2"
      move-class="transition-transform duration-200"
      tag="div"
      class="space-y-2"
    >
      <div
        v-for="task in workspace.uploadTasks"
        :key="task.id"
        class="overflow-hidden rounded-lg border border-lf-border bg-lf-surface-muted px-4 py-3"
      >
        <div class="flex items-center justify-between">
          <div class="flex min-w-0 flex-1 items-center gap-2.5">
            <div
              class="flex h-7 w-7 shrink-0 items-center justify-center rounded-md"
              :class="{
                'bg-blue-50 text-blue-500 dark:bg-blue-500/10': task.stage === 'uploading',
                'bg-amber-50 text-amber-500 dark:bg-amber-500/10': task.stage === 'processing',
                'bg-emerald-50 text-emerald-500 dark:bg-emerald-500/10': task.stage === 'complete',
                'bg-red-50 text-red-500 dark:bg-red-500/10': task.stage === 'error',
              }"
            >
              <IconLucideUpload v-if="task.stage === 'uploading'" class="h-3.5 w-3.5" />
              <IconLucideLoader2
                v-else-if="task.stage === 'processing'"
                class="h-3.5 w-3.5 animate-spin"
              />
              <IconLucideCheck v-else-if="task.stage === 'complete'" class="h-3.5 w-3.5" />
              <IconLucideAlertCircle v-else class="h-3.5 w-3.5" />
            </div>
            <div class="min-w-0 flex-1">
              <span class="truncate text-sm font-medium text-lf-text-strong">
                {{ task.fileName }}
              </span>
              <span class="ml-2 shrink-0 text-xs text-lf-text-muted">
                <template v-if="task.stage === 'uploading'">
                  {{ t('workspace.upload.uploadingPercent', { percent: task.progress }) }}
                </template>
                <template v-else-if="task.stage === 'processing'">
                  {{ t('workspace.upload.processing') }}
                </template>
                <template v-else-if="task.stage === 'complete'">
                  {{ t('workspace.upload.complete') }}
                </template>
                <template v-else>
                  {{ task.errorMessage || t('workspace.upload.failed') }}
                </template>
              </span>
            </div>
          </div>
          <NButton
            quaternary
            size="tiny"
            class="ml-2 shrink-0"
            @click="workspace.removeUploadTask(task.id)"
          >
            <template #icon>
              <NIcon><IconLucideX /></NIcon>
            </template>
          </NButton>
        </div>
      </div>
    </TransitionGroup>

    <!-- 加载状态 -->
    <div v-if="workspace.loadingResourceTree" class="py-12 text-center">
      <NIcon size="24" class="animate-spin text-lf-text-muted"><IconLucideLoader2 /></NIcon>
    </div>

    <!-- 空状态 -->
    <NEmpty
      v-else-if="isEmpty"
      class="py-12"
      :description="t('workspace.explorer.emptyDirectory')"
    >
      <template #extra>
        <NUpload multiple :show-file-list="false" :custom-request="handleUpload">
          <NButton type="primary">{{ t('workspace.resource.actions.uploadFirst') }}</NButton>
        </NUpload>
      </template>
    </NEmpty>

    <!-- 目录 + 资源列表 -->
    <template v-else>
      <!-- 目录列表 -->
      <div v-if="directories.length > 0" class="space-y-1.5">
        <DirectoryItem
          v-for="dir in directories"
          :key="dir.path"
          :name="dir.name"
          :path="dir.path"
          :child-count="dir.childCount ?? 0"
          @open="handleNavigate"
        />
      </div>

      <!-- 分隔线 -->
      <div
        v-if="directories.length > 0 && resourceItems.length > 0"
        class="border-t border-lf-border"
      />

      <!-- 资源列表 -->
      <div v-if="resourceItems.length > 0" class="space-y-1.5">
        <ResourceItem
          v-for="item in resourceItems"
          :key="item.path"
          :resource="item.resource!"
          :replacing="workspace.replacingResourceIds.includes(item.resource!.id)"
          :incremental-updating="workspace.incrementalUpdatingIds.includes(item.resource!.id)"
          :downloading="workspace.downloadingKeys.includes(`resource:${item.resource!.id}`)"
          :deleting="workspace.deletingResourceIds.includes(item.resource!.id)"
          @open-segments="(r) => emit('openSegments', r)"
          @replace="(r) => chooseReplacementFile(r.id)"
          @incremental-update="(r) => chooseIncrementalUpdateFile(r.id)"
          @download="(r) => void downloadResource(r)"
          @delete="(r) => void deleteResource(r)"
        />
      </div>
    </template>
  </div>
</template>
