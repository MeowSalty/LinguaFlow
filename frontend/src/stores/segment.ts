import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import {
  type ApiSchemas,
  fetchResourceSegments,
  updateResourceSegment as updateResourceSegmentRequest,
} from '@/api/client'
import { fetchSegmentGroups, type ResourceSegmentGroup } from '@/api/epub'
import { t } from '@/i18n'

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

export interface SegmentProgress {
  pending: number
  translated: number
  edited: number
  approved: number
  rejected: number
  total: number
}

const getErrorMessage = (error: unknown, fallback: string): string =>
  error instanceof Error ? error.message : fallback

export const useSegmentStore = defineStore('segment', () => {
  // ── 段落状态 ──
  const segments = ref<Segment[]>([])
  const segmentsCursor = ref<string | null>(null)
  const loadingSegments = ref(false)
  const segmentsError = ref<string | null>(null)
  const editingSegmentIds = ref<number[]>([])
  const actionError = ref<string | null>(null)

  // ── 筛选器 ──
  const segmentSearch = ref('')
  const segmentStatusFilter = ref<SegmentStatusFilter>('all')

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
      const response = await fetchResourceSegments(projectId, resourceId, {
        status: segmentStatusFilter.value === 'all' ? undefined : segmentStatusFilter.value,
        search: segmentSearch.value.trim() || undefined,
        cursor: append ? (segmentsCursor.value ?? undefined) : undefined,
        limit: 50,
        ...(groupKey ? { group_key: groupKey } : {}),
      })
      segments.value = append ? [...segments.value, ...response.items] : response.items
      segmentsCursor.value = response.next_cursor ?? null

      // 仅在无筛选条件的全量加载时更新进度缓存
      if (!append && segmentStatusFilter.value === 'all' && !segmentSearch.value.trim()) {
        updateSegmentProgressCache(resourceId, segments.value)
      }
    } catch (error) {
      segmentsError.value = getErrorMessage(error, t('api.errors.fetchSegmentsFailed'))
    } finally {
      loadingSegments.value = false
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
      actionError.value = getErrorMessage(error, t('api.errors.updateSegmentFailed'))
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
      segmentGroupsError.value = getErrorMessage(error, t('api.errors.fetchSegmentGroupsFailed'))
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
    const currentKeys = [...epubSelectedGroupKeys.value]
    const newSet = new Set(epubSelectedGroupKeys.value)
    if (newSet.has(groupKey)) {
      newSet.delete(groupKey)
    } else {
      newSet.add(groupKey)
    }
    epubSelectedGroupKeys.value = newSet
    console.debug('[segmentStore] toggleEpubGroupSelection:', {
      toggledKey: groupKey,
      before: currentKeys,
      after: [...newSet],
      storeId: 'segment',
    })
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

  /** 清空段落列表和游标（供跨域协调调用） */
  const resetSegments = (): void => {
    segments.value = []
    segmentsCursor.value = null
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
    const before = [...epubSelectedGroupKeys.value]
    epubSelectedGroupKeys.value = new Set()
    console.debug('[segmentStore] resetEpubState:', {
      clearedKeys: before,
      after: [...epubSelectedGroupKeys.value],
    })
  }

  const reset = (): void => {
    segments.value = []
    segmentsCursor.value = null
    segmentsError.value = null
    segmentSearch.value = ''
    segmentStatusFilter.value = 'all'
    segmentProgressCache.value = new Map()
    actionError.value = null
    resetEpubState()
  }

  return {
    segments,
    segmentsCursor,
    loadingSegments,
    segmentsError,
    editingSegmentIds,
    actionError,
    segmentSearch,
    segmentStatusFilter,
    segmentProgressCache,
    updateSegmentProgressCache,
    loadSegments,
    updateSegment,
    resetSegments,
    reset,
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
