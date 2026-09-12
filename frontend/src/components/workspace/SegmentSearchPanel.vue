<script setup lang="ts">
import { NButton, NInput, NSelect, NSpin, NTag } from 'naive-ui'
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'

import type { ApiSchemas } from '@/api/client'
import {
  buildSearchMatch,
  makeSearchSnippet,
  resolveSearchableText,
  type SearchMatchOptions,
  type SearchSnippet,
} from '@/composables/useSearchHighlight'
import { getSegmentStatusLabel, statusTagType } from '@/composables/useWorkspaceUtils'
import { useProjectWorkspaceStore } from '@/stores/projectWorkspace'
import { t } from '@/i18n'
import { countUnicodeCodePoints, SEGMENT_SEARCH_MAX_LENGTH } from '@/utils/unicode'

type Segment = ApiSchemas['Segment']

const props = defineProps<{
  projectId: number | null
  textRenderMode: 'plaintext' | 'html'
  /** 面板当前可见（席位展开或抽屉拉出）；不可见挂起时暂停结果分页自动加载 */
  active: boolean
}>()

const emit = defineEmits<{
  /** 已发起定位（jumpToSegment 已启动）；父级据此隐藏抽屉，主列表滚动由父级在开窗数据落地后执行 */
  jumped: [segment: Segment]
  /** 打开搜索替换（过渡期复用既有抽屉；后端就绪后的完整模式迁移见 Track C） */
  openReplace: []
  /** 收起面板（席位常驻模式下不影响文档流宽度） */
  close: []
}>()

const workspace = useProjectWorkspaceStore()

// ── 搜索输入（复用 store 字段，主列表 watcher 已不再监听它们）──
const searchInputRef = ref<InstanceType<typeof NInput> | null>(null)
const searchFieldValue = computed({
  get: () => workspace.segmentSearchFieldFilter,
  set: (value: string) => {
    workspace.segmentSearchFieldFilter = value as 'source' | 'target' | 'both'
    scheduleSearch()
  },
})

const caseSensitiveOn = computed(() => workspace.segmentSearchCaseSensitive)
const toggleCaseSensitive = (): void => {
  workspace.segmentSearchCaseSensitive = !workspace.segmentSearchCaseSensitive
  scheduleSearch()
}

const searchFieldOptions = computed(() => [
  { label: t('workspace.segment.searchLocate.searchFieldBoth'), value: 'both' },
  { label: t('workspace.segment.searchLocate.searchFieldSource'), value: 'source' },
  { label: t('workspace.segment.searchLocate.searchFieldTarget'), value: 'target' },
])

// ── 匹配模式与全字匹配（复用 store 字段与 300ms 防抖）──
const toggleMatchMode = (): void => {
  workspace.segmentSearchMatchMode =
    workspace.segmentSearchMatchMode === 'substring' ? 'regex' : 'substring'
  scheduleSearch()
}

const matchModeTitle = computed(() =>
  workspace.segmentSearchMatchMode === 'regex'
    ? t('workspace.segment.searchLocate.matchModeRegexHint')
    : t('workspace.segment.searchLocate.matchModeSubstringHint'),
)

const wholeWordOn = computed(() => workspace.segmentSearchWholeWord)
const toggleWholeWord = (): void => {
  workspace.segmentSearchWholeWord = !workspace.segmentSearchWholeWord
  scheduleSearch()
}
const wholeWordTitle = computed(() => {
  const label = t('workspace.segment.searchLocate.wholeWord')
  return `${label} · ${t('workspace.segment.searchLocate.wholeWordHint')}`
})

/** 面板与主表共用的展示级匹配选项（后端结果权威，本地仅用于高亮与片段） */
const searchMatchOptions = computed<SearchMatchOptions>(() => ({
  caseSensitive: workspace.segmentSearchCaseSensitive,
  wholeWord: workspace.segmentSearchWholeWord,
  matchMode: workspace.segmentSearchMatchMode,
}))

