<script setup lang="ts">
import { NAlert, NButton, NDrawer, NDrawerContent, NEmpty, NSelect } from 'naive-ui'
import { computed, nextTick, ref, toRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import type { ApiSchemas } from '@/api/client'
import {
  getQualityCodeLabel,
  QUALITY_CODE_GROUPS,
  type QualityCode,
} from '@/composables/useQualityIssues'
import { useSegmentEditing } from '@/composables/useSegmentEditing'
import {
  type SegmentQualityCodeFilter,
  type SegmentQualityIssuesFilter,
  type SegmentQualitySeverityFilter,
  useProjectWorkspaceStore,
} from '@/stores/projectWorkspace'
import { DRAWER_WIDTH } from '@/components/common/uiConstants'
import SegmentChapterSidebar from '@/components/workspace/SegmentChapterSidebar.vue'
import SegmentDataTable from '@/components/workspace/SegmentDataTable.vue'
import SegmentSearchPanel from '@/components/workspace/SegmentSearchPanel.vue'
import SegmentSearchReplaceDrawer from '@/components/workspace/SegmentSearchReplaceDrawer.vue'

type Segment = ApiSchemas['Segment']

const { t } = useI18n()
const workspace = useProjectWorkspaceStore()

const props = defineProps<{
  projectId: number | null
}>()

const emit = defineEmits<{
  previewTranslation: [segment: Segment]
  previewRevision: [segment: Segment]
  refresh: []
  translateByGroupKey: [groupKey: string]
  selectionChange: [segmentIds: number[]]
  translateBatch: [segmentIds: number[]]
  qaRecheck: []
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
  dismissIssue,
  reinstateIssue,
} = useSegmentEditing(projectIdRef, activeResourceIdRef)

// ── 文本渲染模式 ──
const textRenderMode = computed<'plaintext' | 'html'>(() => {
  const format = workspace.activeResource?.format
  return format === 'epub' || format === 'docx' || format === 'html' ? 'html' : 'plaintext'
})

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

// ── 搜索定位面板与移动端抽屉 ──
const searchPanelVisible = ref(true)
const chaptersDrawerVisible = ref(false)
const searchDrawerVisible = ref(false)

// 主文档流滚动容器：跳转/章节切换后需要主动定位
const mainScrollRef = ref<HTMLElement | null>(null)

/** 滚动到定位行并尽量居中（锚点行 + 上文行都在窗口内，视觉焦点在锚点） */
const scrollMainToAnchor = async (): Promise<void> => {
  await nextTick()
  const host = mainScrollRef.value
  if (!host) return
  const anchor = host.querySelector('.segment-row--focused')
  if (anchor instanceof HTMLElement) {
    const hostRect = host.getBoundingClientRect()
    const rect = anchor.getBoundingClientRect()
    const target =
      host.scrollTop + (rect.top - hostRect.top) - host.clientHeight / 2 + rect.height / 2
    host.scrollTo({ top: Math.max(0, target) })
  } else {
    host.scrollTop = 0
  }
}

const handleSearchJumped = (): void => {
  // 移动端跳转后收起搜索抽屉；桌面端抽屉本就未打开，无需区分视口
  searchDrawerVisible.value = false
  void scrollMainToAnchor()
}

// 章节切换（含"全部章节"）后回顶：列表从章首重新加载
watch(
  () => workspace.epubActiveGroupKey,
  () => {
    void nextTick().then(() => {
      if (mainScrollRef.value) mainScrollRef.value.scrollTop = 0
    })
  },
)

// ── 向上加载（锚点窗口 / 滚到窗口顶）：前置更早段落并补偿滚动位置 ──
const handleLoadMoreUp = async (): Promise<void> => {
  if (!props.projectId || !workspace.activeResourceId) return
  const host = mainScrollRef.value
  const prevHeight = host?.scrollHeight ?? 0
  const prevTop = host?.scrollTop ?? 0
  await workspace.loadMoreSegmentsUp(
    props.projectId,
    workspace.activeResourceId,
    workspace.epubActiveGroupKey ?? undefined,
  )
  await nextTick()
  if (host) {
    host.scrollTop = host.scrollHeight - prevHeight + prevTop
  }
}

// ── 结果计数（include_total 返回）──
const segmentsCountLabel = computed(() => {
  if (workspace.segmentsTotal === null) return null
  return t('workspace.segment.resultCount', {
    shown: workspace.segments.length,
    total: workspace.segmentsTotal,
  })
})

// ── 搜索替换抽屉 ──
const searchReplaceDrawerRef = ref<InstanceType<typeof SegmentSearchReplaceDrawer> | null>(null)

const openSearchReplace = (): void => {
  searchReplaceDrawerRef.value?.open()
}

const handleSearchReplaceApplied = (payload: { resourceId: number }): void => {
  if (!props.projectId) return
  void workspace.loadSegments(
    props.projectId,
    payload.resourceId,
    false,
    workspace.epubActiveGroupKey ?? undefined,
  )
  if (workspace.isEpubResource) {
    void workspace.refreshChapterGroups(props.projectId, payload.resourceId)
  }
}

// ── 质量筛选 chips ──
const qualityIssuesChips = computed(() => [
  { value: 'has' as const, label: t('workspace.filters.qualityIssuesHas') },
  { value: 'none' as const, label: t('workspace.filters.qualityIssuesNone') },
])

const qualitySeverityChips = computed(() => [
  { value: 'error' as const, label: t('workspace.segment.qualityError'), tone: 'danger' as const },
  {
    value: 'warning' as const,
    label: t('workspace.segment.qualityWarning'),
    tone: 'warning' as const,
  },
])

// ── 质量代码分组下拉 ──
// 24 项 code 用分组下拉呈现，支持搜索，避免 chip 平铺导致的布局失衡。
const qualityCodeSelectValue = computed<SegmentQualityCodeFilter | null>({
  get: () =>
    workspace.segmentQualityCodeFilter === 'all' ? null : workspace.segmentQualityCodeFilter,
  set: (val) => {
    workspace.segmentQualityCodeFilter = val ?? 'all'
    if (val && workspace.segmentQualityIssuesFilter === 'none') {
      workspace.segmentQualityIssuesFilter = 'all'
    }
  },
})

const qualityCodeSelectOptions = computed(() =>
  QUALITY_CODE_GROUPS.map((group) => ({
    type: 'group' as const,
    label: t(`workspace.segment.qualityCodeGroups.${group.key}`),
    key: group.key,
    children: group.codes.map((value: QualityCode) => ({
      label: getQualityCodeLabel(value),
      value,
    })),
  })),
)

const hasActiveQualityFilter = computed(
  () =>
    workspace.segmentQualityIssuesFilter !== 'all' ||
    workspace.segmentQualitySeverityFilter !== 'all' ||
    workspace.segmentQualityCodeFilter !== 'all',
)

const toggleQualityIssues = (value: Exclude<SegmentQualityIssuesFilter, 'all'>): void => {
  workspace.segmentQualityIssuesFilter =
    workspace.segmentQualityIssuesFilter === value ? 'all' : value
  if (workspace.segmentQualityIssuesFilter === 'none') {
    workspace.segmentQualitySeverityFilter = 'all'
    workspace.segmentQualityCodeFilter = 'all'
  }
}

const toggleQualitySeverity = (value: Exclude<SegmentQualitySeverityFilter, 'all'>): void => {
  const next = workspace.segmentQualitySeverityFilter === value ? 'all' : value
  workspace.segmentQualitySeverityFilter = next
  if (next !== 'all' && workspace.segmentQualityIssuesFilter === 'none') {
    workspace.segmentQualityIssuesFilter = 'all'
  }
}

const clearQualityFilters = (): void => {
  workspace.segmentQualityIssuesFilter = 'all'
  workspace.segmentQualitySeverityFilter = 'all'
  workspace.segmentQualityCodeFilter = 'all'
}

const CHIP_BASE =
  'inline-flex h-7 items-center rounded-full border px-2.5 text-xs font-medium transition-all select-none'

const CHIP_CLASSES = {
  disabled: `${CHIP_BASE} cursor-not-allowed border-lf-border-soft bg-lf-surface-muted/40 text-lf-text-subtle`,
  activeDanger: `${CHIP_BASE} cursor-pointer border-lf-danger-soft bg-lf-danger-soft text-lf-danger shadow-sm shadow-lf-shadow`,
  activeWarning: `${CHIP_BASE} cursor-pointer border-lf-warning-soft bg-lf-warning-soft/50 text-lf-warning shadow-sm shadow-lf-shadow`,
  active: `${CHIP_BASE} cursor-pointer border-brand-500/35 bg-lf-brand-soft text-brand-700 shadow-sm shadow-lf-shadow`,
  inactive: `${CHIP_BASE} cursor-pointer border-lf-border-soft bg-lf-surface text-lf-text-muted hover:border-lf-border hover:bg-lf-surface-elevated hover:text-lf-text-strong`,
} as const

const chipClass = (active: boolean, tone: 'default' | 'danger' | 'warning' = 'default'): string => {
  if (!workspace.activeResourceId) return CHIP_CLASSES.disabled
  if (active) {
    if (tone === 'danger') return CHIP_CLASSES.activeDanger
    if (tone === 'warning') return CHIP_CLASSES.activeWarning
    return CHIP_CLASSES.active
  }
  return CHIP_CLASSES.inactive
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

// reachEnd：编辑到窗口末尾且还有更多时触发。此处仅追加加载、不强制保存：
// saveInlineEdit / saveAndEditNext 保存后都会取消编辑态，导致连续编辑中断；
// append 后保持编辑态，用户再次 Ctrl+Enter 即可走 saveAndEditNext 继续下一条。
const handleReachEnd = (): void => {
  handleLoadMore()
}

// ── 事件转发处理 ──
const handleSelectionChange = (ids: number[]): void => {
  selectedSegmentIds.value = ids
  emit('selectionChange', ids)
}

const handlePreviewTranslation = (segment: Segment): void => {
  emit('previewTranslation', segment)
}

const handlePreviewRevision = (segment: Segment): void => {
  emit('previewRevision', segment)
}

const handleSaveAndEditNext = (segment: Segment): void => {
  void saveAndEditNext(segment, workspace.segments)
}

const handleUpdateInlineEditForm = (field: 'target_text' | 'comment', value: string): void => {
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
  <div class="flex h-full min-h-0 flex-col gap-3">
    <!-- 工具栏（瘦身：搜索/章节入口移至搜索面板与章节侧栏） -->
    <div
      class="flex shrink-0 flex-col gap-2.5 rounded-lf-card border border-lf-border-soft bg-lf-surface-muted/50 px-3 py-2.5"
    >
      <div class="flex flex-col gap-2.5 xl:flex-row xl:items-center xl:justify-between">
        <div class="flex min-w-0 flex-1 flex-col gap-2 md:flex-row md:flex-wrap md:items-center">
          <NSelect
            v-model:value="workspace.activeResourceId"
            clearable
            size="small"
            class="md:min-w-36 md:max-w-xs"
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
            v-model:value="workspace.segmentStatusFilter"
            size="small"
            class="w-36! shrink-0"
            :disabled="!workspace.activeResourceId"
            :options="segmentStatusOptions"
          />
        </div>
        <div class="flex shrink-0 flex-wrap items-center gap-2">
          <span
            v-if="segmentsCountLabel"
            class="hidden text-xs whitespace-nowrap text-lf-text-muted sm:inline"
          >
            {{ segmentsCountLabel }}
          </span>
          <NButton
            size="small"
            class="hidden! xl:inline-flex!"
            :secondary="searchPanelVisible"
            :type="searchPanelVisible ? 'primary' : 'default'"
            :title="t('workspace.segment.searchLocateToggleHint')"
            @click="searchPanelVisible = !searchPanelVisible"
          >
            {{ t('workspace.segment.searchLocateToggle') }}
          </NButton>
          <NButton
            size="small"
            class="xl:hidden!"
            :disabled="!workspace.activeResourceId"
            :title="t('workspace.segment.searchLocateToggleHint')"
            @click="searchDrawerVisible = true"
          >
            {{ t('workspace.segment.searchLocateToggle') }}
          </NButton>
          <!-- 三栏全开（章节栏常驻）需要约 1620px 以上；更窄视口章节导航走此按钮 + 抽屉 -->
          <NButton
            v-if="workspace.isEpubResource"
            size="small"
            class="min-[1620px]:hidden!"
            :disabled="!workspace.activeResourceId"
            @click="chaptersDrawerVisible = true"
          >
            {{ t('workspace.segment.openChapters') }}
          </NButton>
          <NButton secondary size="small" @click="emit('qaRecheck')">
            {{ t('workspace.qaRecheck.action') }}
          </NButton>
          <NButton
            secondary
            size="small"
            :disabled="!workspace.activeResourceId"
            :loading="workspace.loadingSegments"
            @click="handleRefresh"
          >
            {{ t('common.actions.refresh') }}
          </NButton>
        </div>
      </div>

      <div
        class="flex flex-wrap items-center gap-x-3 gap-y-2 border-t border-lf-border-soft/80 pt-2.5"
      >
        <span class="text-[11px] font-medium tracking-wide text-lf-text-subtle uppercase">
          {{ t('workspace.filters.qualityLabel') }}
        </span>

        <div class="flex flex-wrap items-center gap-1.5">
          <button
            v-for="chip in qualityIssuesChips"
            :key="chip.value"
            type="button"
            :disabled="!workspace.activeResourceId"
            :class="chipClass(workspace.segmentQualityIssuesFilter === chip.value)"
            @click="toggleQualityIssues(chip.value)"
          >
            {{ chip.label }}
          </button>
        </div>

        <span class="hidden h-3.5 w-px bg-lf-border-soft sm:inline-block" />

        <div class="flex flex-wrap items-center gap-1.5">
          <button
            v-for="chip in qualitySeverityChips"
            :key="chip.value"
            type="button"
            :disabled="!workspace.activeResourceId"
            :class="chipClass(workspace.segmentQualitySeverityFilter === chip.value, chip.tone)"
            @click="toggleQualitySeverity(chip.value)"
          >
            {{ chip.label }}
          </button>
        </div>

        <span class="hidden h-3.5 w-px bg-lf-border-soft sm:inline-block" />

        <NSelect
          v-model:value="qualityCodeSelectValue"
          size="small"
          filterable
          clearable
          class="min-w-36 max-w-xs flex-1"
          :disabled="!workspace.activeResourceId"
          :options="qualityCodeSelectOptions"
          :placeholder="t('workspace.segment.qualityCodePlaceholder')"
        />

        <button
          v-if="hasActiveQualityFilter"
          type="button"
          class="ml-auto text-xs text-lf-text-muted transition-colors hover:text-lf-text-strong"
          :disabled="!workspace.activeResourceId"
          @click="clearQualityFilters"
        >
          {{ t('workspace.filters.clearQuality') }}
        </button>
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

    <!-- 三栏：章节侧栏 | 文档流（唯一滚动区） | 搜索定位面板 -->
    <div v-else class="flex min-h-0 flex-1 gap-3">
      <SegmentChapterSidebar
        v-if="workspace.isEpubResource && workspace.segmentGroups.length > 0"
        class="hidden w-60 shrink-0 min-[1620px]:flex"
        :project-id="projectId"
      />

      <div ref="mainScrollRef" class="lf-scroll min-w-0 flex-1 space-y-3 overflow-y-auto">
        <div class="lf-table overflow-hidden rounded-lf-card border border-lf-border-soft">
          <SegmentDataTable
            ref="segmentDataTableRef"
            :segments="workspace.segments"
            :loading="workspace.loadingSegments"
            :has-more="workspace.segmentsCursor !== null"
            :text-render-mode="textRenderMode"
            :show-updated-at="false"
            :show-mobile-cards="true"
            :show-selection="true"
            :show-comment="true"
            :editing-segment-ids="workspace.editingSegmentIds"
            :inline-editing-segment-id="inlineEditingSegmentId"
            :inline-edit-form="inlineEditForm"
            :inline-comment-visible="inlineCommentVisible"
            :inline-comment-text="inlineCommentText"
            :anchor-flash-segment-id="workspace.searchActiveResultId"
            :anchor-context-ids="workspace.anchorContextIds"
            :has-prev="workspace.segmentsPrevCursor !== null"
            :loading-up="workspace.loadingSegmentsUp"
            :search-query="workspace.segmentSearch"
            :search-case-sensitive="workspace.segmentSearchCaseSensitive"
            @selection-change="handleSelectionChange"
            @preview-translation="handlePreviewTranslation"
            @preview-revision="handlePreviewRevision"
            @load-more="handleLoadMore"
            @load-more-up="handleLoadMoreUp"
            @reach-end="handleReachEnd"
            @start-inline-edit="startInlineEdit"
            @cancel-inline-edit="cancelInlineEdit"
            @save-inline-edit="saveInlineEdit"
            @save-and-edit-next="handleSaveAndEditNext"
            @open-inline-comment="openInlineComment"
            @save-inline-comment="saveInlineComment"
            @close-inline-comment="handleCloseInlineComment"
            @update:inline-comment-text="handleUpdateInlineCommentText"
            @update:inline-edit-form="handleUpdateInlineEditForm"
            @dismiss-issue="dismissIssue"
            @reinstate-issue="reinstateIssue"
          />
        </div>
      </div>

      <SegmentSearchPanel
        v-if="searchPanelVisible"
        class="hidden w-86 shrink-0 xl:flex"
        :project-id="projectId"
        :text-render-mode="textRenderMode"
        @jumped="handleSearchJumped"
        @open-replace="openSearchReplace"
      />
    </div>

    <!-- 移动端：章节抽屉 -->
    <NDrawer v-model:show="chaptersDrawerVisible" placement="left" :width="280">
      <NDrawerContent closable :title="t('workspace.segment.openChapters')">
        <SegmentChapterSidebar
          v-if="workspace.isEpubResource && workspace.segmentGroups.length > 0"
          class="h-full"
          :project-id="projectId"
        />
      </NDrawerContent>
    </NDrawer>

    <!-- 移动端：搜索定位抽屉 -->
    <NDrawer v-model:show="searchDrawerVisible" placement="right" :width="DRAWER_WIDTH.s">
      <NDrawerContent closable :title="t('workspace.segment.searchLocate.title')">
        <SegmentSearchPanel
          v-if="searchDrawerVisible"
          class="h-full"
          :project-id="projectId"
          :text-render-mode="textRenderMode"
          @jumped="handleSearchJumped"
          @open-replace="openSearchReplace"
        />
      </NDrawerContent>
    </NDrawer>

    <SegmentSearchReplaceDrawer
      ref="searchReplaceDrawerRef"
      :project-id="projectId"
      :text-render-mode="textRenderMode"
      :selected-segment-ids="selectedSegmentIds"
      @applied="handleSearchReplaceApplied"
    />
  </div>
</template>
