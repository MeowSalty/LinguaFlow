<script setup lang="ts">
import { NAlert, NButton, NEmpty, NInput, NSelect } from 'naive-ui'
import { computed, ref, toRef } from 'vue'
import { useI18n } from 'vue-i18n'

import type { ApiSchemas } from '@/api/client'
import { useSegmentEditing } from '@/composables/useSegmentEditing'
import { useProjectWorkspaceStore } from '@/stores/projectWorkspace'
import SegmentDataTable from '@/components/workspace/SegmentDataTable.vue'

type Segment = ApiSchemas['Segment']

const { t } = useI18n()
const workspace = useProjectWorkspaceStore()

const props = defineProps<{
  projectId: number | null
}>()

const emit = defineEmits<{
  translate: [segment?: Segment]
  refresh: []
  translateByGroupKey: [groupKey: string]
  selectionChange: [segmentIds: number[]]
  translateBatch: [segmentIds: number[]]
}>()

const projectIdRef = toRef(props, 'projectId')
const activeResourceIdRef = toRef(workspace, 'activeResourceId')

const {
  segmentStatusOptions,
  inlineEditingSegmentId,
  inlineEditForm,
  inlineCommentVisible,
  inlineCommentText,
  startInlineEdit,
  cancelInlineEdit,
  saveInlineEdit,
  saveAndEditNext,
  openInlineComment,
  saveInlineComment,
} = useSegmentEditing(projectIdRef, activeResourceIdRef)

// ── 文本渲染模式 ──
const textRenderMode = computed<'plaintext' | 'html'>(() =>
  workspace.isEpubResource ? 'html' : 'plaintext',
)

// ── 批量选择 ──
const segmentDataTableRef = ref<InstanceType<typeof SegmentDataTable> | null>(null)
const selectedSegmentIds = ref<number[]>([])

const clearSelectedSegments = (): void => {
  segmentDataTableRef.value?.clearSelection()
  selectedSegmentIds.value = []
}

// ── 暴露给父组件，供浮动操作岛使用 ──
defineExpose({
  selectedSegmentIds,
  clearSelectedSegments,
})

// ── 章节选择器 ──
// 使用 computed 从 epubActiveGroupKey 派生，避免 watcher 竞争导致值同步 bug
const chapterSelectValue = computed<string | null>(() => workspace.epubActiveGroupKey ?? '__all__')

const chapterOptions = computed(() => {
  const allOption = {
    label: t('workspace.segment.chapterAll'),
    value: '__all__',
  }
  const groupOptions = workspace.segmentGroups.map((group) => ({
    label: group.group_title,
    value: group.group_key,
  }))
  return [allOption, ...groupOptions]
})

// ── 章节切换处理 ──
const handleChapterChange = (value: string): void => {
  if (!props.projectId || !workspace.activeResourceId) return

  if (value === '__all__') {
    workspace.exitChapter()
    void workspace.loadSegments(props.projectId, workspace.activeResourceId)
  } else {
    const group = workspace.segmentGroups.find((g) => g.group_key === value)
    workspace.enterChapter(value, group?.group_title ?? value)
    void workspace.loadSegments(props.projectId, workspace.activeResourceId, false, value)
  }
}

// ── 资源切换联动 ──
const handleResourceChange = (value: number | null): void => {
  workspace.setActiveResource(value)
  workspace.exitChapter()

  if (value && workspace.isEpubResource) {
    void workspace.loadEpubData(props.projectId!, value)
    // EPUB 资源选中后默认加载全部段落（"全部章节"视图）
    void workspace.loadSegments(props.projectId!, value)
  }
}

// ── 刷新按钮处理 ──
const handleRefresh = (): void => {
  if (!props.projectId || !workspace.activeResourceId) return

  if (workspace.isEpubResource && workspace.epubActiveGroupKey) {
    void workspace.loadSegments(
      props.projectId,
      workspace.activeResourceId,
      false,
      workspace.epubActiveGroupKey,
    )
  } else {
    emit('refresh')
  }
}

// ── 加载更多 ──
const handleLoadMore = (): void => {
  void workspace.loadSegments(
    props.projectId!,
    workspace.activeResourceId!,
    true,
    workspace.epubActiveGroupKey ?? undefined,
  )
}