const hasQuery = computed(() => workspace.segmentSearch.trim().length > 0)

// ── 防抖搜索 ──
let searchTimer: ReturnType<typeof setTimeout> | null = null
/** 防抖窗口内：旧结果已作废、新请求尚未发出（模板据此先显示搜索中） */
const searchPending = ref(false)

/** 停止防抖等待（不触碰结果），供 runSearch 与资源切换复用 */
const cancelScheduledSearch = (): void => {
  if (searchTimer) {
    clearTimeout(searchTimer)
    searchTimer = null
  }
  searchPending.value = false
}

/**
 * 关键词/字段/大小写/模式/全字变化：立即作废在途请求并清空旧结果与选中，
 * 300ms 防抖后再发新请求，避免旧结果以新选项渲染。
 */
const scheduleSearch = (): void => {
  cancelScheduledSearch()
  workspace.resetSearchResults()
  selectedResultIndex.value = -1
  searchPending.value = true
  searchTimer = setTimeout(() => void runSearch(), 300)
}

const runSearch = (): void => {
  cancelScheduledSearch()
  if (!props.projectId || !workspace.activeResourceId) return
  // 新一轮搜索：旧的选中索引对新结果集无意义（避免错标到别的条目上）
  selectedResultIndex.value = -1
  if (!hasQuery.value) {
    workspace.resetSearchResults()
    return
  }
  void workspace.loadSearchResults(props.projectId, workspace.activeResourceId, false)
}

watch(
  () => workspace.segmentSearch,
  () => scheduleSearch(),
)

// 资源切换：旧资源结果一律作废。新资源有效且有关键词时走同一防抖入口重搜，
// 否则停止悬挂的 pending 并清空——避免停留在旧结果或假的「无结果」。
watch(
  () => workspace.activeResourceId,
  (resourceId) => {
    if (resourceId && hasQuery.value) {
      scheduleSearch()
      return
    }
    cancelScheduledSearch()
    selectedResultIndex.value = -1
    workspace.resetSearchResults()
  },
)

// ── 结果卡片 ──
interface ResultCard {
  segment: Segment
  /** 命中展示文本（源文命中展示源文，仅译文命中展示译文，双命中优先源文） */
  snippet: SearchSnippet
  /**
   * 命中字段：source / target / both；
   * generic = 后端判定命中但展示级未定位（regex 模式浏览器侧不做匹配，后端结果权威）
   */
  hitField: 'source' | 'target' | 'both' | 'generic'
  /**
   * 跳转后主列表的滚动字段：both 与展示文本一致取源文；
   * generic 时展示级无 mark 可定位，为 null 由主列表回退整行
   */
  displayField: 'source' | 'target' | null
  /** 章节标题（后端 group_key 就绪前为空） */
  chapterTitle: string
}

const resultCards = computed<ResultCard[]>(() =>
  workspace.searchResults.map((segment) => {
    const options = searchMatchOptions.value
    const query = workspace.segmentSearch.trim()
    // 字段判定与片段展示同用正文高亮的可见文本：HTML 模式下剔除标签/属性，
    // 避免原始标记本身命中查询而误判命中字段
    const sourceText = resolveSearchableText(segment.source_text, props.textRenderMode)
    const targetText = resolveSearchableText(segment.target_text ?? '', props.textRenderMode)
    const inSource =
      workspace.segmentSearchFieldFilter !== 'target' &&
      buildSearchMatch(sourceText, query, options).ranges.length > 0
    const inTarget =
      workspace.segmentSearchFieldFilter !== 'source' &&
      buildSearchMatch(targetText, query, options).ranges.length > 0
    const hitField =
      inSource && inTarget ? 'both' : inSource ? 'source' : inTarget ? 'target' : 'generic'
    // 滚动字段随展示文本同源：both 取源文；两字段均未定位（generic）时为 null，主列表回退整行
    const displayField: 'source' | 'target' | null = inSource
      ? 'source'
      : inTarget
        ? 'target'
        : null
    // generic 命中（展示级未定位）按搜索字段回退展示，保证展示文本来自用户搜索的字段
    const displayText = inSource
      ? sourceText
      : inTarget
        ? targetText
        : workspace.segmentSearchFieldFilter === 'target'
          ? targetText
          : sourceText
    const chapterTitle = segment.group_key
      ? (workspace.segmentGroups.find((g) => g.group_key === segment.group_key)?.group_title ?? '')
      : ''
    return {
      segment,
      // 展示级匹配未定位命中时不做局部高亮，整段作为片段文本呈现
      snippet:
        hitField === 'generic'
          ? { before: displayText, hit: '', after: '' }
          : makeSearchSnippet(displayText, query, options),
      hitField,
      displayField,
      chapterTitle,
    }
  }),
)

