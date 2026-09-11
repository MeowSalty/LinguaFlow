import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import {
  type ApiSchemas,
  fetchResourceSegments,
  setResourceSegmentIssueDisposition as setIssueDispositionRequest,
  updateResourceSegment as updateResourceSegmentRequest,
} from '@/api/client'
import { fetchSegmentGroups, type ResourceSegmentGroup } from '@/api/epub'
import type { ResourceSegmentQualityCode } from '@/api/projects'
import { t } from '@/i18n'
import { extractErrorMessage } from '@/utils/errors'

export type { ResourceSegmentGroup }

type Segment = ApiSchemas['Segment']
type SegmentUpdatePayload = ApiSchemas['ResourceSegmentUpdateRequest']

export type SegmentStatusFilter =
  | 'pending'
  | 'translated'
  | 'edited'
  | 'approved'
  | 'rejected'
  | 'all'

export type SegmentQualityIssuesFilter = 'has' | 'none' | 'all'
export type SegmentQualitySeverityFilter = 'warning' | 'error' | 'all'
export type SegmentQualityCodeFilter = ResourceSegmentQualityCode | 'all'
export type SegmentSearchFieldFilter = 'source' | 'target' | 'both'

export interface SegmentProgress {
  pending: number
  translated: number
  edited: number
  approved: number
  rejected: number
  total: number
}

