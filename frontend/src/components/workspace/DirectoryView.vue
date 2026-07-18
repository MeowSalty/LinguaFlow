<script setup lang="ts">
import { NCheckbox } from 'naive-ui'
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'

import type { ApiSchemas } from '@/api/client'
import DirectoryItem from '@/components/workspace/DirectoryItem.vue'
import ResourceItem from '@/components/workspace/ResourceItem.vue'
import type { DirectoryChild } from '@/stores/projectWorkspace'

type Resource = ApiSchemas['Resource']

const props = defineProps<{
  /** 当前目录的子目录列表 */
  directories: DirectoryChild[]
  /** 当前目录的资源列表 */
  resourceItems: DirectoryChild[]
  /** 已选中资源 ID 集合 */
  selectedIdSet: Set<number>
  /** 替换中的资源 ID 列表 */
  replacingResourceIds: number[]
  /** 增量更新中的资源 ID 列表 */
  incrementalUpdatingIds: number[]
  /** 下载中的 key 列表 */
  downloadingKeys: string[]
  /** 删除中的资源 ID 列表 */
  deletingResourceIds: number[]
}>()

const emit = defineEmits<{
  /** 进入子目录 */
  navigate: [path: string]
  /** 打开资源段落（非 EPUB 资源直接跳转段落编辑） */
  openSegments: [resource: Resource]
  /** 进入 EPUB 虚拟目录 */
  openEpubDirectory: [resource: Resource]
  /** 替换资源 */
  replace: [resource: Resource]
  /** 增量更新资源 */
  incrementalUpdate: [resource: Resource]
  /** 下载资源 */
  download: [resource: Resource]
  /** 下载翻译后的资源 */
  downloadTranslated: [resource: Resource]
  /** 删除资源 */
  delete: [resource: Resource]
  /** 切换资源选中状态 */
  toggleSelect: [resource: Resource]
  /** 设置一组资源的选中状态 */
  setSelection: [resourceIds: number[], selected: boolean]
}>()

const { t } = useI18n()

// ── 资源多选 ──

/** 当前目录直接资源及全部子目录后代资源 ID */
const currentDirectoryResourceIds = computed(() => [
  ...new Set([
    ...props.resourceItems.map((item) => item.resource!.id),
    ...props.directories.flatMap((directory) => directory.descendantResourceIds ?? []),
  ]),
])

/** 当前目录资源是否全选 */
const isCurrentDirAllSelected = computed(
  () =>
    currentDirectoryResourceIds.value.length > 0 &&
    currentDirectoryResourceIds.value.every((id) => props.selectedIdSet.has(id)),
)

/** 当前目录是否有部分选中 */
const isCurrentDirIndeterminate = computed(
  () =>
    !isCurrentDirAllSelected.value &&
    currentDirectoryResourceIds.value.some((id) => props.selectedIdSet.has(id)),
)

const isDirectorySelected = (directory: DirectoryChild): boolean => {
  const ids = directory.descendantResourceIds ?? []
  return ids.length > 0 && ids.every((id) => props.selectedIdSet.has(id))
}

const isDirectoryIndeterminate = (directory: DirectoryChild): boolean => {
  const ids = directory.descendantResourceIds ?? []
  return !isDirectorySelected(directory) && ids.some((id) => props.selectedIdSet.has(id))
}

const handleCurrentDirectorySelection = (selected: boolean): void => {
  emit('setSelection', currentDirectoryResourceIds.value, selected)
}

const handleDirectorySelection = (directory: DirectoryChild, selected: boolean): void => {
  emit('setSelection', directory.descendantResourceIds ?? [], selected)
}

const currentDirectorySelectionAriaLabel = computed(() =>
  isCurrentDirAllSelected.value
    ? t('workspace.explorer.deselectCurrentDirectoryResources')
    : t('workspace.explorer.selectCurrentDirectoryResources'),
)
</script>

<template>
  <!-- 表头行 -->
  <div
    v-if="directories.length > 0 || resourceItems.length > 0"
    class="flex items-center gap-2.5 border-b border-lf-border-soft px-3 py-1.5 text-[11px] font-medium tracking-wide text-lf-text-subtle uppercase"
  >
    <NCheckbox
      :checked="isCurrentDirAllSelected"
      :indeterminate="isCurrentDirIndeterminate"
      :disabled="currentDirectoryResourceIds.length === 0"
      :aria-label="currentDirectorySelectionAriaLabel"
      class="shrink-0"
      @update:checked="handleCurrentDirectorySelection"
    />
    <div class="w-7 shrink-0" />
    <!-- 图标占位 -->
    <span class="flex-1">{{ t('workspace.explorer.headerName') }}</span>
    <span class="w-16 text-right">{{ t('workspace.explorer.headerSegments') }}</span>
    <span class="w-20 text-right">{{ t('workspace.explorer.headerProgress') }}</span>
    <div class="w-14" />
    <!-- 操作占位 -->
  </div>

  <!-- 目录列表 -->
  <div v-if="directories.length > 0" class="space-y-1">
    <DirectoryItem
      v-for="dir in directories"
      :key="dir.path"
      :name="dir.name"
      :path="dir.path"
      :child-count="dir.childCount ?? 0"
      :checked="isDirectorySelected(dir)"
      :indeterminate="isDirectoryIndeterminate(dir)"
      :disabled="(dir.descendantResourceIds?.length ?? 0) === 0"
      @open="emit('navigate', $event)"
      @selection="handleDirectorySelection(dir, $event)"
    />
  </div>

  <!-- 资源列表 -->
  <div v-if="resourceItems.length > 0" class="space-y-1">
    <ResourceItem
      v-for="item in resourceItems"
      :key="item.path"
      :resource="item.resource!"
      :replacing="replacingResourceIds.includes(item.resource!.id)"
      :incremental-updating="incrementalUpdatingIds.includes(item.resource!.id)"
      :downloading="downloadingKeys.includes(`resource:${item.resource!.id}`)"
      :downloading-translated="downloadingKeys.includes(`resource:${item.resource!.id}:translated`)"
      :deleting="deletingResourceIds.includes(item.resource!.id)"
      :selected="selectedIdSet.has(item.resource!.id)"
      @open="(r) => emit('openEpubDirectory', r)"
      @open-segments="(r) => emit('openSegments', r)"
      @replace="(r) => emit('replace', r)"
      @incremental-update="(r) => emit('incrementalUpdate', r)"
      @download="(r) => emit('download', r)"
      @download-translated="(r) => emit('downloadTranslated', r)"
      @delete="(r) => emit('delete', r)"
      @toggle-select="(r) => emit('toggleSelect', r)"
    />
  </div>
</template>
