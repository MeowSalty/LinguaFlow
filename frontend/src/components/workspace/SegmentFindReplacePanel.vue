<script setup lang="ts">
import { NAlert, NButton, NInput, NSelect, NSpin, NTag, useDialog, useMessage } from 'naive-ui'
import type { DialogReactive } from 'naive-ui'
import { computed, nextTick, onMounted, onUnmounted, ref, watch } from 'vue'
import type { ApiSchemas } from '@/api/client'
import type { ResourceSegmentQualityCode } from '@/api/projects'
import {
  buildSearchMatch,
  makeSearchSnippet,
  resolveSearchableText,
  type SearchMatchOptions,
} from '@/composables/useSearchHighlight'
import { getQualityCodeLabel, QUALITY_CODE_GROUPS } from '@/composables/useQualityIssues'
import {
  useSegmentReplaceSession,
  type ReplaceSessionInput,
} from '@/composables/useSegmentReplaceSession'
import { getSegmentStatusLabel } from '@/composables/useWorkspaceUtils'
import { useProjectWorkspaceStore, type SegmentStatusFilter } from '@/stores/projectWorkspace'
import { t } from '@/i18n'
import type { SegmentSearchScope } from '@/utils/segmentSearchScope'
import { countUnicodeCodePoints, SEGMENT_SEARCH_MAX_LENGTH } from '@/utils/unicode'
import SearchMatchControls from './SearchMatchControls.vue'
import SegmentReplacePreviewList from './SegmentReplacePreviewList.vue'
import SegmentResultCard from './SegmentResultCard.vue'

type Segment = ApiSchemas['Segment']
const props = defineProps<{
  projectId: number | null
  textRenderMode: 'plaintext' | 'html'
  active: boolean
  selectedSegmentIds: number[]
}>()
const emit = defineEmits<{
  jumped: [segment: Segment]
  close: []
  applied: [payload: { projectId: number; resourceId: number; items: Segment[] }]
}>()
const workspace = useProjectWorkspaceStore()
const dialog = useDialog()
const message = useMessage()
const searchInputRef = ref<InstanceType<typeof NInput> | null>(null)
const replaceInputRef = ref<InstanceType<typeof NInput> | null>(null)
const replaceExpanded = ref(false)
const replaceWith = ref('')
const fieldBeforeReplace = ref<'source' | 'target' | 'both' | null>(null)
const scopeValue = ref('all')
const filtersExpanded = ref(false)
const statusFilter = ref<SegmentStatusFilter>('all')
const qualityFilter = ref<'all' | 'has' | 'none'>('all')
const severityFilter = ref<'all' | 'warning' | 'error'>('all')
const qualityCode = ref<ResourceSegmentQualityCode | null>(null)
const selectedResultIndex = ref(-1)
const expandedSearchSegmentIds = ref(new Set<number>())
const resultsListRef = ref<HTMLElement | null>(null)
const resultsSentinelRef = ref<HTMLElement | null>(null)
const searchPending = ref(false)
let searchTimer: ReturnType<typeof setTimeout> | null = null
let resultsObserver: IntersectionObserver | null = null
let confirmation: DialogReactive | null = null
let pageRequest: { identity: string; promise: Promise<void> } | null = null
const scrollPositions = { search: 0, preview: 0 }