export const useSegmentStore = defineStore('segment', () => {
  // ── 段落状态 ──
  const segments = ref<Segment[]>([])
  const segmentsCursor = ref<string | null>(null)
  /** 向上翻页游标（direction=desc 语义：取该 segment_index 之前的一页）；null = 窗口顶无更早内容 */
  const segmentsPrevCursor = ref<string | null>(null)
  const loadingSegmentsUp = ref(false)
  const segmentsTotal = ref<number | null>(null)
  const loadingSegments = ref(false)
  const segmentsError = ref<string | null>(null)
  const editingSegmentIds = ref<number[]>([])
  const actionError = ref<string | null>(null)

  // ── 筛选器 ──
  const segmentSearch = ref('')
  const segmentStatusFilter = ref<SegmentStatusFilter>('all')
  const segmentQualityIssuesFilter = ref<SegmentQualityIssuesFilter>('all')
  const segmentQualitySeverityFilter = ref<SegmentQualitySeverityFilter>('all')
  const segmentQualityCodeFilter = ref<SegmentQualityCodeFilter>('all')
  const segmentSearchFieldFilter = ref<SegmentSearchFieldFilter>('both')
  const segmentSearchCaseSensitive = ref(true)

  // ── EPUB 章节导航状态 ──

  /** 章节分组列表 */
  const segmentGroups = ref<ResourceSegmentGroup[]>([])

  /** 章节分组加载状态 */
  const loadingSegmentGroups = ref(false)

  /** 章节分组错误信息 */
  const segmentGroupsError = ref<string | null>(null)

  /** EPUB 导航层：null = 章节列表, string = 当前查看的 chapter group_key */
  const epubActiveGroupKey = ref<string | null>(null)

  /** 当前章节的标题（用于面包屑） */
  const epubActiveGroupTitle = ref<string>('')

  /** 章节级选中的 group_key 集合（用于批量翻译） */
  const epubSelectedGroupKeys = ref<Set<string>>(new Set())

  // ── 段落进度缓存 ──

  /** 资源级段落状态缓存：resourceId → 状态分布 */
  const segmentProgressCache = ref<Map<number, SegmentProgress>>(new Map())

  // ── 搜索定位（独立面板）状态 ──
  const searchResults = ref<Segment[]>([])
  const searchResultsCursor = ref<string | null>(null)
  const searchResultsTotal = ref<number | null>(null)
  const loadingSearchResults = ref(false)
  const searchResultsError = ref<string | null>(null)
  /** 当前定位目标的段落 id */
  const searchActiveResultId = ref<number | null>(null)
  /** 跳转完成序号：每次 jumpToSegment 开窗成功后自增（重复跳同一条也触发），驱动主列表滚动到锚点 */
  const searchJumpSeq = ref(0)
  /** 进行中的跳转计数：>0 时筛选/章节切换 watcher 让路，由 jumpToSegment 的锚点开窗接管加载与定位 */
  const jumpingToSegmentCount = ref(0)
  /** 开窗请求序号守卫：过期的锚点开窗 / 向上翻页响应直接丢弃（连续跳转、跳转与上翻并发时防旧覆新） */
  let segmentsWindowRequestId = 0
  /** 请求序号守卫：过期响应直接丢弃（防抖后连续请求的竞态防护） */
  let searchResultsRequestId = 0

  /** 最近一次搜索替换的 operation_id（按资源隔离，用于撤销/重做） */
  const lastSearchReplaceOperationId = ref<string | null>(null)

  const updateSegmentProgressCache = (resourceId: number, segments: Segment[]): void => {
    const counts: SegmentProgress = {
      pending: 0,
      translated: 0,
      edited: 0,
      approved: 0,
      rejected: 0,
      total: segments.length,
    }
    for (const seg of segments) {
      if (seg.status === 'pending') counts.pending++
      else if (seg.status === 'translated') counts.translated++
      else if (seg.status === 'edited') counts.edited++
      else if (seg.status === 'approved') counts.approved++
      else if (seg.status === 'rejected') counts.rejected++
    }
    segmentProgressCache.value = new Map(segmentProgressCache.value).set(resourceId, counts)
  }

  // ── EPUB 计算属性 ──

  /** 当前资源是否为 EPUB（基于 groups 数据判断） */
  const isEpubResource = computed(() => {
    if (segmentGroups.value.length > 1) return true
    if (segmentGroups.value.length === 1) {
      return segmentGroups.value[0]?.group_key !== ''
    }
    return false
  })

  /** 章节总数 */
  const epubChapterCount = computed(() => segmentGroups.value.length)

  /** 是否在章节内容视图中（vs 章节列表） */
  const isInChapterView = computed(() => epubActiveGroupKey.value !== null)

  // ── Actions：段落 ──

  const loadSegments = async (
    projectId: number,
    resourceId: number,
    append = false,
    groupKey?: string,
  ): Promise<void> => {
    loadingSegments.value = true
    segmentsError.value = null

    try {
      const hasQualityFilter =
        segmentQualityIssuesFilter.value !== 'all' ||
        segmentQualitySeverityFilter.value !== 'all' ||
        segmentQualityCodeFilter.value !== 'all'

      // 主列表不携带搜索条件：搜索职责完全属于搜索定位面板（loadSearchResults），
      // 保证「搜索不改变主列表」——包括锚点跳转与章节切换路径。
      const response = await fetchResourceSegments(projectId, resourceId, {
        status: segmentStatusFilter.value === 'all' ? undefined : segmentStatusFilter.value,
        include_total: !append,
        quality_issues:
          segmentQualityIssuesFilter.value === 'all' ? undefined : segmentQualityIssuesFilter.value,
        quality_severity:
          segmentQualitySeverityFilter.value === 'all'
            ? undefined
            : segmentQualitySeverityFilter.value,
        quality_code:
          segmentQualityCodeFilter.value === 'all' ? undefined : segmentQualityCodeFilter.value,
        cursor: append ? (segmentsCursor.value ?? undefined) : undefined,
        limit: 50,
        ...(groupKey ? { group_key: groupKey } : {}),
      })
      segments.value = append ? [...segments.value, ...response.items] : response.items
      segmentsCursor.value = response.next_cursor ?? null
      if (!append) {
        segmentsTotal.value = response.total ?? null
        // 向上翻页游标仅在锚点/向上加载窗口中有意义，普通重载时复位
        segmentsPrevCursor.value = null
      }

      // 仅在无筛选条件的全量加载时更新进度缓存
      if (!append && segmentStatusFilter.value === 'all' && !hasQualityFilter) {
        updateSegmentProgressCache(resourceId, segments.value)
      }
    } catch (error) {
      segmentsError.value = extractErrorMessage(error, t('api.errors.fetchSegmentsFailed'))
    } finally {
      loadingSegments.value = false
    }
  }

  /**
   * 以锚点段落为窗口起点加载一页（Track C：anchor_segment_id 就绪后的精确定位）。
   * beforeContext > 0 且窗口顶还有更早内容时，用 cursor=prev_cursor&direction=desc
   * 组合拉取紧邻上文行并前置，避免定位行顶在窗口最上缘、缺少前文参照。
   * 返回 false 表示已被更新的开窗请求取代（响应已丢弃），调用方不应再据此推进状态。
   */
  const loadSegmentsAround = async (
    projectId: number,
    resourceId: number,
    options: { groupKey?: string; anchorSegmentId: number; beforeContext?: number },
  ): Promise<boolean> => {
    const { groupKey, anchorSegmentId, beforeContext = 2 } = options
    const requestId = ++segmentsWindowRequestId
    loadingSegments.value = true
    // 新开窗使在途的向上翻页整体失效，解除其 loading 标记
    loadingSegmentsUp.value = false
    segmentsError.value = null

    try {
      const hasQualityFilter =
        segmentQualityIssuesFilter.value !== 'all' ||
        segmentQualitySeverityFilter.value !== 'all' ||
        segmentQualityCodeFilter.value !== 'all'
      const filterParams = {
        status: segmentStatusFilter.value === 'all' ? undefined : segmentStatusFilter.value,
        quality_issues:
          segmentQualityIssuesFilter.value === 'all' ? undefined : segmentQualityIssuesFilter.value,
        quality_severity:
          segmentQualitySeverityFilter.value === 'all'
            ? undefined
            : segmentQualitySeverityFilter.value,
        quality_code:
          segmentQualityCodeFilter.value === 'all' ? undefined : segmentQualityCodeFilter.value,
      }

      const anchored = await fetchResourceSegments(projectId, resourceId, {
        ...filterParams,
        anchor_segment_id: anchorSegmentId,
        include_total: true,
        limit: 50,
        ...(groupKey ? { group_key: groupKey } : {}),
      })
      if (requestId !== segmentsWindowRequestId) return false
      segments.value = anchored.items
      segmentsCursor.value = anchored.next_cursor ?? null
      segmentsTotal.value = anchored.total ?? null
      segmentsPrevCursor.value = anchored.prev_cursor ?? null

      if (beforeContext > 0 && segmentsPrevCursor.value) {
        const context = await fetchResourceSegments(projectId, resourceId, {
          ...filterParams,
          cursor: segmentsPrevCursor.value,
          direction: 'desc',
          limit: beforeContext,
          ...(groupKey ? { group_key: groupKey } : {}),
        })
        if (requestId !== segmentsWindowRequestId) return false
        // desc 响应升序返回紧邻窗口顶的 beforeContext 行；新窗口顶游标取其 prev_cursor
        segments.value = [...context.items, ...segments.value]
        segmentsPrevCursor.value = context.prev_cursor ?? null
      }

      if (segmentStatusFilter.value === 'all' && !hasQualityFilter) {
        updateSegmentProgressCache(resourceId, segments.value)
      }
      return true
    } catch (error) {
      if (requestId !== segmentsWindowRequestId) return false
      segmentsError.value = extractErrorMessage(error, t('api.errors.fetchSegmentsFailed'))
      return false
    } finally {
      if (requestId === segmentsWindowRequestId) {
        loadingSegments.value = false
      }
    }
  }

  /**
   * 向上加载一页（direction=desc + prev_cursor），前置到当前窗口顶。
   * 滚动位置补偿由调用方（SegmentPanel）在 await 后执行。
   * 若加载期间发生了新的锚点开窗（跳转），响应整体丢弃——旧游标对新窗口无意义。
   */
  const loadMoreSegmentsUp = async (
    projectId: number,
    resourceId: number,
    groupKey?: string,
  ): Promise<void> => {
    const cursor = segmentsPrevCursor.value
    if (!cursor || loadingSegmentsUp.value) return
    const requestId = segmentsWindowRequestId

    loadingSegmentsUp.value = true
    segmentsError.value = null

    try {
      const response = await fetchResourceSegments(projectId, resourceId, {
        status: segmentStatusFilter.value === 'all' ? undefined : segmentStatusFilter.value,
        quality_issues:
          segmentQualityIssuesFilter.value === 'all' ? undefined : segmentQualityIssuesFilter.value,
        quality_severity:
          segmentQualitySeverityFilter.value === 'all'
            ? undefined
            : segmentQualitySeverityFilter.value,
        quality_code:
          segmentQualityCodeFilter.value === 'all' ? undefined : segmentQualityCodeFilter.value,
        cursor,
        direction: 'desc',
        limit: 50,
        ...(groupKey ? { group_key: groupKey } : {}),
      })
      if (requestId !== segmentsWindowRequestId) return
      const existing = new Set(segments.value.map((s) => s.id))
      const fresh = response.items.filter((item) => !existing.has(item.id))
      if (fresh.length) {
        segments.value = [...fresh, ...segments.value]
      }
      segmentsPrevCursor.value = response.prev_cursor ?? null
    } catch (error) {
      if (requestId !== segmentsWindowRequestId) return
      segmentsError.value = extractErrorMessage(error, t('api.errors.fetchSegmentsFailed'))
    } finally {
      if (requestId === segmentsWindowRequestId) {
        loadingSegmentsUp.value = false
      }
    }
  }

  /**
   * 跨全资源搜索段落（不传 group_key），供独立搜索定位面板使用。
   * 搜索词/字段/大小写复用 segmentSearch / segmentSearchFieldFilter / segmentSearchCaseSensitive。
   * append 为结果分页追加。
   */
  const loadSearchResults = async (
    projectId: number,
    resourceId: number,
    append = false,
  ): Promise<void> => {
    const requestId = ++searchResultsRequestId
    loadingSearchResults.value = true
    searchResultsError.value = null
    if (!append) {
      searchActiveResultId.value = null
    }

    try {
      const searchTerm = segmentSearch.value.trim()
      const hasSearch = Boolean(searchTerm)

      const response = await fetchResourceSegments(projectId, resourceId, {
        search: searchTerm || undefined,
        search_field: hasSearch ? segmentSearchFieldFilter.value : undefined,
        case_sensitive: hasSearch ? segmentSearchCaseSensitive.value : undefined,
        include_total: !append,
        cursor: append ? (searchResultsCursor.value ?? undefined) : undefined,
        limit: 50,
      })
      if (requestId !== searchResultsRequestId) return
      searchResults.value = append ? [...searchResults.value, ...response.items] : response.items
      searchResultsCursor.value = response.next_cursor ?? null
      if (!append) {
        searchResultsTotal.value = response.total ?? null
      }
    } catch (error) {
      if (requestId !== searchResultsRequestId) return
      searchResultsError.value = extractErrorMessage(error, t('api.errors.fetchSegmentsFailed'))
    } finally {
      if (requestId === searchResultsRequestId) {
        loadingSearchResults.value = false
      }
    }
  }

  /**
   * 定位到某个段落（Track C 精确版）：
   * 按命中段落的 group_key 切换章节（非 EPUB 无分组则退回全资源视图），
   * 以 anchor_segment_id 精确开窗并带上紧邻上文行。
   * 跳转期间置 jumpingToSegmentCount：筛选/章节切换 watcher 据此让路，
   * 否则 enterChapter 触发的章首重载会与锚点开窗竞态、覆盖定位窗口。
   */
  const jumpToSegment = async (
    projectId: number,
    resourceId: number,
    segment: Segment,
  ): Promise<void> => {
    const groupKey = segment.group_key ?? undefined
    jumpingToSegmentCount.value++
    try {
      if (groupKey) {
        const groupTitle =
          segmentGroups.value.find((g) => g.group_key === groupKey)?.group_title ?? groupKey
        enterChapter(groupKey, groupTitle)
      } else {
        exitChapter()
      }
      const completed = await loadSegmentsAround(projectId, resourceId, {
        groupKey,
        anchorSegmentId: segment.id,
        beforeContext: 2,
      })
      if (!completed) return
      searchActiveResultId.value = segment.id
      searchJumpSeq.value++
    } finally {
      jumpingToSegmentCount.value--
    }
  }

  const updateSegment = async (
    projectId: number,
    resourceId: number,
    segmentId: number,
    payload: SegmentUpdatePayload,
  ): Promise<Segment> => {
    editingSegmentIds.value = [...editingSegmentIds.value, segmentId]
    actionError.value = null

    try {
      const segment = await updateResourceSegmentRequest(projectId, resourceId, segmentId, payload)
      segments.value = segments.value.map((item) => (item.id === segment.id ? segment : item))

      // 刷新章节分组进度
      await refreshChapterGroups(projectId, resourceId)

      return segment
    } catch (error) {
      actionError.value = extractErrorMessage(error, t('api.errors.updateSegmentFailed'))
      throw error
    } finally {
      editingSegmentIds.value = editingSegmentIds.value.filter((id) => id !== segmentId)
    }
  }

  /**
   * 对单条质量问题下裁决（dismissed）或撤销裁决（pending）。
   * 后端返回更新后的整个 segment，直接替换本地副本。
   */
  const setIssueDisposition = async (
    projectId: number,
    resourceId: number,
    segmentId: number,
    payload: ApiSchemas['IssueDispositionRequest'],
  ): Promise<Segment> => {
    editingSegmentIds.value = [...editingSegmentIds.value, segmentId]
    actionError.value = null

    try {
      const segment = await setIssueDispositionRequest(projectId, resourceId, segmentId, payload)
      segments.value = segments.value.map((item) => (item.id === segment.id ? segment : item))
      return segment
    } catch (error) {
      actionError.value = extractErrorMessage(error, t('api.errors.setIssueDispositionFailed'))
      throw error
    } finally {
      editingSegmentIds.value = editingSegmentIds.value.filter((id) => id !== segmentId)
    }
  }

  /**
   * 加载章节分组列表
   */
  const loadSegmentGroups = async (projectId: number, resourceId: number): Promise<void> => {
    loadingSegmentGroups.value = true
    segmentGroupsError.value = null

    try {
      const response = await fetchSegmentGroups(projectId, resourceId)
      segmentGroups.value = response.items
    } catch (error) {
      segmentGroupsError.value = extractErrorMessage(
        error,
        t('api.errors.fetchSegmentGroupsFailed'),
      )
    } finally {
      loadingSegmentGroups.value = false
    }
  }

  /** 进入某个章节 */
  const enterChapter = (groupKey: string, groupTitle: string): void => {
    epubActiveGroupKey.value = groupKey
    epubActiveGroupTitle.value = groupTitle
  }

  /** 返回章节列表 */
  const exitChapter = (): void => {
    epubActiveGroupKey.value = null
    epubActiveGroupTitle.value = ''
  }

  /** 切换章节选中状态 */
  const toggleEpubGroupSelection = (groupKey: string): void => {
    const newSet = new Set(epubSelectedGroupKeys.value)
    if (newSet.has(groupKey)) {
      newSet.delete(groupKey)
    } else {
      newSet.add(groupKey)
    }
    epubSelectedGroupKeys.value = newSet
  }

  /**
   * 刷新章节分组进度
   */
  const refreshChapterGroups = async (projectId: number, resourceId: number): Promise<void> => {
    try {
      const response = await fetchSegmentGroups(projectId, resourceId)
      segmentGroups.value = response.items
    } catch {
      // 静默失败，不影响用户操作
    }
  }

  // ── 工具方法 ──

  /** 清空搜索定位状态（供 reset 调用，同时作废在途请求） */
  const resetSearchResults = (): void => {
    searchResultsRequestId++
    searchResults.value = []
    searchResultsCursor.value = null
    searchResultsTotal.value = null
    loadingSearchResults.value = false
    searchResultsError.value = null
    searchActiveResultId.value = null
  }

  /** 清空段落列表和游标（供跨域协调调用） */
  const resetSegments = (): void => {
    segments.value = []
    segmentsCursor.value = null
    segmentsPrevCursor.value = null
    segmentsTotal.value = null
    lastSearchReplaceOperationId.value = null
    resetSearchResults()
  }

  /**
   * 重置 EPUB 章节状态
   */
  const resetEpubState = (): void => {
    segmentGroups.value = []
    loadingSegmentGroups.value = false
    segmentGroupsError.value = null
    epubActiveGroupKey.value = null
    epubActiveGroupTitle.value = ''
    epubSelectedGroupKeys.value = new Set()
  }

  const reset = (): void => {
    segments.value = []
    segmentsCursor.value = null
    segmentsPrevCursor.value = null
    segmentsTotal.value = null
    segmentsError.value = null
    segmentSearch.value = ''
    segmentStatusFilter.value = 'all'
    segmentQualityIssuesFilter.value = 'all'
    segmentQualitySeverityFilter.value = 'all'
    segmentQualityCodeFilter.value = 'all'
    segmentSearchFieldFilter.value = 'both'
    segmentSearchCaseSensitive.value = true
    segmentProgressCache.value = new Map()
    lastSearchReplaceOperationId.value = null
    actionError.value = null
    resetSearchResults()
    resetEpubState()
  }

  return {
    segments,
    segmentsCursor,
    segmentsTotal,
    loadingSegments,
    segmentsError,
    editingSegmentIds,
    actionError,
    segmentSearch,
    segmentStatusFilter,
    segmentQualityIssuesFilter,
    segmentQualitySeverityFilter,
    segmentQualityCodeFilter,
    segmentSearchFieldFilter,
    segmentSearchCaseSensitive,
    lastSearchReplaceOperationId,
    segmentProgressCache,
    updateSegmentProgressCache,
    loadSegments,
    updateSegment,
    setIssueDisposition,
    resetSegments,
    reset,
    // ── 窗口化加载（锚点 / 向上翻页）──
    segmentsPrevCursor,
    loadingSegmentsUp,
    loadSegmentsAround,
    loadMoreSegmentsUp,
    // ── 搜索定位（独立面板）导出 ──
    searchResults,
    searchResultsCursor,
    searchResultsTotal,
    loadingSearchResults,
    searchResultsError,
    searchActiveResultId,
    searchJumpSeq,
    jumpingToSegmentCount,
    loadSearchResults,
    jumpToSegment,
    resetSearchResults,
    // ── EPUB 新增导出 ──
    segmentGroups,
    loadingSegmentGroups,
    segmentGroupsError,
    epubActiveGroupKey,
    epubActiveGroupTitle,
    epubSelectedGroupKeys,
    isEpubResource,
    epubChapterCount,
    isInChapterView,
    loadSegmentGroups,
    enterChapter,
    exitChapter,
    toggleEpubGroupSelection,
    refreshChapterGroups,
    resetEpubState,
  }
})
