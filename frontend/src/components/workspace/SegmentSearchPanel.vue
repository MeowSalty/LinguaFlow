<script setup lang="ts">
import { NButton, NInput, NSelect, NSpin, NTag } from 'naive-ui'
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'

import type { ApiSchemas } from '@/api/client'
import { buildSearchHighlightRanges, makeSearchSnippet } from '@/composables/useSearchHighlight'
import { getSegmentStatusLabel, statusTagType } from '@/composables/useWorkspaceUtils'
import { useProjectWorkspaceStore } from '@/stores/projectWorkspace'
import { t } from '@/i18n'

type Segment = ApiSchemas['Segment']

const props = defineProps<{
  projectId: number | null
  textRenderMode: 'plaintext' | 'html'
}>()

const emit = defineEmits<{
  /** 已发起定位（主列表窗口将切换）；父级可据此收起移动端容器等 */
  jumped: [segment: Segment]
  /** 打开搜索替换（过渡期复用既有抽屉；后端就绪后的完整模式迁移见 Track C） */
  openReplace: []
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

const hasQuery = computed(() => workspace.segmentSearch.trim().length > 0)

// ── 防抖搜索 ──
let searchTimer: ReturnType<typeof setTimeout> | null = null
const scheduleSearch = (): void => {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => void runSearch(), 300)
}

const runSearch = (): void => {
  if (searchTimer) {
    clearTimeout(searchTimer)
    searchTimer = null
  }
  if (!props.projectId || !workspace.activeResourceId) return
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

// 切换资源时清空结果（新资源的结果无意义）
watch(
  () => workspace.activeResourceId,
  () => {
    selectedResultIndex.value = -1
    workspace.resetSearchResults()
  },
)

// ── 结果卡片 ──
interface ResultCard {
  segment: Segment
  /** 命中展示文本（源文命中展示源文，仅译文命中展示译文，双命中优先源文） */
  snippet: { before: string; hit: string; after: string }
  /** 命中字段：source / target / both */
  hitField: 'source' | 'target' | 'both'
  /** 章节标题（后端 group_key 就绪前为空） */
  chapterTitle: string
}

const resultCards = computed<ResultCard[]>(() =>
  workspace.searchResults.map((segment) => {
    const caseSensitive = workspace.segmentSearchCaseSensitive
    const query = workspace.segmentSearch.trim()
    const inSource =
      workspace.segmentSearchFieldFilter !== 'target' &&
      buildSearchHighlightRanges(segment.source_text, query, caseSensitive).length > 0
    const inTarget =
      workspace.segmentSearchFieldFilter !== 'source' &&
      buildSearchHighlightRanges(segment.target_text ?? '', query, caseSensitive).length > 0
    const hitField = inSource && inTarget ? 'both' : inSource ? 'source' : 'target'
    const displayText = inSource ? segment.source_text : (segment.target_text ?? '')
    const chapterTitle = segment.group_key
      ? (workspace.segmentGroups.find((g) => g.group_key === segment.group_key)?.group_title ?? '')
      : ''
    return {
      segment,
      snippet: makeSearchSnippet(displayText, query, caseSensitive),
      hitField,
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
    default:
      return t('workspace.segment.searchLocate.hitBoth')
  }
}

const chapterCount = computed(
  () => new Set(resultCards.value.map((c) => c.chapterTitle).filter(Boolean)).size,
)

const statsLabel = computed(() => {
  const total = workspace.searchResultsTotal
  if (!hasQuery.value) return null
  if (workspace.loadingSearchResults && resultCards.value.length === 0) {
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
  void workspace.jumpToSegment(props.projectId, workspace.activeResourceId, card.segment)
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
        entries[0]?.isIntersecting &&
        workspace.searchResultsCursor &&
        !workspace.loadingSearchResults &&
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

// ── 键盘：Ctrl+F 聚焦输入；输入框内 ↑↓ 选择 / Enter 跳转 / Esc 清空 ──
const handleGlobalKeyDown = (e: KeyboardEvent): void => {
  if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'f') {
    e.preventDefault()
    focusInput()
  }
}

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

onMounted(() => {
  document.addEventListener('keydown', handleGlobalKeyDown)
  // 面板打开即聚焦输入
  focusInput()
})

onUnmounted(() => {
  document.removeEventListener('keydown', handleGlobalKeyDown)
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
          class="ml-auto"
          :title="t('workspace.segment.searchReplace.title')"
          @click="emit('openReplace')"
        >
          {{ t('workspace.segment.searchLocate.replaceMode') }}
        </NButton>
      </div>
      <NInput
        ref="searchInputRef"
        v-model:value="workspace.segmentSearch"
        size="small"
        clearable
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
          class="shrink-0 font-semibold"
          :type="caseSensitiveOn ? 'primary' : 'default'"
          :secondary="!caseSensitiveOn"
          :disabled="!workspace.activeResourceId"
          :title="t('workspace.segment.searchLocate.caseSensitive')"
          @click="toggleCaseSensitive"
        >
          Aa
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
        v-else-if="!workspace.loadingSearchResults && resultCards.length === 0"
        class="px-3 py-10 text-center text-xs text-lf-text-subtle"
      >
        {{
          t('workspace.segment.searchLocate.noResults', { query: workspace.segmentSearch.trim() })
        }}
      </div>

      <!-- 搜索中骨架 -->
      <div v-else-if="workspace.loadingSearchResults && resultCards.length === 0" class="py-10">
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
            :class="card.hitField === 'source' ? 'text-lf-text-muted' : ''"
          >
            {{ card.snippet.before }}<mark class="search-hit">{{ card.snippet.hit }}</mark
            >{{ card.snippet.after }}
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
