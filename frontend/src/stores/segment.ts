import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import {
  type ApiSchemas,
  fetchResourceSegments,
  setResourceSegmentIssueDisposition as setIssueDispositionRequest,
  updateResourceSegment as updateResourceSegmentRequest,
} from '@/api/client'
import { fetchSegmentGroups, type ResourceSegmentGroup } from '@/api/epub'
import type { ResourceSegmentQualityCode, SegmentMatchMode } from '@/api/projects'
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
  /** 搜索定位匹配模式：substring 字面子串（默认）/ regex 正则（后端 RE2 语义） */
  const segmentSearchMatchMode = ref<SegmentMatchMode>('substring')
  /** 搜索定位全字匹配：命中前后不得紧邻字母或数字，对 substring 与 regex 均生效 */
  const segmentSearchWholeWord = ref(false)

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

  /**
   * 章节侧栏多选（批量处理）模式：
   * 开启后章节行仅切换选中态，不再触发章节导航与正文加载
   */
  const chapterMultiSelect = ref(false)

  /** 章节分组请求序号：切换资源时丢弃旧资源的迟到响应 */
  let segmentGroupsRequestId = 0

  /** 段落主列表请求序号：切换资源/章节/筛选时丢弃迟到响应 */
  let segmentsRequestId = 0

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
  /**
   * 当前定位命中字段：主列表据此滚动到该字段正文内的命中 mark；
   * null = 展示级未定位命中（generic）或未跳转，滚动回退整行
   */
  const searchActiveResultField = ref<'source' | 'target' | null>(null)
  /** 跳转完成序号：每次 jumpToSegment 开窗成功后自增（重复跳同一条也触发），驱动主列表滚动到锚点 */
  const searchJumpSeq = ref(0)
  /** 进行中的跳转计数：>0 时筛选/章节切换 watcher 让路，由 jumpToSegment 的锚点开窗接管加载与定位 */
  const jumpingToSegmentCount = ref(0)
  /**
   * 开窗请求序号守卫：过期的锚点开窗 / 向上翻页响应直接丢弃
   * （连续跳转、跳转与上翻并发时防旧覆新；resetSearchResults 亦自增以作废旧跳转的在途开窗）
   */
  let segmentsWindowRequestId = 0
  /** 请求序号守卫：过期响应直接丢弃（防抖后连续请求的竞态防护） */
  let searchResultsRequestId = 0
  /**
   * 跳转请求序号守卫：每次 jumpToSegment 自增，resetSearchResults 亦自增；
   * 在途跳转的锚点开窗完成后序号不再匹配即放弃写回，避免旧跳转复活已清空的定位态
   */
  let searchJumpRequestId = 0

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
    if (append && loadingSegments.value) return
    const requestId = ++segmentsRequestId
    segmentsWindowRequestId++
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
      if (requestId !== segmentsRequestId) return
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
      if (requestId !== segmentsRequestId) return
      segmentsError.value = extractErrorMessage(error, t('api.errors.fetchSegmentsFailed'))
    } finally {
      if (requestId === segmentsRequestId) {
        loadingSegments.value = false
      }
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
    segmentsRequestId++
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
   * 搜索词/字段/大小写/匹配模式/全字复用 segmentSearch / segmentSearchFieldFilter /
   * segmentSearchCaseSensitive / segmentSearchMatchMode / segmentSearchWholeWord。
   * append 为结果分页追加；新一轮搜索（append=false）立即清空旧结果与旧错误，
   * 失败时只留错误提示，不与过期结果并存。
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
      searchActiveResultField.value = null
      searchResults.value = []
      searchResultsCursor.value = null
      searchResultsTotal.value = null
    }

    try {
      const searchTerm = segmentSearch.value.trim()
      const hasSearch = Boolean(searchTerm)

      const response = await fetchResourceSegments(projectId, resourceId, {
        search: searchTerm || undefined,
        search_field: hasSearch ? segmentSearchFieldFilter.value : undefined,
        case_sensitive: hasSearch ? segmentSearchCaseSensitive.value : undefined,
        match_mode: hasSearch ? segmentSearchMatchMode.value : undefined,
        whole_word: hasSearch ? segmentSearchWholeWord.value : undefined,
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
   * field 为展示级命中字段（source / target），开窗成功与否决定它是否写入：
   * 被更新的开窗请求取代（completed=false）时直接返回，不留下与新窗口不符的字段；
   * generic 命中传 null，滚动回退整行。
   * 跳转期间置 jumpingToSegmentCount：筛选/章节切换 watcher 据此让路，
   * 否则 enterChapter 触发的章首重载会与锚点开窗竞态、覆盖定位窗口。
   */
  const jumpToSegment = async (
    projectId: number,
    resourceId: number,
    segment: Segment,
    field: 'source' | 'target' | null = null,
  ): Promise<void> => {
    const groupKey = segment.group_key ?? undefined
    const requestId = ++searchJumpRequestId
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
      // 更新的跳转或 resetSearchResults 已作废本次跳转：不得回写定位态与完成序号
      if (!completed || requestId !== searchJumpRequestId) return
      searchActiveResultId.value = segment.id
      searchActiveResultField.value = field
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
    const requestId = ++segmentGroupsRequestId
    loadingSegmentGroups.value = true
    segmentGroupsError.value = null

    try {
      const response = await fetchSegmentGroups(projectId, resourceId)
      if (requestId !== segmentGroupsRequestId) return
      segmentGroups.value = response.items
    } catch (error) {
      if (requestId !== segmentGroupsRequestId) return
      segmentGroupsError.value = extractErrorMessage(
        error,
        t('api.errors.fetchSegmentGroupsFailed'),
      )
    } finally {
      if (requestId === segmentGroupsRequestId) {
        loadingSegmentGroups.value = false
      }
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

  /** 进入章节多选（批量处理）模式 */
  const enterChapterMultiSelect = (): void => {
    chapterMultiSelect.value = true
  }

  /** 退出章节多选模式并清空选择 */
  const exitChapterMultiSelect = (): void => {
    chapterMultiSelect.value = false
    epubSelectedGroupKeys.value = new Set()
  }

  /** 全选章节分组（取当前 segmentGroups 的全部 group_key） */
  const selectAllEpubGroups = (): void => {
    epubSelectedGroupKeys.value = new Set(segmentGroups.value.map((g) => g.group_key))
  }

  /** 清空章节分组选择 */
  const clearEpubGroupSelection = (): void => {
    epubSelectedGroupKeys.value = new Set()
  }

  /**
   * 刷新章节分组进度
   */
  const refreshChapterGroups = async (projectId: number, resourceId: number): Promise<void> => {
    const requestId = ++segmentGroupsRequestId
    try {
      const response = await fetchSegmentGroups(projectId, resourceId)
      if (requestId !== segmentGroupsRequestId) return
      segmentGroups.value = response.items
    } catch {
      // 静默失败，不影响用户操作
    }
  }

  // ── 工具方法 ──

  /**
   * 清空搜索定位状态（供 reset 调用），并作废在途的搜索结果与跳转请求。
   * 同时自增 segmentsWindowRequestId 作废在途的锚点开窗/向上翻页：否则旧跳转的开窗响应
   * 会在跳转守卫（searchJumpRequestId）生效前先写回主列表，覆盖用户改搜索后的 segments/游标。
   * 被作废请求的 finally 守卫随之失配、不再复位 loading，故在此显式清理两个窗口 loading。
   */
  const resetSearchResults = (): void => {
    searchResultsRequestId++
    searchJumpRequestId++
    segmentsWindowRequestId++
    loadingSegments.value = false
    loadingSegmentsUp.value = false
    searchResults.value = []
    searchResultsCursor.value = null
    searchResultsTotal.value = null
    loadingSearchResults.value = false
    searchResultsError.value = null
    searchActiveResultId.value = null
    searchActiveResultField.value = null
  }

  /** 清空段落列表和游标（供跨域协调调用） */
  const resetSegments = (): void => {
    segmentsRequestId++
    segmentsWindowRequestId++
    loadingSegments.value = false
    loadingSegmentsUp.value = false
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
    segmentGroupsRequestId++
    segmentGroups.value = []
    loadingSegmentGroups.value = false
    segmentGroupsError.value = null
    epubActiveGroupKey.value = null
    epubActiveGroupTitle.value = ''
    epubSelectedGroupKeys.value = new Set()
    chapterMultiSelect.value = false
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
    segmentSearchMatchMode.value = 'substring'
    segmentSearchWholeWord.value = false
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
    segmentSearchMatchMode,
    segmentSearchWholeWord,
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
    searchActiveResultField,
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
    chapterMultiSelect,
    isEpubResource,
    epubChapterCount,
    isInChapterView,
    loadSegmentGroups,
    enterChapter,
    exitChapter,
    toggleEpubGroupSelection,
    enterChapterMultiSelect,
    exitChapterMultiSelect,
    selectAllEpubGroups,
    clearEpubGroupSelection,
    refreshChapterGroups,
    resetEpubState,
  }
})
