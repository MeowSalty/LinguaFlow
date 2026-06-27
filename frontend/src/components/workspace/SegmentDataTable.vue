<script setup lang="ts">
import type { DataTableRowKey } from 'naive-ui'
import { NButton, NDataTable, NEmpty, NSpin } from 'naive-ui'
import { ref, toRef, computed } from 'vue'

import type { ApiSchemas } from '@/api/client'
import type { SegmentFormModel } from '@/composables/useSegmentEditing'
import type { SegmentTableConfig, SegmentColumnDeps } from '@/composables/segmentColumns'
import { useSegmentColumns } from '@/composables/segmentColumns'
import SegmentMobileCard from '@/components/workspace/SegmentMobileCard.vue'
import { t } from '@/i18n'

type Segment = ApiSchemas['Segment']

// ── Props ──
const props = defineProps<{
  segments: Segment[]
  loading: boolean
  hasMore: boolean
  textRenderMode: 'plaintext' | 'html'
  showUpdatedAt: boolean
  showMobileCards: boolean
  showSelection: boolean
  showComment: boolean
  editingSegmentIds: number[]
  inlineEditingSegmentId: number | null
  inlineEditForm: SegmentFormModel
  inlineCommentVisible: number | null
  inlineCommentText: string
}>()

// ── Emits ──
const emit = defineEmits<{
  selectionChange: [segmentIds: number[]]
  translate: [segment: Segment]
  loadMore: []
  startInlineEdit: [segment: Segment]
  cancelInlineEdit: []
  saveInlineEdit: [segment: Segment]
  openInlineComment: [segment: Segment]
  saveInlineComment: [segment: Segment]
  closeInlineComment: []
  'update:inlineCommentText': [value: string]
  'update:inlineEditForm': [field: 'source_text' | 'target_text', value: string]
}>()

// ── 响应式配置 ──
const config = computed<SegmentTableConfig>(() => ({
  textRenderMode: props.textRenderMode,
  showUpdatedAt: props.showUpdatedAt,
  showComment: props.showComment,
  showSelection: props.showSelection,
}))

const configRef = toRef(config)

// ── 依赖注入（委托 emit） ──
const deps: SegmentColumnDeps = {
  inlineEditingSegmentId: toRef(props, 'inlineEditingSegmentId'),
  inlineEditForm: props.inlineEditForm,
  inlineCommentVisible: toRef(props, 'inlineCommentVisible'),
  inlineCommentText: toRef(props, 'inlineCommentText'),
  editingSegmentIds: toRef(props, 'editingSegmentIds'),

  startInlineEdit: (segment) => emit('startInlineEdit', segment),
  cancelInlineEdit: () => emit('cancelInlineEdit'),
  saveInlineEdit: (segment) => {
    emit('saveInlineEdit', segment)
    return Promise.resolve()
  },
  openInlineComment: (segment) => emit('openInlineComment', segment),
  saveInlineComment: (segment) => {
    emit('saveInlineComment', segment)
    return Promise.resolve()
  },
  closeInlineComment: () => emit('closeInlineComment'),
  updateCommentText: (value) => emit('update:inlineCommentText', value),
  updateEditFormField: (field, value) => emit('update:inlineEditForm', field, value),
  onTranslate: (segment) => emit('translate', segment),
}

// ── 列定义 ──
const columns = useSegmentColumns(configRef, deps)

// ── 行选择 ──
const selectedSegmentIds = ref<DataTableRowKey[]>([])

const handleSelectionChange = (keys: DataTableRowKey[]): void => {
  selectedSegmentIds.value = keys
  emit('selectionChange', keys as number[])
}

// ── 暴露给父组件 ──
defineExpose({
  selectedSegmentIds,
  clearSelection: (): void => {
    selectedSegmentIds.value = []
  },
})
</script>

<template>
  <div class="space-y-3">
    <!-- 桌面端表格 -->
    <div :class="{ 'hidden md:block': showMobileCards }">
      <NDataTable
        remote
        :columns="columns"
        :data="segments"
        :loading="loading"
        :row-key="(row: Segment) => row.id"
        :scroll-x="1040"
        :checked-row-keys="selectedSegmentIds"
        @update:checked-row-keys="handleSelectionChange"
      />
    </div>

    <!-- 移动端卡片 -->
    <div v-if="showMobileCards" class="space-y-3 md:hidden">
      <NSpin v-if="loading" :show="true" class="flex justify-center py-8" />
      <NEmpty
        v-else-if="segments.length === 0"
        :description="t('workspace.segment.empty')"
        class="py-8"
      />
      <template v-else>
        <SegmentMobileCard
          v-for="segment in segments"
          :key="segment.id"
          :segment="segment"
          :text-render-mode="textRenderMode"
          :show-updated-at="showUpdatedAt"
          :show-comment="showComment"
          :is-editing="inlineEditingSegmentId === segment.id"
          :edit-form="inlineEditForm"
          :is-comment-open="inlineCommentVisible === segment.id"
          :comment-text="inlineCommentText"
          :is-saving="editingSegmentIds.includes(segment.id)"
          @start-edit="emit('startInlineEdit', segment)"
          @cancel-edit="emit('cancelInlineEdit')"
          @save-edit="emit('saveInlineEdit', segment)"
          @open-comment="emit('openInlineComment', segment)"
          @save-comment="emit('saveInlineComment', segment)"
          @close-comment="emit('closeInlineComment')"
          @update-comment-text="(val: string) => emit('update:inlineCommentText', val)"
          @update-edit-field="(field, val) => emit('update:inlineEditForm', field, val)"
          @translate="emit('translate', segment)"
        />
      </template>
    </div>

    <!-- 加载更多按钮 -->
    <div v-if="hasMore" class="flex justify-center pt-3">
      <NButton :loading="loading" @click="emit('loadMore')">
        {{ t('common.loadMore') }}
      </NButton>
    </div>

    <!-- 桌面端空状态 -->
    <NEmpty
      v-if="!loading && segments.length === 0"
      :class="{ 'hidden md:block': showMobileCards }"
      class="py-8"
      :description="t('workspace.segment.empty')"
    />
  </div>
</template>