// 使用与现有搜索 API 调用相同的有效查找值，替换不会另读一份未规范化的输入。
const find = computed(() => workspace.segmentSearch.trim())
const hasQuery = computed(() => find.value.length > 0)
const validLength = computed(
  () => countUnicodeCodePoints(workspace.segmentSearch) <= SEGMENT_SEARCH_MAX_LENGTH,
)
const emptySelection = computed(
  () => scopeValue.value === 'selected' && props.selectedSegmentIds.length === 0,
)
const searchFieldOptions = computed(() => [
  { label: t('workspace.segment.searchLocate.searchFieldBoth'), value: 'both' },
  { label: t('workspace.segment.searchLocate.searchFieldSource'), value: 'source' },
  { label: t('workspace.segment.searchLocate.searchFieldTarget'), value: 'target' },
])
const replaceFieldOptions = computed(() =>
  searchFieldOptions.value.filter((option) => option.value === 'target'),
)
const scopeOptions = computed(() => [
  { label: t('workspace.segment.findReplace.scopeResource'), value: 'all' },
  {
    label: t('workspace.segment.findReplace.scopeSelected', {
      count: props.selectedSegmentIds.length,
    }),
    value: 'selected',
  },
  ...workspace.segmentGroups
    .filter((group) => group.group_key)
    .map((group) => ({
      label: group.group_title || group.group_key,
      value: `chapter:${group.group_key}`,
    })),
])
const statusOptions = computed(() => [
  { label: t('workspace.filters.allStatuses'), value: 'all' },
  ...(['pending', 'translated', 'edited', 'approved', 'rejected'] as const).map((value) => ({
    label: getSegmentStatusLabel(value),
    value,
  })),
])
const qualityOptions = computed(() => [
  { label: t('workspace.segment.findReplace.allQuality'), value: 'all' },
  { label: t('workspace.filters.qualityIssuesHas'), value: 'has' },
  { label: t('workspace.filters.qualityIssuesNone'), value: 'none' },
])
const severityOptions = computed(() => [
  { label: t('workspace.segment.findReplace.allSeverity'), value: 'all' },
  { label: t('workspace.segment.qualityWarning'), value: 'warning' },
  { label: t('workspace.segment.qualityError'), value: 'error' },
])
const codeOptions = computed(() =>
  QUALITY_CODE_GROUPS.map((group) => ({
    type: 'group' as const,
    key: group.key,
    label: t(`workspace.segment.qualityCodeGroups.${group.key}`),
    children: group.codes.map((value) => ({ label: getQualityCodeLabel(value), value })),
  })),
)
const activeFilterLabels = computed(() =>
  [
    statusFilter.value !== 'all' ? getSegmentStatusLabel(statusFilter.value) : '',
    qualityFilter.value !== 'all'
      ? qualityOptions.value.find((option) => option.value === qualityFilter.value)?.label
      : '',
    severityFilter.value !== 'all'
      ? severityOptions.value.find((option) => option.value === severityFilter.value)?.label
      : '',
    qualityCode.value ? getQualityCodeLabel(qualityCode.value) : '',
  ].filter(Boolean),
)
const scopeLabel = computed(() =>
  [
    scopeOptions.value.find((option) => option.value === scopeValue.value)?.label,
    ...activeFilterLabels.value,
  ]
    .filter(Boolean)
    .join(' · '),
)
const searchScope = computed<SegmentSearchScope>(() => ({
  filters: {
    group_key: scopeValue.value.startsWith('chapter:') ? scopeValue.value.slice(8) : undefined,
    status: statusFilter.value === 'all' ? undefined : statusFilter.value,
    quality_issues: qualityFilter.value === 'all' ? undefined : qualityFilter.value,
    quality_severity: severityFilter.value === 'all' ? undefined : severityFilter.value,
    quality_code: qualityCode.value ?? undefined,
  },
  segmentIds:
    scopeValue.value === 'selected'
      ? [...props.selectedSegmentIds].sort((a, b) => a - b)
      : undefined,
}))
const searchMatchOptions = computed<SearchMatchOptions>(() => ({
  caseSensitive: workspace.segmentSearchCaseSensitive,
  wholeWord: workspace.segmentSearchWholeWord,
  matchMode: workspace.segmentSearchMatchMode,
}))
const resourceContext = computed(() =>
  props.projectId && workspace.activeResourceId
    ? { projectId: props.projectId, resourceId: workspace.activeResourceId }
    : null,
)
const input = computed<ReplaceSessionInput | null>(() =>
  resourceContext.value && hasQuery.value && validLength.value && !emptySelection.value
    ? {
        ...resourceContext.value,
        find: find.value,
        replaceWith: replaceWith.value,
        matchMode: workspace.segmentSearchMatchMode,
        caseSensitive: workspace.segmentSearchCaseSensitive,
        wholeWord: workspace.segmentSearchWholeWord,
        searchField: workspace.segmentSearchFieldFilter,
        scope: searchScope.value,
        scopeLabel: scopeLabel.value,
      }
    : null,
)
const session = useSegmentReplaceSession({
  snapshot: () => input.value,
  context: () => resourceContext.value,
  getOperation: () => workspace.lastSearchReplaceOperationId,
  onOperation: (id, context) => {
    if (context.projectId === props.projectId && context.resourceId === workspace.activeResourceId)
      workspace.lastSearchReplaceOperationId = id
  },
  onApplied: (payload) => {
    emit('applied', payload)
    if (payload.projectId === props.projectId && payload.resourceId === workspace.activeResourceId)
      scheduleSearch()
  },
})
const {
  preview,
  previewStale,
  previewing,
  applying,
  undoing,
  busy,
  errorMessage,
  progress,
  lastResult,
  frozenScopeLabel,
  canApply,
} = session
const writing = computed(() => applying.value || undoing.value)
const showingPreview = computed(
  () => replaceExpanded.value && preview.value !== null && !previewStale.value,
)
const resultSummary = computed(() => {
  if (!lastResult.value) return ''
  return 'applied_count' in lastResult.value
    ? t('workspace.segment.searchReplace.applySuccess', {
        applied: lastResult.value.applied_count,
        skipped: lastResult.value.skipped_count,
      })
    : t('workspace.segment.searchReplace.undoSuccess', {
        undone: lastResult.value.undone_count,
        skipped: lastResult.value.skipped_count,
      })
})

