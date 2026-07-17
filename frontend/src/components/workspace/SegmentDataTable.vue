<script setup lang="ts">
import type { DataTableRowKey } from 'naive-ui'
import { NButton, NDataTable, NEmpty, NSpin } from 'naive-ui'
import { ref, toRef, computed, watch, nextTick, onMounted, onUnmounted } from 'vue'

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
  saveAndEditNext: [segment: Segment]
  openInlineComment: [segment: Segment]
  saveInlineComment: [segment: Segment]
  closeInlineComment: []
  'update:inlineCommentText': [value: string]
  'update:inlineEditForm': [field: 'source_text' | 'target_text' | 'comment', value: string]
}>()

// ── 响应式配置 ──
const config = computed<SegmentTableConfig>(() => ({
  textRenderMode: props.textRenderMode,
  showUpdatedAt: props.showUpdatedAt,
  showComment: props.showComment,
  showSelection: props.showSelection,
}))

const configRef = toRef(config)

// ── 原文 HTML 源码切换 ──
const showSourceHtml = ref(false)

const toggleSourceHtml = (): void => {
  showSourceHtml.value = !showSourceHtml.value
}

watch(
  () => props.inlineEditingSegmentId,
  (newId) => {
    showSourceHtml.value = false
    if (newId !== null) {
      const idx = props.segments.findIndex((s) => s.id === newId)
      if (idx >= 0) {
        focusedRowIndex.value = idx
      }
      nextTick(() => {
        const editingRow = document.querySelector('.segment-row--editing')
        const textarea = editingRow?.querySelector('textarea') as HTMLTextAreaElement | null
        textarea?.focus()
      })
    }
  },
)

// ── 依赖注入（委托 emit） ──
const deps: SegmentColumnDeps = {
  inlineEditingSegmentId: toRef(props, 'inlineEditingSegmentId'),
  inlineEditForm: props.inlineEditForm,
  inlineCommentVisible: toRef(props, 'inlineCommentVisible'),
  inlineCommentText: toRef(props, 'inlineCommentText'),
  editingSegmentIds: toRef(props, 'editingSegmentIds'),

  showSourceHtml,
  toggleSourceHtml,

  startInlineEdit: (segment) => emit('startInlineEdit', segment),
  cancelInlineEdit: () => emit('cancelInlineEdit'),
  saveInlineEdit: (segment) => {
    emit('saveInlineEdit', segment)
    return Promise.resolve()
  },
  saveAndEditNext: (segment) => {
    emit('saveAndEditNext', segment)
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

const columns = useSegmentColumns(configRef, deps)

const scrollX = computed(() => {
  const base = 50 + 280 + 280 + 110 + 160 // index + source + target + status + actions
  const selection = props.showSelection ? 48 : 0
  const updatedAt = props.showUpdatedAt ? 90 : 0
  return selection + base + updatedAt
})

// ── 行选择 ──
const selectedSegmentIds = ref<DataTableRowKey[]>([])

const handleSelectionChange = (keys: DataTableRowKey[]): void => {
  selectedSegmentIds.value = keys
  emit('selectionChange', keys as number[])
}

// ── 键盘导航 ──
const focusedRowIndex = ref<number>(-1)

const HEADER_HEIGHT = 64

const scrollFocusedRowIntoView = (): void => {
  setTimeout(() => {
    const rowEl = document.querySelector('.segment-row--focused') as HTMLElement | null
    if (!rowEl) return
    const rect = rowEl.getBoundingClientRect()
    if (rect.top < HEADER_HEIGHT) {
      window.scrollBy({ top: rect.top - HEADER_HEIGHT - 8, behavior: 'smooth' })
    } else if (rect.bottom > window.innerHeight) {
      window.scrollBy({ top: rect.bottom - window.innerHeight + 20, behavior: 'smooth' })
    }
  }, 50)
}

const rowClassName = (row: Segment): string => {
  const classes: string[] = []
  if (row.id === props.inlineEditingSegmentId) {
    classes.push('segment-row--editing')
  } else if (row.status === 'approved') {
    classes.push('segment-row--approved')
  } else if (row.status === 'translated' || row.status === 'edited') {
    classes.push('segment-row--translated')
  } else if (row.status === 'rejected') {
    classes.push('segment-row--rejected')
  }
  if (props.segments.indexOf(row) === focusedRowIndex.value) {
    classes.push('segment-row--focused')
  }
  return classes.join(' ')
}

const handleKeyDown = (e: KeyboardEvent): void => {
  const target = e.target as HTMLElement
  const isInInput = target.tagName === 'INPUT' || target.tagName === 'TEXTAREA'

  if (e.key === 'Escape') {
    if (props.inlineEditingSegmentId !== null) {
      e.preventDefault()
      emit('cancelInlineEdit')
    }
    return
  }

  if (isInInput) {
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey) && props.inlineEditingSegmentId !== null) {
      e.preventDefault()
      const editingSegment = props.segments.find((s) => s.id === props.inlineEditingSegmentId)
      if (editingSegment) {
        emit('saveAndEditNext', editingSegment)
      }
    }
    return
  }

  if (e.key === 'ArrowDown') {
    e.preventDefault()
    if (focusedRowIndex.value < props.segments.length - 1) {
      focusedRowIndex.value++
      scrollFocusedRowIntoView()
    }
  } else if (e.key === 'ArrowUp') {
    e.preventDefault()
    if (focusedRowIndex.value > 0) {
      focusedRowIndex.value--
      scrollFocusedRowIntoView()
    }
  } else if (e.key === 'Enter' && !e.ctrlKey && !e.metaKey) {
    if (focusedRowIndex.value >= 0 && focusedRowIndex.value < props.segments.length) {
      e.preventDefault()
      const segment = props.segments[focusedRowIndex.value]
      if (segment) emit('startInlineEdit', segment)
    }
  }
}

onMounted(() => {
  document.addEventListener('keydown', handleKeyDown)
})

onUnmounted(() => {
  document.removeEventListener('keydown', handleKeyDown)
})

const handleRowClick = (_event: MouseEvent, row: Segment): void => {
  focusedRowIndex.value = props.segments.indexOf(row)
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
        :scroll-x="scrollX"
        :row-class-name="rowClassName"
        :row-props="(row: Segment) => ({ onClick: (e: MouseEvent) => handleRowClick(e, row) })"
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
          :is-saving="editingSegmentIds.includes(segment.id)"
          :is-comment-visible="inlineCommentVisible === segment.id"
          :comment-text="inlineCommentText"
          @start-edit="emit('startInlineEdit', segment)"
          @cancel-edit="emit('cancelInlineEdit')"
          @save-edit="emit('saveInlineEdit', segment)"
          @save-and-next="emit('saveAndEditNext', segment)"
          @open-comment="emit('openInlineComment', segment)"
          @save-comment="emit('saveInlineComment', segment)"
          @close-comment="emit('closeInlineComment')"
          @update-edit-field="(field, val) => emit('update:inlineEditForm', field, val)"
          @update-comment-text="(val) => emit('update:inlineCommentText', val)"
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