const hitFieldLabel = (field: ResultCard['hitField']): string => {
  switch (field) {
    case 'source':
      return t('workspace.segment.searchLocate.hitSource')
    case 'target':
      return t('workspace.segment.searchLocate.hitTarget')
    case 'both':
      return t('workspace.segment.searchLocate.hitBoth')
    default:
      return t('workspace.segment.searchLocate.hitGeneric')
  }
}

/** 仅原文命中或 generic 命中时，片段以弱化色展示（提醒展示文本未必含高亮命中） */
const isMutedSnippet = (card: ResultCard): boolean =>
  card.hitField === 'source' || card.hitField === 'generic'

const chapterCount = computed(
  () => new Set(resultCards.value.map((c) => c.chapterTitle).filter(Boolean)).size,
)

const statsLabel = computed(() => {
  const total = workspace.searchResultsTotal
  if (!hasQuery.value) return null
  if ((searchPending.value || workspace.loadingSearchResults) && resultCards.value.length === 0) {
    return t('workspace.segment.searchLocate.loading')
  }
  if (total === null) return null
  return t('workspace.segment.searchLocate.stats', {
    count: String(total),
    chapters: String(chapterCount.value || 1),
  })
})

// ── 选择与跳转 ──
const selectedResultIndex = ref(-1)
const resultsListRef = ref<HTMLElement | null>(null)

const selectResult = (index: number, jump: boolean): void => {
  const card = resultCards.value[index]
  if (!card) return
  selectedResultIndex.value = index
  const el = resultsListRef.value?.querySelector<HTMLElement>(`[data-result-index="${index}"]`)
  el?.scrollIntoView({ block: 'nearest' })
  if (jump) jumpTo(card)
}

const jumpTo = (card: ResultCard): void => {
  if (!props.projectId || !workspace.activeResourceId) return
  // 携带展示级命中字段：主列表滚动时定位到该字段正文内的 mark（generic 为 null，回退整行）
  void workspace.jumpToSegment(
    props.projectId,
    workspace.activeResourceId,
    card.segment,
    card.displayField,
  )
  emit('jumped', card.segment)
}

// ── 结果分页自动加载 ──
const resultsSentinelRef = ref<HTMLElement | null>(null)
let resultsObserver: IntersectionObserver | null = null

const setupResultsObserver = (): void => {
  resultsObserver?.disconnect()
  resultsObserver = null
  if (!resultsSentinelRef.value) return
  resultsObserver = new IntersectionObserver(
    (entries) => {
      if (
        // 挂起（不可见）期间哨兵仍有几何、观察器仍会回调，须暂停自动加载
        props.active &&
        entries[0]?.isIntersecting &&
        workspace.searchResultsCursor &&
        !workspace.loadingSearchResults &&
        // 出错后停用自动加载（错误未清时重试只会立刻再失败），改由哨兵上的重试按钮手动触发
        !workspace.searchResultsError &&
        props.projectId &&
        workspace.activeResourceId
      ) {
        void workspace.loadSearchResults(props.projectId, workspace.activeResourceId, true)
      }
    },
    { root: resultsListRef.value, rootMargin: '300px' },
  )
  resultsObserver.observe(resultsSentinelRef.value)
}

