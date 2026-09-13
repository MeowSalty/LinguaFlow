<script setup lang="ts">
import { NAlert, NButton, NEmpty, NIcon, NSelect, NTag } from 'naive-ui'
import { computed, nextTick, onBeforeUnmount, onMounted, ref, toRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'

import type { ApiSchemas } from '@/api/client'
import {
  getQualityCodeLabel,
  QUALITY_CODE_GROUPS,
  type QualityCode,
} from '@/composables/useQualityIssues'
import { useResponsiveDock } from '@/composables/useResponsiveDock'
import { useSegmentEditing } from '@/composables/useSegmentEditing'
import {
  type SegmentQualityCodeFilter,
  type SegmentQualityIssuesFilter,
  type SegmentQualitySeverityFilter,
  useProjectWorkspaceStore,
} from '@/stores/projectWorkspace'
import SegmentChapterSidebar from '@/components/workspace/SegmentChapterSidebar.vue'
import SegmentDataTable from '@/components/workspace/SegmentDataTable.vue'
import SegmentSearchPanel from '@/components/workspace/SegmentSearchPanel.vue'
import SegmentSearchReplaceDrawer from '@/components/workspace/SegmentSearchReplaceDrawer.vue'

type Segment = ApiSchemas['Segment']

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const workspace = useProjectWorkspaceStore()

const props = defineProps<{
  projectId: number | null
}>()

const emit = defineEmits<{
  previewTranslation: [segment: Segment]
  previewRevision: [segment: Segment]
  refresh: []
  selectionChange: [segmentIds: number[]]
  qaRecheck: []
  batchTranslate: []
  batchQaRecheck: []
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

// 段落 ID 仅在所属资源内有效；路由深链或资源选择器切换时同步清空父子选择状态，
// 避免搜索替换抽屉重新选择「仅选中段落」后携带上一资源的 ID。
watch(
  () => workspace.activeResourceId,
  (resourceId, previousResourceId) => {
    if (resourceId !== previousResourceId) clearSelectedSegments()
  },
)

// ── 暴露给父组件，供浮动操作岛使用 ──
defineExpose({
  selectedSegmentIds,
  clearSelectedSegments,
})

// ── 编辑视图路由：query.edit / query.chapter 由页面层写入与恢复 ──
const exitEditor = (): void => {
  const query = { ...route.query }
  delete query.edit
  delete query.chapter
  void router.replace({ query })
}

// 编辑视图内切换资源：数据加载交给下方筛选/资源 watcher，这里只换状态 + 同步路由
const handleResourceChange = (value: number | null): void => {
  workspace.setActiveResource(value)
  workspace.exitChapter()

  const query = { ...route.query }
  if (value) {
    query.edit = String(value)
  } else {
    delete query.edit
  }
  delete query.chapter
  void router.replace({ query })
}

// 章节切换同步到路由（分享链接 / 前进后退）
watch(
  () => workspace.epubActiveGroupKey,
  (groupKey) => {
    if (route.query.edit === undefined) return
    const query = { ...route.query }
    if (groupKey) {
      query.chapter = groupKey
    } else {
      delete query.chapter
    }
    void router.replace({ query })
  },
)

// ── 席位常驻双面板（章节目录 | 搜索定位）──
// 面板实例以 Teleport 在席位与抽屉间搬移，不重挂载。
const {
  chaptersOpen,
  searchOpen,
  chaptersDocked,
  searchDocked,
  chaptersDrawerVisible,
  searchDrawerVisible,
  chaptersInDrawer,
  searchInDrawer,
  toggleChapters,
  toggleSearch,
  openChaptersDrawer,
  openSearchDrawer,
  closeChaptersDrawer,
  closeSearchDrawer,
  hideSearchDrawer,
} = useResponsiveDock()

const anyDrawerVisible = computed(() => chaptersDrawerVisible.value || searchDrawerVisible.value)

/** ✕ 收起面板：席位态切开关、抽屉态关抽屉；两者都只转不可见，面板常驻挂载、重开状态不丢 */
const handleCloseChapters = (): void => {
  if (chaptersInDrawer.value) {
    closeChaptersDrawer()
  } else {
    toggleChapters()
  }
}

const handleCloseSearch = (): void => {
  if (searchInDrawer.value) {
    closeSearchDrawer()
  } else {
    toggleSearch()
  }
}

const closeAllDrawers = (): void => {
  if (chaptersDrawerVisible.value) closeChaptersDrawer()
  if (searchDrawerVisible.value) closeSearchDrawer()
}

// ── 搜索面板实例：面板常驻挂载（收起/隐藏仅转不可见），聚焦需父级主动驱动 ──
const searchPanelRef = ref<InstanceType<typeof SegmentSearchPanel> | null>(null)

// 挂载门：面板打开过一次就保持挂载——之后「收起/隐藏」只转不可见（见 searchPanelClass），
// 输入、结果列表滚动与选中态在整个编辑视图生命周期内不丢
const searchPanelMounted = ref(false)
watch(searchOpen, (open) => {
  if (open) searchPanelMounted.value = true
})

// 搜索面板呈现形态：抽屉 | 停靠席位 | 不可见挂起。不可见用 visibility 而非
// display:none——后者会丢内部滚动位置；挂起沿用当前几何（席位/抽屉），恢复可见时零位移
const searchPanelClass = computed(() => {
  if (searchInDrawer.value) {
    return 'lf-drawer lf-drawer--right w-86 max-w-[90vw] animate-drawer-right'
  }
  if (searchDocked.value) {
    const seat = 'absolute top-0 bottom-0 left-[calc(100%+12px)] w-86'
    return searchOpen.value ? `${seat} flex` : `${seat} invisible pointer-events-none`
  }
  return 'lf-drawer lf-drawer--right w-86 max-w-[90vw] invisible pointer-events-none'
})

// 形态变化（可见性切换或抽屉↔席位断点跨越）后回填结果列表滚动位置：
// 跨越伴随一次 Teleport 搬移，浏览器会重置滚动容器的 scrollTop
watch(searchPanelClass, () => {
  void searchPanelRef.value?.restoreResultsScroll()
})

/** Ctrl+F：面板未展开时先展开（席位或抽屉形态），聚焦随后统一处理 */
const handleSearchActivate = (): void => {
  if (searchInDrawer.value) return
  if (searchDocked.value) {
    if (!searchOpen.value) toggleSearch()
  } else {
    openSearchDrawer()
  }
}

// 抽屉拉出 / 席位展开后聚焦搜索输入：面板不再重挂载，onMounted 的聚焦只在首次挂载发生
watch(searchInDrawer, (inDrawer) => {
  if (inDrawer) void nextTick(() => searchPanelRef.value?.focusInput())
})
watch(searchOpen, (open) => {
  if (open && searchDocked.value && !searchInDrawer.value) {
    void nextTick(() => searchPanelRef.value?.focusInput())
  }
})

// 全局快捷键：Ctrl+F 打开搜索定位；ESC 关闭抽屉（输入框内的 Esc 交给输入框自身处理）
const handleGlobalKeyDown = (e: KeyboardEvent): void => {
  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'f') {
    if (!workspace.activeResourceId) return
    e.preventDefault()
    handleSearchActivate()
    void nextTick(() => searchPanelRef.value?.focusInput())
    return
  }
  if (e.key !== 'Escape' || !anyDrawerVisible.value) return
  if (e.target instanceof HTMLInputElement || e.target instanceof HTMLTextAreaElement) return
  closeAllDrawers()
}

onMounted(() => {
  document.addEventListener('keydown', handleGlobalKeyDown)
})

onBeforeUnmount(() => {
  document.removeEventListener('keydown', handleGlobalKeyDown)
})

// ── 翻译进度（面包屑徽标）──
const progressLabel = computed(() => {
  const resource = workspace.activeResource
  if (!resource || !resource.total_segments) return null
  const percent = Math.round((resource.translated_segments / resource.total_segments) * 100)
  return t('workspace.editor.progressLabel', { percent: String(percent) })
})

// 主文档流滚动容器：跳转/章节切换后需要主动定位
const mainScrollRef = ref<HTMLElement | null>(null)

/** 滚动到定位行并尽量居中（锚点行 + 上文行都在窗口内，视觉焦点在锚点） */
const scrollMainToAnchor = async (): Promise<void> => {
  await nextTick()
  const host = mainScrollRef.value
  if (!host) return
  // 桌面表格与移动端卡片同时渲染（其一被 display:none 隐藏，零尺寸矩形会算错滚动目标），
  // 取当前可见的那个锚点行；用 anchor-flash 定位——focused 类还兼作键盘导航游标
  const anchor = Array.from(host.querySelectorAll<HTMLElement>('.segment-row--anchor-flash')).find(
    (el) => el.offsetParent !== null,
  )
  if (!anchor) {
    host.scrollTop = 0
    return
  }
  const hostRect = host.getBoundingClientRect()
  // 命中可能在超长行的中段，整行矩形不足以让命中可见：按跳转携带的命中字段，取该字段正文内
  // 首个 mark 的矩形；字段为 null（generic 未定位）、mark 缺失或矩形零尺寸（隐藏 / 行已更新）
  // 时回退整行矩形
  const field = workspace.searchActiveResultField
  const hit = field
    ? anchor.querySelector<HTMLElement>(`[data-search-field="${field}"] mark.search-hit`)
    : null
  const hitRect = hit?.getClientRects()[0]
  const visibleHitRect = hitRect && (hitRect.width > 0 || hitRect.height > 0) ? hitRect : null
  const rect = visibleHitRect ?? anchor.getBoundingClientRect()
  const target =
    host.scrollTop + (rect.top - hostRect.top) - host.clientHeight / 2 + rect.height / 2
  host.scrollTo({ top: Math.max(0, target) })
}

const handleSearchJumped = (): void => {
  // 抽屉态跳转后仅隐藏抽屉、保留搜索会话（面板不卸载，重开时输入/滚动/选中态原样恢复）。
  // 滚动定位不在此处做——此刻开窗数据尚未落地，由下方 searchJumpSeq watcher 在完成后执行
  if (searchDrawerVisible.value) hideSearchDrawer()
}

// 跳转定位滚动：jumpToSegment 开窗成功后自增（重复跳同一条也会触发），锚点行此时已在 DOM
watch(
  () => workspace.searchJumpSeq,
  () => {
    void scrollMainToAnchor()
  },
)

// 章节切换（含"全部章节"）后回顶：列表从章首重新加载。
// 跳转定位期间让路——回顶会与锚点滚动竞态（定位由 searchJumpSeq watcher 接管）
watch(
  () => workspace.epubActiveGroupKey,
  () => {
    if (workspace.jumpingToSegmentCount > 0) return
    void nextTick().then(() => {
      if (mainScrollRef.value) mainScrollRef.value.scrollTop = 0
    })
  },
)

// ── 筛选 / 资源 / 章节切换 → 重载段落（编辑视图数据加载的唯一责任方）──
watch(
  () => [
    workspace.segmentStatusFilter,
    workspace.segmentQualityIssuesFilter,
    workspace.segmentQualitySeverityFilter,
    workspace.segmentQualityCodeFilter,
    workspace.activeResourceId,
    workspace.epubActiveGroupKey,
  ],
  (newVal, oldVal) => {
    if (!props.projectId || !workspace.activeResourceId) return
    // 跳转定位进行中：jumpToSegment 的 enterChapter 会触发本 watcher，但跳转自带
    // 锚点开窗加载；此处再按章首重载会与开窗竞态，把定位窗口覆盖为章首窗口
    if (workspace.jumpingToSegmentCount > 0) return

    const resourceIdChanged = newVal[4] !== oldVal?.[4]

    // EPUB 资源切换时加载章节数据
    if (resourceIdChanged && workspace.isEpubResource) {
      void workspace.loadEpubData(props.projectId, workspace.activeResourceId)
    }

    // 加载段落数据（EPUB "全部章节"视图加载全部段落，章节视图加载对应章节段落）
    void workspace.loadSegments(
      props.projectId,
      workspace.activeResourceId,
      false,
      workspace.epubActiveGroupKey ?? undefined,
    )
  },
  // immediate：编辑视图挂载时 activeResourceId 可能已被页面层深链 watcher 设好
  //（store 状态先于组件挂载），错过首跳就要等下一次筛选变化才有列表
  { immediate: true },
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
  <!-- 整个编辑视图恢复 8b6445a7 之前的全局 1100px 居中帽（AppLayout 曾统一包 max-w-275）；
       两个功能面板吸附停靠在这顶帽子的两侧空白区，视口放不下时自动转抽屉 -->
  <div class="mx-auto flex h-full w-full max-w-275 flex-col gap-3">
    <!-- 工具栏：返回 | 资源/章节面包屑 + 进度 | 面板开关与操作 -->
    <div
      class="flex shrink-0 flex-wrap items-center gap-x-2 gap-y-2 rounded-lf-card border border-lf-border-soft bg-lf-surface-muted/50 px-3 py-2.5"
    >
      <NButton
        quaternary
        size="small"
        :title="t('workspace.editor.backToBrowse')"
        @click="exitEditor"
      >
        <template #icon>
          <NIcon><IconCarbonArrowLeft /></NIcon>
        </template>
      </NButton>

      <NSelect
        v-model:value="workspace.activeResourceId"
        clearable
        size="small"
        class="w-56! shrink-0"
        :options="
          workspace.resources.map((resource) => ({
            label: resource.path,
            value: resource.id,
          }))
        "
        :placeholder="t('workspace.segment.resourcePlaceholder')"
        @update:value="handleResourceChange"
      />

      <template v-if="workspace.isEpubResource && workspace.epubActiveGroupTitle">
        <span class="hidden text-lf-text-subtle sm:inline">/</span>
        <span class="hidden max-w-48 truncate text-sm font-medium text-lf-text-strong sm:inline">
          {{ workspace.epubActiveGroupTitle }}
        </span>
      </template>

      <NTag v-if="progressLabel" size="small" :bordered="false" type="success">
        {{ progressLabel }}
      </NTag>

      <span class="min-w-0 flex-1" />

      <span
        v-if="segmentsCountLabel"
        class="hidden text-xs whitespace-nowrap text-lf-text-muted sm:inline"
      >
        {{ segmentsCountLabel }}
      </span>

      <!-- 章节目录：按钮顺序与左侧面板方向一致；停靠态切换席位，窄屏打开抽屉 -->
      <NButton
        v-if="workspace.isEpubResource && chaptersDocked"
        size="small"
        :secondary="chaptersOpen"
        :type="chaptersOpen ? 'primary' : 'default'"
        @click="toggleChapters()"
      >
        {{ t('workspace.segment.openChapters') }}
      </NButton>
      <NButton
        v-else-if="workspace.isEpubResource"
        size="small"
        :disabled="!workspace.activeResourceId"
        @click="openChaptersDrawer()"
      >
        {{ t('workspace.segment.openChapters') }}
      </NButton>

      <!-- 搜索：按钮靠右对应右侧面板，快捷键作为辅助入口 -->
      <NButton
        v-if="searchDocked"
        size="small"
        :secondary="searchOpen"
        :type="searchOpen ? 'primary' : 'default'"
        :title="t('workspace.segment.searchLocateToggleHint')"
        @click="toggleSearch()"
      >
        {{ t('workspace.segment.searchLocateToggle') }}
      </NButton>
      <NButton
        v-else
        size="small"
        :disabled="!workspace.activeResourceId"
        :title="t('workspace.segment.searchLocateToggleHint')"
        @click="openSearchDrawer()"
      >
        {{ t('workspace.segment.searchLocateToggle') }}
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

    <!-- 筛选行：状态 + 质量 chips -->
    <div
      class="flex shrink-0 flex-wrap items-center gap-x-3 gap-y-2 rounded-lf-card border border-lf-border-soft bg-lf-surface-muted/50 px-3 py-2.5"
    >
      <NSelect
        v-model:value="workspace.segmentStatusFilter"
        size="small"
        class="w-36! shrink-0"
        :disabled="!workspace.activeResourceId"
        :options="segmentStatusOptions"
      />

      <span class="h-3.5 w-px bg-lf-border-soft" />

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

    <NAlert v-if="workspace.segmentsError" type="error" :bordered="false">
      {{ workspace.segmentsError }}
    </NAlert>

    <NEmpty
      v-if="!workspace.activeResourceId"
      class="py-10"
      :description="t('workspace.segment.noResource')"
    />

    <!-- 中央内容列恢复全站限宽（max-w-275，与浏览态一致）；两个面板吸附停靠在
         内容列两侧的空白区域（不挤压内容列），视口放不下时自动转抽屉 -->
    <div v-else class="relative min-h-0 flex-1">
      <!-- 章节面板：吸附内容列左侧空白（right: 100% + 间隙）；窄视口静默收起后转入
           不可见抽屉形态挂起（列表滚动位置不丢），手动开抽屉或变宽恢复停靠。
           Teleport 仅停靠形态禁用，抽屉形态常驻 body，开关零搬移（同搜索面板） -->
      <Teleport to="body" :disabled="chaptersDocked">
        <div
          v-if="chaptersOpen && workspace.isEpubResource && workspace.segmentGroups.length > 0"
          :class="
            chaptersInDrawer
              ? 'lf-drawer lf-drawer--left w-72 max-w-[85vw] animate-drawer-left'
              : chaptersDocked
                ? 'absolute top-0 bottom-0 right-[calc(100%+12px)] flex w-58'
                : 'lf-drawer lf-drawer--left w-72 max-w-[85vw] invisible pointer-events-none'
          "
        >
          <SegmentChapterSidebar
            class="h-full w-full"
            :project-id="projectId"
            @close="handleCloseChapters"
            @batch-translate="emit('batchTranslate')"
            @batch-qa-recheck="emit('batchQaRecheck')"
          />
        </div>
      </Teleport>

      <!-- 文档流（唯一滚动区，宽度与浏览态内容区一致：限宽居中） -->
      <div ref="mainScrollRef" class="lf-scroll h-full overflow-y-auto">
        <div class="mx-auto w-full max-w-275">
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
              :has-prev="workspace.segmentsPrevCursor !== null"
              :loading-up="workspace.loadingSegmentsUp"
              :search-query="workspace.segmentSearch"
              :search-field="workspace.segmentSearchFieldFilter"
              :search-case-sensitive="workspace.segmentSearchCaseSensitive"
              :search-match-mode="workspace.segmentSearchMatchMode"
              :search-whole-word="workspace.segmentSearchWholeWord"
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
      </div>

      <!-- 搜索面板：吸附内容列右侧空白（left: 100% + 间隙）；收起/隐藏仅转不可见，
           面板实例、滚动与选中态全程保留，重开原样恢复。
           Teleport 仅停靠形态禁用（席位 absolute 定位依赖容器）；抽屉形态（可见/挂起）
           常驻 body——开关抽屉零 DOM 搬移，搬移会重置列表滚动位置 -->
      <Teleport to="body" :disabled="searchDocked">
        <div v-if="searchPanelMounted" :class="searchPanelClass">
          <SegmentSearchPanel
            ref="searchPanelRef"
            class="h-full w-full"
            :project-id="projectId"
            :text-render-mode="textRenderMode"
            :active="searchInDrawer || (searchOpen && searchDocked)"
            @jumped="handleSearchJumped"
            @open-replace="openSearchReplace"
            @close="handleCloseSearch"
          />
        </div>
      </Teleport>
    </div>

    <!-- 抽屉遮罩（任一抽屉可见时；CSS 过渡兜底：确保 leave 完成后不残留交互层） -->
    <Teleport to="body">
      <div
        v-if="chaptersInDrawer || searchInDrawer"
        class="fixed inset-0 z-40 bg-black/40 transition-opacity duration-200"
        :style="{ opacity: 1 }"
        @click="closeAllDrawers"
      />
      <!-- 不可见时铺一个 pointer-events:none 的同级节点保持过渡对称（避免 Transition 在无 rAF 环境卡 leave） -->
      <div
        v-if="!(chaptersInDrawer || searchInDrawer)"
        class="fixed inset-0 z-40 bg-black/40 pointer-events-none opacity-0 transition-opacity duration-200"
      />
    </Teleport>

    <SegmentSearchReplaceDrawer
      ref="searchReplaceDrawerRef"
      :project-id="projectId"
      :text-render-mode="textRenderMode"
      :selected-segment-ids="selectedSegmentIds"
      @applied="handleSearchReplaceApplied"
    />
  </div>
</template>
