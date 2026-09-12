<script setup lang="ts">
import type { DataTableRowKey } from 'naive-ui'
import { NDataTable, NEmpty, NSpin } from 'naive-ui'
import { ref, toRef, computed, watch, nextTick, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'

import type { ApiSchemas } from '@/api/client'
import type { SegmentFormModel } from '@/composables/useSegmentEditing'
import type { SegmentTableConfig, SegmentColumnDeps } from '@/composables/segmentColumns'
import { useSegmentColumns } from '@/composables/segmentColumns'
import type { SearchMatchMode, SearchMatchOptions } from '@/composables/useSearchHighlight'
import SegmentMobileCard from '@/components/workspace/SegmentMobileCard.vue'

type Segment = ApiSchemas['Segment']

const { t } = useI18n()

// ── Props ──
const props = defineProps<{
  segments: Segment[]
  loading: boolean
  hasMore: boolean
  /** 窗口顶还有更早内容（prev_cursor 非空） */
  hasPrev?: boolean
  loadingUp?: boolean
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
  /** 定位高亮的段落 id（锚点定位闪烁） */
  anchorFlashSegmentId?: number | null
  /** 搜索定位面板关键词（激活时源文/译文列以搜索高亮渲染） */
  searchQuery?: string
  /** 搜索字段范围：被排除的列不做搜索高亮 */
  searchField?: 'source' | 'target' | 'both'
  searchCaseSensitive?: boolean
  /** 搜索定位匹配模式（substring / regex），与面板同源 */
  searchMatchMode?: SearchMatchMode
  /** 搜索定位全字匹配，与面板同源 */
  searchWholeWord?: boolean
}>()

// ── Emits ──
const emit = defineEmits<{
  selectionChange: [segmentIds: number[]]
  previewTranslation: [segment: Segment]
  previewRevision: [segment: Segment]
  loadMore: []
  /** 到达窗口顶部且还有更早内容：向上加载一页（父级负责滚动位置补偿） */
  loadMoreUp: []
  /** 编辑窗口底部：已保存并到达当前列表末尾，请加载更多（保留当前编辑态） */
  reachEnd: [segment: Segment]
  startInlineEdit: [segment: Segment]
  cancelInlineEdit: []
  saveInlineEdit: [segment: Segment]
  saveAndEditNext: [segment: Segment]
  openInlineComment: [segment: Segment]
  saveInlineComment: [segment: Segment]
  closeInlineComment: []
  dismissIssue: [segment: Segment, issue: import('@/composables/useQualityIssues').QualityIssue]
  reinstateIssue: [segment: Segment, issue: import('@/composables/useQualityIssues').QualityIssue]
  'update:inlineCommentText': [value: string]
  'update:inlineEditForm': [field: 'target_text' | 'comment', value: string]
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

// ── 质量问题高亮联动（HTML 模式） ──
const hoveredIssueKey = ref<string | null>(null)

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
const searchQueryRef = computed(() => props.searchQuery ?? '')
const searchFieldRef = computed<'source' | 'target' | 'both'>(() => props.searchField ?? 'both')
const searchMatchOptionsRef = computed<SearchMatchOptions>(() => ({
  caseSensitive: props.searchCaseSensitive ?? true,
  wholeWord: props.searchWholeWord ?? false,
  matchMode: props.searchMatchMode ?? 'substring',
}))
const deps: SegmentColumnDeps = {
  inlineEditingSegmentId: toRef(props, 'inlineEditingSegmentId'),
  inlineEditForm: props.inlineEditForm,
  inlineCommentVisible: toRef(props, 'inlineCommentVisible'),
  inlineCommentText: toRef(props, 'inlineCommentText'),
  editingSegmentIds: toRef(props, 'editingSegmentIds'),

  showSourceHtml,
  toggleSourceHtml,

  hoveredIssueKey,

  searchQuery: searchQueryRef,
  searchField: searchFieldRef,
  searchMatchOptions: searchMatchOptionsRef,

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
  onPreviewTranslation: (segment) => emit('previewTranslation', segment),
  onPreviewRevision: (segment) => emit('previewRevision', segment),
  onDismissIssue: (segment, issue) => emit('dismissIssue', segment, issue),
  onReinstateIssue: (segment, issue) => emit('reinstateIssue', segment, issue),
}

const columns = useSegmentColumns(configRef, deps)

const scrollX = computed(() => {
  // 三栏工作区下主栏较窄，列宽预算收紧：1920 屏（面板开）完整放下，更窄视口横向滚动
  const base = 64 + 260 + 260 + 100 + 144 // index + source + target + status + actions
  const selection = props.showSelection ? 48 : 0
  const updatedAt = props.showUpdatedAt ? 80 : 0
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

const scrollFocusedRowIntoView = (): void => {
  setTimeout(() => {
    const rowEl = document.querySelector('.segment-row--focused') as HTMLElement | null
    if (!rowEl) return
    // block: 'nearest' 仅在该行不可见时滚动，适配内嵌滚动容器（不再依赖 window 滚动）
    rowEl.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
  }, 50)
}

const rowClassName = (row: Segment): string => {
  const classes: string[] = []
  if (row.id === props.inlineEditingSegmentId) {
    classes.push('segment-row--editing')
  } else if (row.status === 'rejected') {
    classes.push('segment-row--rejected')
  }
  if (props.segments.indexOf(row) === focusedRowIndex.value) {
    classes.push('segment-row--focused')
  }
  if (
    props.anchorFlashSegmentId !== null &&
    props.anchorFlashSegmentId !== undefined &&
    row.id === props.anchorFlashSegmentId
  ) {
    classes.push('segment-row--anchor-flash', 'segment-row--focused')
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
        const idx = props.segments.indexOf(editingSegment)
        const isLastInWindow = idx >= 0 && idx === props.segments.length - 1
        if (isLastInWindow && props.hasMore) {
          // 已到窗口底部且还有更多：不取消编辑，通知父级保存并追加加载
          emit('reachEnd', editingSegment)
        } else {
          emit('saveAndEditNext', editingSegment)
        }
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
  setupLoadMoreObserver()
})

onUnmounted(() => {
  document.removeEventListener('keydown', handleKeyDown)
  loadMoreObserver?.disconnect()
  loadMoreObserver = null
  loadMoreUpObserver?.disconnect()
  loadMoreUpObserver = null
})

const handleRowClick = (_event: MouseEvent, row: Segment): void => {
  focusedRowIndex.value = props.segments.indexOf(row)
}

// ── 底部哨兵自动加载 ──
// 滚动由外层 pane 承担，此处仅做视口级（root: null）哨兵观察，
// rootMargin 提前 600px 预加载；loading 中不重复 emit。
const loadMoreSentinel = ref<HTMLElement | null>(null)
let loadMoreObserver: IntersectionObserver | null = null

const setupLoadMoreObserver = (): void => {
  loadMoreObserver?.disconnect()
  loadMoreObserver = null
  if (!loadMoreSentinel.value) return
  loadMoreObserver = new IntersectionObserver(
    (entries) => {
      const entry = entries[0]
      if (entry?.isIntersecting && props.hasMore && !props.loading) {
        emit('loadMore')
      }
    },
    { root: null, rootMargin: '600px' },
  )
  loadMoreObserver.observe(loadMoreSentinel.value)
}

// hasMore 变化时哨兵随 v-if 挂载/卸载；loading 结束后重新 observe，
// 使哨兵仍在视口内时能触发下一次 IntersectionObserver 初始回调。
watch([() => props.hasMore, () => props.loading], async () => {
  await nextTick()
  setupLoadMoreObserver()
})

// ── 顶部哨兵：向上加载（锚点窗口 / 章节中部滚动到顶时前置更早段落） ──
const loadMoreUpSentinel = ref<HTMLElement | null>(null)
let loadMoreUpObserver: IntersectionObserver | null = null

const setupLoadMoreUpObserver = (): void => {
  loadMoreUpObserver?.disconnect()
  loadMoreUpObserver = null
  if (!loadMoreUpSentinel.value || !props.hasPrev) return
  loadMoreUpObserver = new IntersectionObserver(
    (entries) => {
      const entry = entries[0]
      if (entry?.isIntersecting && props.hasPrev && !props.loading && !props.loadingUp) {
        emit('loadMoreUp')
      }
    },
    { root: null, rootMargin: '300px' },
  )
  loadMoreUpObserver.observe(loadMoreUpSentinel.value)
}

watch(
  [() => props.hasPrev, () => props.loading, () => props.loadingUp],
  async () => {
    await nextTick()
    setupLoadMoreUpObserver()
  },
  { immediate: true },
)

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
    <!-- 顶部哨兵：窗口顶还有更早内容时进入视口即向上加载 -->
    <div v-if="hasPrev" ref="loadMoreUpSentinel" class="flex h-8 items-center justify-center pb-1">
      <NSpin v-if="loadingUp" :show="true" :size="14" />
    </div>

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
          :class="
            segment.id === anchorFlashSegmentId
              ? 'segment-row--anchor-flash segment-row--focused'
              : undefined
          "
          @start-edit="emit('startInlineEdit', segment)"
          @cancel-edit="emit('cancelInlineEdit')"
          @save-edit="emit('saveInlineEdit', segment)"
          @save-and-next="emit('saveAndEditNext', segment)"
          @open-comment="emit('openInlineComment', segment)"
          @save-comment="emit('saveInlineComment', segment)"
          @close-comment="emit('closeInlineComment')"
          @update-edit-field="(field, val) => emit('update:inlineEditForm', field, val)"
          @update-comment-text="(val) => emit('update:inlineCommentText', val)"
          @preview-translation="emit('previewTranslation', segment)"
          @preview-revision="emit('previewRevision', segment)"
          @dismiss-issue="(seg, issue) => emit('dismissIssue', seg, issue)"
          @reinstate-issue="(seg, issue) => emit('reinstateIssue', seg, issue)"
        />
      </template>
    </div>

    <!-- 底部哨兵：进入视口即自动加载下一页（rootMargin 提前预载） -->
    <div v-if="hasMore" ref="loadMoreSentinel" class="flex h-10 items-center justify-center pt-3">
      <NSpin v-if="loading" :show="true" :size="16" />
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