/** 分页失败后的手动重试（loadSearchResults 会先清空错误再发起 append） */
const retryLoadMore = (): void => {
  if (!props.projectId || !workspace.activeResourceId || workspace.loadingSearchResults) return
  void workspace.loadSearchResults(props.projectId, workspace.activeResourceId, true)
}

watch(
  () => [workspace.searchResultsCursor, workspace.loadingSearchResults],
  async () => {
    await nextTick()
    setupResultsObserver()
  },
)

watch(
  () => workspace.searchResults.length,
  async () => {
    await nextTick()
    setupResultsObserver()
  },
)

// ── 结果列表滚动位置留存：挂起期间若发生 DOM 搬移（停靠↔抽屉断点切换）或浏览器
//    重排，scrollTop 会被重置；用 scroll 监听持续留存，形态变化后由父级驱动回填 ──
let savedResultsScrollTop = 0
const handleResultsScroll = (): void => {
  savedResultsScrollTop = resultsListRef.value?.scrollTop ?? 0
}

/** 回填留存的结果列表滚动位置（父级在面板形态变化后调用） */
const restoreResultsScroll = async (): Promise<void> => {
  await nextTick()
  if (resultsListRef.value) {
    resultsListRef.value.scrollTop = savedResultsScrollTop
  }
}

// 可见性切换后重建分页观察器：挂起期间哨兵几何未变不会再回调，
// 重新 observe 才能在面板恢复可见且哨兵仍于视口内时续上自动加载
watch(
  () => props.active,
  async () => {
    await nextTick()
    setupResultsObserver()
  },
)

// ── 键盘：输入框内 ↑↓ 选择 / Enter 跳转 / Esc 清空 ──
// Ctrl+F 由父级 SegmentPanel 统一分发：本面板跳转后仅隐藏不卸载，
// 自行监听会在隐藏态聚焦不可见的输入框
const focusInput = (): void => {
  searchInputRef.value?.focus()
}

const handleInputKeyDown = (e: KeyboardEvent): void => {
  if (!resultCards.value.length) return
  if (e.key === 'ArrowDown') {
    e.preventDefault()
    selectResult(Math.min(selectedResultIndex.value + 1, resultCards.value.length - 1), false)
  } else if (e.key === 'ArrowUp') {
    e.preventDefault()
    selectResult(Math.max(selectedResultIndex.value - 1, 0), false)
  } else if (e.key === 'Enter') {
    e.preventDefault()
    selectResult(selectedResultIndex.value >= 0 ? selectedResultIndex.value : 0, true)
  }
}

const handleInputKeyUp = (e: KeyboardEvent): void => {
  if (e.key === 'Escape' && workspace.segmentSearch) {
    e.preventDefault()
    workspace.segmentSearch = ''
    workspace.resetSearchResults()
    selectedResultIndex.value = -1
  }
}

// ── 暴露给父组件：面板常驻挂载（收起/隐藏仅转不可见），聚焦与滚动回填由父级驱动 ──
defineExpose({
  focusInput,
  restoreResultsScroll,
})

onMounted(() => {
  resultsListRef.value?.addEventListener('scroll', handleResultsScroll, { passive: true })
  // 从「完全收起」重新挂载且 store 已有结果时，分页观察器需要主动重建
  //（相关 watch 只在值变化时触发，重挂载前后值不变不会fire）
  void nextTick(() => setupResultsObserver())
  // 面板打开即聚焦输入
  focusInput()
})

onUnmounted(() => {
  resultsListRef.value?.removeEventListener('scroll', handleResultsScroll)
  resultsObserver?.disconnect()
  if (searchTimer) clearTimeout(searchTimer)
})
</script>