// ── 事件转发处理 ──
const handleSelectionChange = (ids: number[]): void => {
  selectedSegmentIds.value = ids
  emit('selectionChange', ids)
}

const handleTranslate = (segment: Segment): void => {
  emit('translate', segment)
}

const handleSaveAndEditNext = (segment: Segment): void => {
  void saveAndEditNext(segment, workspace.segments)
}

const handleUpdateInlineEditForm = (
  field: 'source_text' | 'target_text' | 'comment',
  value: string,
): void => {
  inlineEditForm[field] = value
}

const handleUpdateInlineCommentText = (value: string): void => {
  inlineCommentText.value = value
}

const handleCloseInlineComment = (): void => {
  inlineCommentVisible.value = null
}
</script>

<template>
  <div class="space-y-3">
    <div
      class="flex flex-col gap-2.5 rounded-xl border border-lf-border-soft bg-lf-surface-muted/50 px-3 py-2.5 xl:flex-row xl:items-center xl:justify-between"
    >
      <div class="flex min-w-0 flex-1 flex-col gap-2 md:flex-row md:items-center">
        <NSelect
          v-model:value="workspace.activeResourceId"
          clearable
          size="small"
          class="md:max-w-xs"
          :options="
            workspace.resources.map((resource) => ({
              label: resource.path,
              value: resource.id,
            }))
          "
          :placeholder="t('workspace.segment.resourcePlaceholder')"
          @update:value="handleResourceChange"
        />
        <NSelect
          v-if="workspace.isEpubResource"
          :value="chapterSelectValue"
          size="small"
          class="md:max-w-xs"
          :options="chapterOptions"
          :loading="workspace.loadingSegmentGroups"
          :placeholder="t('workspace.segment.chapterPlaceholder')"
          @update:value="handleChapterChange"
        />
        <NInput
          v-model:value="workspace.segmentSearch"
          clearable
          size="small"
          class="md:max-w-xs"
          :disabled="!workspace.activeResourceId"
          :placeholder="t('workspace.segment.searchPlaceholder')"
        />
        <NSelect
          v-model:value="workspace.segmentStatusFilter"
          size="small"
          class="md:w-36"
          :disabled="!workspace.activeResourceId"
          :options="segmentStatusOptions"
        />
      </div>
      <div class="flex shrink-0 items-center gap-2">
        <NButton
          secondary
          size="small"
          :disabled="!workspace.activeResourceId"
          :loading="workspace.loadingSegments"
          @click="handleRefresh"
        >
          {{ t('workspace.actions.refresh') }}
        </NButton>
      </div>
    </div>

    <NAlert v-if="workspace.segmentsError" type="error" :bordered="false">
      {{ workspace.segmentsError }}
    </NAlert>

    <NEmpty
      v-if="!workspace.activeResourceId"
      class="py-10"
      :description="t('workspace.segment.noResource')"
    />

    <div v-else class="lf-table overflow-hidden rounded-xl border border-lf-border-soft">
      <SegmentDataTable
        ref="segmentDataTableRef"
        :segments="workspace.segments"
        :loading="workspace.loadingSegments"
        :has-more="workspace.segmentsCursor !== null"
        :text-render-mode="textRenderMode"
        :show-updated-at="true"
        :show-mobile-cards="true"
        :show-selection="true"
        :show-comment="true"
        :editing-segment-ids="workspace.editingSegmentIds"
        :inline-editing-segment-id="inlineEditingSegmentId"
        :inline-edit-form="inlineEditForm"
        :inline-comment-visible="inlineCommentVisible"
        :inline-comment-text="inlineCommentText"
        @selection-change="handleSelectionChange"
        @translate="handleTranslate"
        @load-more="handleLoadMore"
        @start-inline-edit="startInlineEdit"
        @cancel-inline-edit="cancelInlineEdit"
        @save-inline-edit="saveInlineEdit"
        @save-and-edit-next="handleSaveAndEditNext"
        @open-inline-comment="openInlineComment"
        @save-inline-comment="saveInlineComment"
        @close-inline-comment="handleCloseInlineComment"
        @update:inline-comment-text="handleUpdateInlineCommentText"
        @update:inline-edit-form="handleUpdateInlineEditForm"
      />
    </div>
  </div>
</template>