function clearFilters(): void {
  statusFilter.value = 'all'
  qualityFilter.value = 'all'
  severityFilter.value = 'all'
  qualityCode.value = null
}
watch(qualityFilter, (value) => {
  if (value === 'none') {
    severityFilter.value = 'all'
    qualityCode.value = null
  }
})
watch([severityFilter, qualityCode], () => {
  if ((severityFilter.value !== 'all' || qualityCode.value) && qualityFilter.value === 'none')
    qualityFilter.value = 'all'
})
function cancelScheduledSearch(): void {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = null
  searchPending.value = false
}
function scheduleSearch(): void {
  cancelScheduledSearch()
  pageRequest = null
  workspace.resetSearchResults()
  selectedResultIndex.value = -1
  expandedSearchSegmentIds.value = new Set()
  if (!hasQuery.value || !validLength.value || !resourceContext.value) return
  searchPending.value = true
  searchTimer = setTimeout(() => void runSearch(), 300)
}
async function runSearch(): Promise<void> {
  cancelScheduledSearch()
  const context = resourceContext.value
  if (!context || !hasQuery.value || !validLength.value) return
  await workspace.loadSearchResults(context.projectId, context.resourceId, false, searchScope.value)
}
function loadMore(): Promise<void> {
  const context = resourceContext.value
  const identity = searchIdentity.value
  if (pageRequest?.identity === identity) return pageRequest.promise
  if (!context || workspace.loadingSearchResults || !workspace.searchResultsCursor)
    return Promise.resolve()
  const request = {
    identity,
    promise: workspace.loadSearchResults(
      context.projectId,
      context.resourceId,
      true,
      searchScope.value,
    ),
  }
  pageRequest = request
  void request.promise.finally(() => {
    if (pageRequest === request) pageRequest = null
  })
  return request.promise
}
const searchIdentity = computed(() =>
  JSON.stringify([
    resourceContext.value,
    find.value,
    workspace.segmentSearchFieldFilter,
    searchMatchOptions.value,
    searchScope.value,
    validLength.value,
  ]),
)
watch(searchIdentity, scheduleSearch)
watch(
  () => [props.projectId, workspace.activeResourceId],
  () => {
    scopeValue.value = 'all'
    clearFilters()
    selectedResultIndex.value = -1
    confirmation?.destroy()
    confirmation = null
  },
)
watch(
  input,
  () => {
    confirmation?.destroy()
    confirmation = null
  },
  { flush: 'sync' },
)

// 仅比较同一已知段落的内容变化；分页/定位增加新行不会误使预览过期。
watch(
  () =>
    workspace.segments.map((segment) => ({
      id: segment.id,
      content: JSON.stringify([segment.target_text, segment.status, segment.quality_issues]),
    })),
  (next, previous) => {
    const before = new Map(previous.map((item) => [item.id, item.content]))
    if (next.some((item) => before.has(item.id) && before.get(item.id) !== item.content)) {
      session.invalidate()
      confirmation?.destroy()
      confirmation = null
    }
  },
)