<template>
  <div
    class="flex h-full min-h-0 flex-col overflow-hidden rounded-lf-card border border-lf-border-soft bg-lf-surface shadow-sm shadow-lf-shadow"
  >
    <!-- 头部：标题 + 输入 + 选项 -->
    <div class="shrink-0 border-b border-lf-border-soft p-3">
      <div class="mb-2.5 flex items-center gap-2">
        <span class="text-[13px] font-semibold text-lf-text-strong">
          {{ t('workspace.segment.searchLocate.title') }}
        </span>
        <NButton
          size="tiny"
          quaternary
          :title="t('workspace.segment.searchReplace.title')"
          @click="emit('openReplace')"
        >
          {{ t('workspace.segment.searchLocate.replaceMode') }}
        </NButton>
        <button
          type="button"
          class="ml-auto flex h-5 w-5 shrink-0 items-center justify-center rounded text-[13px] text-lf-text-subtle transition-colors hover:bg-lf-surface-muted hover:text-lf-text-strong"
          :title="t('workspace.editor.collapsePanel')"
          @click="emit('close')"
        >
          ✕
        </button>
      </div>
      <NInput
        ref="searchInputRef"
        v-model:value="workspace.segmentSearch"
        size="small"
        clearable
        :maxlength="SEGMENT_SEARCH_MAX_LENGTH"
        :count-graphemes="countUnicodeCodePoints"
        show-count
        :placeholder="t('workspace.segment.searchLocate.placeholder')"
        :disabled="!workspace.activeResourceId"
        @keydown="handleInputKeyDown"
        @keyup="handleInputKeyUp"
      />
      <div class="mt-2 flex items-center gap-2">
        <NSelect
          v-model:value="searchFieldValue"
          size="tiny"
          class="min-w-0 flex-1"
          :options="searchFieldOptions"
          :disabled="!workspace.activeResourceId"
        />
        <NButton
          size="tiny"
          class="shrink-0 font-mono font-semibold"
          :type="workspace.segmentSearchMatchMode === 'regex' ? 'primary' : 'default'"
          :secondary="workspace.segmentSearchMatchMode !== 'regex'"
          :disabled="!workspace.activeResourceId"
          :title="matchModeTitle"
          :aria-label="matchModeTitle"
          @mousedown.prevent
          @click="toggleMatchMode"
        >
          .*
        </NButton>
        <NButton
          size="tiny"
          class="shrink-0 font-semibold"
          :type="caseSensitiveOn ? 'primary' : 'default'"
          :secondary="!caseSensitiveOn"
          :disabled="!workspace.activeResourceId"
          :title="t('workspace.segment.searchLocate.caseSensitive')"
          :aria-label="t('workspace.segment.searchLocate.caseSensitive')"
          @mousedown.prevent
          @click="toggleCaseSensitive"
        >
          Aa
        </NButton>
        <NButton
          size="tiny"
          class="shrink-0 font-semibold"
          :type="wholeWordOn ? 'primary' : 'default'"
          :secondary="!wholeWordOn"
          :disabled="!workspace.activeResourceId"
          :title="wholeWordTitle"
          :aria-label="wholeWordTitle"
          @mousedown.prevent
          @click="toggleWholeWord"
        >
          <span class="underline decoration-[1.5px] underline-offset-2">ab</span>
        </NButton>
      </div>
      <div class="mt-2 min-h-4 text-xs text-lf-text-subtle">
        <span v-if="statsLabel">{{ statsLabel }}</span>
        <NTag v-if="workspace.searchResultsError" size="small" type="error" :bordered="false">
          {{ workspace.searchResultsError }}
        </NTag>
      </div>
    </div>

    <!-- 结果列表 -->
    <div ref="resultsListRef" class="lf-scroll min-h-0 flex-1 overflow-y-auto p-2">
      <!-- 空态 -->
      <div
        v-if="!hasQuery"
        class="px-3 py-10 text-center text-xs leading-relaxed text-lf-text-subtle"
      >
        {{ t('workspace.segment.searchLocate.emptyHint') }}
      </div>
      <div
        v-else-if="
          !searchPending &&
          !workspace.loadingSearchResults &&
          !workspace.searchResultsError &&
          resultCards.length === 0
        "
        class="px-3 py-10 text-center text-xs text-lf-text-subtle"
      >
        {{
          t('workspace.segment.searchLocate.noResults', { query: workspace.segmentSearch.trim() })
        }}
      </div>

      <!-- 搜索中骨架（防抖等待期同样视为搜索中） -->
      <div
        v-else-if="(searchPending || workspace.loadingSearchResults) && resultCards.length === 0"
        class="py-10"
      >
        <NSpin :show="true" class="w-full" />
      </div>

      <template v-else>
        <button
          v-for="(card, index) in resultCards"
          :key="card.segment.id"
          type="button"
          :data-result-index="index"
          class="relative mb-1.5 block w-full cursor-pointer rounded-lf-ctl border p-2.5 text-left transition-colors"
          :class="
            index === selectedResultIndex
              ? 'border-brand-500 bg-lf-brand-soft'
              : 'border-lf-border-soft hover:border-lf-border-strong hover:bg-lf-surface-muted/60'
          "
          @click="selectResult(index, true)"
        >
          <div class="mb-1 flex items-center gap-2 text-[11px] text-lf-text-subtle">
            <span class="min-w-0 truncate font-medium text-lf-text-muted">
              {{ card.chapterTitle || t('workspace.segment.chapterAll') }}
            </span>
            <span class="shrink-0 tabular-nums">#{{ card.segment.segment_index }}</span>
            <NTag
              size="tiny"
              class="ml-auto shrink-0"
              :type="statusTagType(card.segment.status)"
              :bordered="false"
            >
              {{ getSegmentStatusLabel(card.segment.status) }}
            </NTag>
          </div>
          <div
            class="line-clamp-2 text-xs leading-relaxed break-words text-lf-text"
            :class="isMutedSnippet(card) ? 'text-lf-text-muted' : ''"
          >
            <template v-if="card.snippet.hit">
              {{ card.snippet.before }}<mark class="search-hit">{{ card.snippet.hit }}</mark
              >{{ card.snippet.after }}
            </template>
            <template v-else>{{ card.snippet.before }}</template>
          </div>
          <div class="mt-1 text-[10.5px] text-lf-text-subtle">
            <span class="rounded border border-lf-border-soft px-1">
              {{ hitFieldLabel(card.hitField) }}
            </span>
          </div>
        </button>

        <!-- 结果分页哨兵 -->
        <div
          v-if="workspace.searchResultsCursor"
          ref="resultsSentinelRef"
          class="flex h-8 items-center justify-center py-2"
        >
          <NSpin v-if="workspace.loadingSearchResults" :show="true" :size="14" />
          <NButton
            v-else-if="workspace.searchResultsError"
            size="tiny"
            quaternary
            :disabled="!props.projectId || !workspace.activeResourceId"
            @click="retryLoadMore"
          >
            {{ t('workspace.segment.searchLocate.retryLoad') }}
          </NButton>
          <span v-else class="text-[10.5px] text-lf-text-subtle">
            {{ t('workspace.segment.searchLocate.loadMoreHint') }}
          </span>
        </div>
      </template>
    </div>

    <!-- 底部提示 -->
    <div
      class="flex shrink-0 items-center gap-3 border-t border-lf-border-soft bg-lf-surface-muted px-3 py-1.5 text-[11px] text-lf-text-muted"
    >
      <span>{{ t('workspace.segment.searchLocate.keyboardHint') }}</span>
      <span class="ml-auto">{{ t('workspace.segment.searchLocate.noAffect') }}</span>
    </div>
  </div>
</template>