const resultCards = computed(() =>
  workspace.searchResults.map((segment) => {
    const source = resolveSearchableText(segment.source_text, props.textRenderMode)
    const target = resolveSearchableText(segment.target_text ?? '', props.textRenderMode)
    const sourceRanges =
      workspace.segmentSearchFieldFilter !== 'target'
        ? buildSearchMatch(source, find.value, searchMatchOptions.value).ranges
        : []
    const targetRanges =
      workspace.segmentSearchFieldFilter !== 'source'
        ? buildSearchMatch(target, find.value, searchMatchOptions.value).ranges
        : []
    const inSource = sourceRanges.length > 0
    const inTarget = targetRanges.length > 0
    const hitField =
      inSource && inTarget ? 'both' : inSource ? 'source' : inTarget ? 'target' : 'generic'
    const displayField = inSource ? ('source' as const) : inTarget ? ('target' as const) : null
    const display = inSource
      ? source
      : inTarget || workspace.segmentSearchFieldFilter === 'target'
        ? target
        : source
    const expanded = expandedSearchSegmentIds.value.has(segment.id)
    const snippet = makeSearchSnippet(display, find.value, searchMatchOptions.value, expanded)
    const targetSnippet =
      inSource && inTarget
        ? makeSearchSnippet(target, find.value, searchMatchOptions.value, expanded)
        : null
    return {
      segment,
      hitField,
      displayField,
      chapter: workspace.segmentGroups.find((group) => group.group_key === segment.group_key)
        ?.group_title,
      snippet,
      targetSnippet,
      omittedMatchCount: snippet.omittedMatchCount + (targetSnippet?.omittedMatchCount ?? 0),
      sourceMatchCount: sourceRanges.filter((range) => range.end > range.start).length,
      targetMatchCount: targetRanges.filter((range) => range.end > range.start).length,
    }
  }),
)
const statsLabel = computed(() => {
  if (showingPreview.value && preview.value)
    return t('workspace.segment.findReplace.previewStats', {
      count: preview.value.matched_segment_count,
      matches: preview.value.total_replacements,
    })
  if (searchPending.value || (workspace.loadingSearchResults && !resultCards.value.length))
    return t('workspace.segment.searchLocate.loading')
  return workspace.searchResultsTotal !== null
    ? t('workspace.segment.findReplace.searchStats', { count: workspace.searchResultsTotal })
    : ''
})
function jumpTo(segment: Segment, field: 'source' | 'target' | null): void {
  if (!resourceContext.value) return
  const filtersBefore = JSON.stringify([
    workspace.segmentStatusFilter,
    workspace.segmentQualityIssuesFilter,
    workspace.segmentQualitySeverityFilter,
    workspace.segmentQualityCodeFilter,
  ])
  void workspace.jumpToSegment(
    resourceContext.value.projectId,
    resourceContext.value.resourceId,
    segment,
    field,
  )
  const filtersAfter = JSON.stringify([
    workspace.segmentStatusFilter,
    workspace.segmentQualityIssuesFilter,
    workspace.segmentQualitySeverityFilter,
    workspace.segmentQualityCodeFilter,
  ])
  if (filtersBefore !== filtersAfter)
    message.info(t('workspace.segment.findReplace.clearedMainFilters'))
  emit('jumped', segment)
}
function selectResult(index: number, jump = true): void {
  const card = resultCards.value[index]
  if (!card) return
  selectedResultIndex.value = index
  resultsListRef.value
    ?.querySelector(`[data-result-index="${index}"]`)
    ?.scrollIntoView({ block: 'nearest' })
  if (jump) jumpTo(card.segment, card.displayField)
}
function toggleSearchContext(segmentId: number): void {
  const next = new Set(expandedSearchSegmentIds.value)
  if (next.has(segmentId)) next.delete(segmentId)
  else next.add(segmentId)
  expandedSearchSegmentIds.value = next
}
async function navigate(delta: number, jump = true): Promise<void> {
  const identity = searchIdentity.value
  let index =
    selectedResultIndex.value < 0
      ? delta > 0
        ? 0
        : resultCards.value.length - 1
      : selectedResultIndex.value + delta
  if (index >= resultCards.value.length && workspace.searchResultsCursor) {
    await loadMore()
    if (identity !== searchIdentity.value || workspace.searchResultsError || !props.active) return
  }
  if (!resultCards.value.length) return
  index = Math.max(0, Math.min(index, resultCards.value.length - 1))
  selectResult(index, jump)
}
function handleInputKeyDown(event: KeyboardEvent): void {
  if (event.isComposing || showingPreview.value) return
  if (event.key === 'Enter' || event.key === 'ArrowDown' || event.key === 'ArrowUp') {
    event.preventDefault()
    void navigate(event.key === 'ArrowUp' || event.shiftKey ? -1 : 1, event.key === 'Enter')
  }
}
async function previewChanges(segmentId?: number): Promise<void> {
  if (workspace.segmentSearchFieldFilter === 'source') workspace.segmentSearchFieldFilter = 'target'
  await session.previewChanges(segmentId)
}
function applyChanges(segmentId?: number): void {
  const snapshot = session.getApplyConfirmation(segmentId)
  if (!snapshot) return
  const view = dialog.warning({
    title: t('workspace.segment.findReplace.confirmTitle'),
    content: t('workspace.segment.findReplace.confirmContent', {
      resource: workspace.activeResource?.name ?? '',
      scope: snapshot.scopeLabel,
      count: snapshot.matchedSegmentCount,
      matches: snapshot.totalReplacements,
      find: snapshot.find,
      replace: snapshot.replaceWith || t('workspace.segment.findReplace.deleteMatches'),
    }),
    positiveText: t('workspace.segment.findReplace.confirmApply'),
    negativeText: t('common.cancel'),
    onPositiveClick: async () => {
      const result = await session.applyPreview(snapshot.token, snapshot.segmentId)
      if (
        result &&
        snapshot.projectId === props.projectId &&
        snapshot.resourceId === workspace.activeResourceId
      )
        message.success(
          t('workspace.segment.searchReplace.applySuccess', {
            applied: result.applied_count,
            skipped: result.skipped_count,
          }),
        )
      return true
    },
    onAfterLeave: () => {
      if (confirmation === view) confirmation = null
    },
  })
  confirmation = view
}
async function applyPreviewSegment(segmentId: number): Promise<void> {
  if (busy.value) return
  applyChanges(segmentId)
}
function undoChanges(): void {
  const snapshot = session.getUndoConfirmation()
  if (!snapshot) return
  const view = dialog.warning({
    title: t('workspace.segment.searchReplace.undoConfirmTitle'),
    content: t('workspace.segment.searchReplace.undoConfirmContent'),
    positiveText: t('common.confirm'),
    negativeText: t('common.cancel'),
    onPositiveClick: async () => {
      const result = await session.undo(snapshot.token)
      if (
        result &&
        snapshot.projectId === props.projectId &&
        snapshot.resourceId === workspace.activeResourceId
      )
        message.success(
          t('workspace.segment.searchReplace.undoSuccess', {
            undone: result.undone_count,
            skipped: result.skipped_count,
          }),
        )
      return true
    },
    onAfterLeave: () => {
      if (confirmation === view) confirmation = null
    },
  })
  confirmation = view
}
function locatePreview(id: number): void {
  const segment = session.getPreviewSegment(id)
  if (segment) jumpTo(segment, 'target')
}
function setReplaceExpanded(expanded: boolean): void {
  if (expanded === replaceExpanded.value) return
  if (expanded) {
    fieldBeforeReplace.value = workspace.segmentSearchFieldFilter
    workspace.segmentSearchFieldFilter = 'target'
    replaceExpanded.value = true
    void nextTick(() => replaceInputRef.value?.focus())
    return
  }
  // Leaving replace mode returns to the search session. Do not carry a frozen
  // replacement preview or its stale warning into the search-only view.
  if (preview.value || previewStale.value) session.clearPreview()
  replaceExpanded.value = false
  if (fieldBeforeReplace.value) workspace.segmentSearchFieldFilter = fieldBeforeReplace.value
  fieldBeforeReplace.value = null
}
function toggleReplace(): void {
  setReplaceExpanded(!replaceExpanded.value)
}
function openReplace(): void {
  setReplaceExpanded(true)
}
function focusInput(): void {
  searchInputRef.value?.focus()
}
function handleClose(): void {
  emit('close')
}
function handlePanelKeyDown(event: KeyboardEvent): void {
  if (event.key !== 'Escape' || event.isComposing) return
  event.stopPropagation()
  event.preventDefault()
  if (confirmation) {
    confirmation.destroy()
    confirmation = null
    return
  }
  handleClose()
}
function saveScroll(): void {
  scrollPositions[showingPreview.value ? 'preview' : 'search'] =
    resultsListRef.value?.scrollTop ?? 0
}
async function restoreResultsScroll(): Promise<void> {
  await nextTick()
  if (resultsListRef.value)
    resultsListRef.value.scrollTop = scrollPositions[showingPreview.value ? 'preview' : 'search']
}
function observeResults(): void {
  resultsObserver?.disconnect()
  if (!resultsSentinelRef.value || showingPreview.value || previewing.value) return
  resultsObserver = new IntersectionObserver(
    (entries) => {
      if (
        props.active &&
        !showingPreview.value &&
        !previewing.value &&
        entries[0]?.isIntersecting &&
        !workspace.searchResultsError
      )
        void loadMore()
    },
    { root: resultsListRef.value, rootMargin: '200px' },
  )
  resultsObserver.observe(resultsSentinelRef.value)
}
watch(
  [
    () => workspace.searchResultsCursor,
    () => workspace.loadingSearchResults,
    () => props.active,
    showingPreview,
    previewing,
  ],
  async () => {
    await nextTick()
    observeResults()
  },
)
watch(showingPreview, () => {
  void restoreResultsScroll()
})
defineExpose({ focusInput, openReplace, restoreResultsScroll })
onMounted(() => {
  focusInput()
  if (hasQuery.value) scheduleSearch()
  void nextTick(observeResults)
})
onUnmounted(() => {
  cancelScheduledSearch()
  resultsObserver?.disconnect()
  confirmation?.destroy()
})
</script>

<template>
  <section
    class="flex h-full min-h-0 flex-col overflow-hidden rounded-lf-card border border-lf-border-soft bg-lf-surface shadow-sm shadow-lf-shadow"
    :aria-label="t('workspace.segment.findReplace.title')"
    @keydown="handlePanelKeyDown"
  >
    <div class="shrink-0 border-b border-lf-border-soft p-3">
      <div class="mb-2.5 flex items-center justify-between gap-2">
        <span class="text-[13px] font-semibold text-lf-text-strong">{{
          t('workspace.segment.findReplace.title')
        }}</span>
        <NButton
          size="tiny"
          quaternary
          class="shrink-0"
          :aria-label="t('workspace.editor.collapsePanel')"
          :title="t('workspace.editor.collapsePanel')"
          @click="handleClose"
          ><template #icon><IconCarbonClose /></template
        ></NButton>
      </div>
      <div class="flex items-start gap-1.5">
        <NButton
          size="small"
          quaternary
          class="mt-0.5 shrink-0"
          :class="replaceExpanded ? 'bg-lf-brand-soft text-brand-700' : 'text-lf-text-muted'"
          :aria-expanded="replaceExpanded"
          :aria-label="t('workspace.segment.findReplace.toggleReplace')"
          :title="t('workspace.segment.findReplace.toggleReplace')"
          @click="toggleReplace"
        >
          <template #icon
            ><IconCarbonChevronDown v-if="replaceExpanded" /><IconCarbonChevronRight v-else
          /></template>
        </NButton>
        <div class="min-w-0 flex-1">
          <NInput
            ref="searchInputRef"
            v-model:value="workspace.segmentSearch"
            size="small"
            clearable
            :maxlength="SEGMENT_SEARCH_MAX_LENGTH"
            :count-graphemes="countUnicodeCodePoints"
            :placeholder="t('workspace.segment.findReplace.findPlaceholder')"
            :aria-label="t('workspace.segment.searchReplace.findLabel')"
            :disabled="!resourceContext || writing"
            @keydown="handleInputKeyDown"
          />
          <div v-if="replaceExpanded" class="mt-1.5">
            <NInput
              ref="replaceInputRef"
              v-model:value="replaceWith"
              size="small"
              :placeholder="t('workspace.segment.searchReplace.replacePlaceholder')"
              :aria-label="t('workspace.segment.searchReplace.replaceLabel')"
              :disabled="!resourceContext || writing"
            />
            <p class="mt-1 text-[11px] leading-4 text-lf-text-subtle">
              {{
                workspace.segmentSearchMatchMode === 'regex'
                  ? t('workspace.segment.findReplace.targetOnlyRegex')
                  : t('workspace.segment.findReplace.targetOnly')
              }}
            </p>
          </div>
        </div>
      </div>
      <div
        class="mt-2 flex items-center justify-between gap-2 rounded-lf-ctl bg-lf-surface-muted/60 px-2 py-1"
      >
        <span class="text-[11px] font-medium text-lf-text-subtle">{{
          t('workspace.segment.searchLocate.matchMode')
        }}</span>
        <SearchMatchControls
          v-model:case-sensitive="workspace.segmentSearchCaseSensitive"
          v-model:whole-word="workspace.segmentSearchWholeWord"
          v-model:match-mode="workspace.segmentSearchMatchMode"
          :disabled="!resourceContext || writing"
        />
      </div>
      <div class="mt-2 grid grid-cols-[auto_minmax(0,1fr)] items-center gap-x-2 gap-y-2">
        <span class="text-xs text-lf-text-subtle">{{
          t('workspace.segment.findReplace.field')
        }}</span>
        <NSelect
          v-model:value="workspace.segmentSearchFieldFilter"
          size="small"
          :options="replaceExpanded ? replaceFieldOptions : searchFieldOptions"
          :aria-label="t('workspace.segment.searchLocate.searchField')"
          :disabled="replaceExpanded || !resourceContext || writing"
        />
        <span class="text-xs text-lf-text-subtle">{{
          t('workspace.segment.findReplace.scope')
        }}</span>
        <div class="flex min-w-0 items-center gap-1">
          <NSelect
            v-model:value="scopeValue"
            class="min-w-0 flex-1"
            size="small"
            filterable
            :options="scopeOptions"
            :aria-label="t('workspace.segment.searchReplace.scopeLabel')"
            :disabled="!resourceContext || writing"
          />
          <NButton
            size="small"
            quaternary
            :type="activeFilterLabels.length ? 'primary' : 'default'"
            :class="activeFilterLabels.length ? 'bg-lf-brand-soft' : ''"
            :aria-expanded="filtersExpanded"
            :aria-label="t('workspace.segment.findReplace.filters')"
            :title="t('workspace.segment.findReplace.filters')"
            @click="filtersExpanded = !filtersExpanded"
          >
            <template #icon><IconCarbonFilter /></template>
            <span
              v-if="activeFilterLabels.length"
              class="ml-0.5 inline-flex min-w-4 items-center justify-center rounded-full bg-brand-500 px-1 text-[10px] leading-4 text-white"
              >{{ activeFilterLabels.length }}</span
            >
          </NButton>
        </div>
      </div>
      <div
        v-if="filtersExpanded"
        class="mt-2 space-y-2 rounded-lf-ctl border border-lf-border-soft bg-lf-surface-muted/70 p-2"
      >
        <NSelect
          v-model:value="statusFilter"
          size="small"
          :options="statusOptions"
          :aria-label="t('workspace.segment.columns.status')"
          :disabled="writing"
        />
        <div class="grid grid-cols-2 gap-2">
          <NSelect
            v-model:value="qualityFilter"
            size="small"
            :options="qualityOptions"
            :aria-label="t('workspace.filters.qualityLabel')"
            :disabled="writing"
          /><NSelect
            v-model:value="severityFilter"
            size="small"
            :options="severityOptions"
            :aria-label="t('workspace.segment.findReplace.severity')"
            :disabled="writing"
          />
        </div>
        <NSelect
          v-model:value="qualityCode"
          size="small"
          clearable
          filterable
          :options="codeOptions"
          :placeholder="t('workspace.segment.qualityCodePlaceholder')"
          :aria-label="t('workspace.segment.qualityCodePlaceholder')"
          :disabled="writing"
        />
      </div>
      <div
        v-if="activeFilterLabels.length"
        class="mt-2 flex flex-wrap items-center gap-1 rounded-lf-ctl bg-lf-surface-muted/50 px-2 py-1.5"
      >
        <NTag v-for="label in activeFilterLabels" :key="label" size="small" :bordered="false">{{
          label
        }}</NTag>
        <NButton size="tiny" quaternary :disabled="writing" @click="clearFilters">{{
          t('workspace.filters.clearQuality')
        }}</NButton>
      </div>
      <p
        v-if="workspace.segmentSearch !== find && hasQuery"
        class="mt-2 text-[11px] text-lf-text-subtle"
      >
        {{ t('workspace.segment.findReplace.trimHint') }}
      </p>
      <p v-if="emptySelection" class="mt-2 text-xs text-lf-text-subtle">
        {{ t('workspace.segment.findReplace.emptySelection') }}
      </p>
      <NAlert v-if="!validLength" type="error" :bordered="false" class="mt-2">{{
        t('api.errors.segmentSearchTooLong', { max: SEGMENT_SEARCH_MAX_LENGTH })
      }}</NAlert>
    </div>
    <div v-if="errorMessage || previewStale" class="shrink-0 border-b border-lf-border-soft p-3">
      <NAlert :type="errorMessage ? 'error' : 'warning'" :bordered="false">{{
        errorMessage || t('workspace.segment.searchReplace.stalePreview')
      }}</NAlert>
    </div>
    <div
      class="flex min-h-9 shrink-0 flex-wrap items-center gap-2 border-b border-lf-border-soft bg-lf-surface-muted/30 px-3 py-1.5 text-xs text-lf-text-muted"
    >
      <span aria-live="polite" class="font-medium text-lf-text-muted">{{ statsLabel }}</span>
      <NButton
        v-if="showingPreview"
        size="tiny"
        quaternary
        class="ml-auto"
        :disabled="writing"
        @click="session.clearPreview"
        >{{ t('workspace.segment.findReplace.backToSearch') }}</NButton
      >
      <div v-else class="ml-auto flex gap-1">
        <NButton
          size="tiny"
          quaternary
          :disabled="!resultCards.length || workspace.loadingSearchResults"
          :aria-label="t('workspace.segment.findReplace.previous')"
          :title="t('workspace.segment.findReplace.previous')"
          @click="navigate(-1)"
          ><template #icon><IconCarbonArrowUp /></template
        ></NButton>
        <NButton
          size="tiny"
          quaternary
          :disabled="!resultCards.length || workspace.loadingSearchResults"
          :aria-label="t('workspace.segment.findReplace.next')"
          :title="t('workspace.segment.findReplace.next')"
          @click="navigate(1)"
          ><template #icon><IconCarbonArrowDown /></template
        ></NButton>
      </div>
      <span v-if="showingPreview" class="basis-full text-[11px] text-lf-text-subtle">{{
        frozenScopeLabel
      }}</span>
    </div>
    <div
      ref="resultsListRef"
      class="lf-scroll min-h-0 flex-1 overflow-y-auto"
      @scroll.passive="saveScroll"
    >
      <div
        v-if="lastResult"
        class="border-b border-lf-border-soft bg-lf-surface-muted px-3 py-2 text-xs"
      >
        <p aria-live="polite" class="text-lf-text">{{ resultSummary }}</p>
        <details v-if="lastResult.skipped?.length" class="mt-1 text-lf-text-muted">
          <summary class="cursor-pointer">
            {{ t('workspace.segment.findReplace.skipDetails') }}
          </summary>
          <p v-for="item in lastResult.skipped" :key="item.segment_id" class="mt-1">
            #{{ item.segment_id }} ·
            {{ t(`workspace.segment.searchReplace.skipReasons.${item.reason}`) }}
          </p>
        </details>
      </div>
      <SegmentReplacePreviewList
        v-if="showingPreview && preview"
        :preview="preview"
        :text-render-mode="textRenderMode"
        :find="find"
        :replace-with="replaceWith"
        :match-options="searchMatchOptions"
        @locate="locatePreview"
        @apply-segment="applyPreviewSegment"
      />
      <template v-else>
        <div v-if="workspace.searchResultsError" class="p-3">
          <NAlert type="error" :bordered="false">{{ workspace.searchResultsError }}</NAlert
          ><NButton
            size="small"
            class="mt-2"
            :disabled="workspace.loadingSearchResults"
            @click="workspace.searchResultsCursor ? loadMore() : runSearch()"
            >{{ t('workspace.segment.searchLocate.retryLoad') }}</NButton
          >
        </div>
        <div v-if="!hasQuery" class="px-4 py-10 text-center text-xs text-lf-text-subtle">
          {{ t('workspace.segment.searchLocate.emptyHint') }}
        </div>
        <div
          v-else-if="(searchPending || workspace.loadingSearchResults) && !resultCards.length"
          class="flex justify-center py-10"
        >
          <NSpin size="small" />
        </div>
        <div
          v-else-if="!resultCards.length && !workspace.searchResultsError"
          class="px-4 py-10 text-center text-xs text-lf-text-subtle"
        >
          {{
            emptySelection
              ? t('workspace.segment.findReplace.emptySelection')
              : t('workspace.segment.searchLocate.noResults', { query: find })
          }}
        </div>
        <div v-for="(card, index) in resultCards" :key="card.segment.id" :data-result-index="index">
          <SegmentResultCard
            mode="search"
            :segment="card.segment"
            :segment-id="card.segment.id"
            :segment-index="card.segment.segment_index"
            :chapter="card.chapter"
            :display-field="card.displayField"
            :snippet="card.snippet"
            :target-snippet="card.targetSnippet"
            :source-match-count="card.sourceMatchCount"
            :target-match-count="card.targetMatchCount"
            :selected="index === selectedResultIndex"
            :expanded="expandedSearchSegmentIds.has(card.segment.id)"
            :omitted-match-count="card.omittedMatchCount"
            :replace-enabled="
              replaceExpanded &&
              card.hitField !== 'source' &&
              workspace.segmentSearchFieldFilter !== 'source'
            "
            :busy="busy || !input"
            @select="selectResult(index)"
            @toggle-context="toggleSearchContext(card.segment.id)"
            @preview-segment="previewChanges(card.segment.id)"
          />
        </div>
        <div
          v-if="workspace.searchResultsCursor"
          ref="resultsSentinelRef"
          class="flex min-h-10 items-center justify-center p-2"
        >
          <NSpin v-if="workspace.loadingSearchResults" :size="14" /><NButton
            v-else
            size="tiny"
            quaternary
            @click="loadMore"
            >{{ t('workspace.segment.findReplace.loadMore') }}</NButton
          >
        </div>
      </template>
    </div>
    <div
      v-if="replaceExpanded || workspace.lastSearchReplaceOperationId"
      class="shrink-0 space-y-2 border-t border-lf-border-soft bg-lf-surface p-3"
    >
      <p v-if="previewing" aria-live="polite" class="text-xs text-lf-text-subtle">
        {{ t('workspace.segment.findReplace.collecting', { count: progress }) }}
      </p>
      <NButton
        v-if="replaceExpanded && !showingPreview"
        block
        type="primary"
        size="small"
        :loading="previewing"
        :disabled="!input || busy"
        @click="previewChanges()"
        >{{
          workspace.segmentSearchFieldFilter === 'source'
            ? t('workspace.segment.findReplace.switchTargetPreview')
            : previewStale
              ? t('workspace.segment.findReplace.previewAgain')
              : t('workspace.segment.findReplace.previewScope')
        }}</NButton
      >
      <NButton
        v-if="showingPreview"
        block
        type="primary"
        size="small"
        :loading="applying"
        :disabled="!canApply"
        @click="applyChanges()"
        >{{
          t('workspace.segment.findReplace.applyCount', {
            count: preview?.matched_segment_count ?? 0,
          })
        }}</NButton
      >
      <NButton
        v-if="workspace.lastSearchReplaceOperationId"
        block
        quaternary
        size="tiny"
        :loading="undoing"
        :disabled="busy"
        @click="undoChanges"
        >{{ t('workspace.segment.findReplace.undoOperation') }}</NButton
      >
    </div>
    <div
      class="shrink-0 border-t border-lf-border-soft bg-lf-surface-muted/60 px-3 py-1.5 text-[11px] text-lf-text-subtle"
    >
      {{ t('workspace.segment.findReplace.keyboardHint') }}
    </div>
  </section>
</template>
