import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import {
  type ApiSchemas,
  fetchResourceSegments,
  updateResourceSegment as updateResourceSegmentRequest,
} from '@/api/client'
import { t } from '@/i18n'

type Segment = ApiSchemas['Segment']
type SegmentUpdatePayload = ApiSchemas['ResourceSegmentUpdateRequest']

export type SegmentStatusFilter = 'pending' | 'translated' | 'reviewed' | 'rejected' | 'all'

export interface SegmentProgress {
  pending: number
  translated: number
  reviewed: number
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

  // ── 段落进度缓存 ──

  /** 资源级段落状态缓存：resourceId → 状态分布 */
  const segmentProgressCache = ref<Map<number, SegmentProgress>>(new Map())

  const updateSegmentProgressCache = (resourceId: number, segments: Segment[]): void => {
    const counts: SegmentProgress = {
      pending: 0,
      translated: 0,
      reviewed: 0,
      rejected: 0,
      total: segments.length,
    }
    for (const seg of segments) {
      if (seg.status === 'pending') counts.pending++
      else if (seg.status === 'translated') counts.translated++
      else if (seg.status === 'reviewed') counts.reviewed++
      else if (seg.status === 'rejected') counts.rejected++
    }
    segmentProgressCache.value = new Map(segmentProgressCache.value).set(resourceId, counts)
  }

  /** 获取指定资源的翻译进度百分比（未加载段落的资源返回 0） */
  const getResourceProgress = (resourceId: number): number => {
    const progress = segmentProgressCache.value.get(resourceId)
    if (!progress || progress.total === 0) return 0
    return Math.round(((progress.translated + progress.reviewed) / progress.total) * 100)
  }

  /** 已加载段落中已翻译/已审核的总数（前端聚合） */
  const translatedSegmentCount = computed(() => {
    let sum = 0
    for (const progress of segmentProgressCache.value.values()) {
      sum += progress.translated + progress.reviewed
    }
    return sum
  })

  // ── Actions：段落 ──

  const loadSegments = async (
    projectId: number,
    resourceId: number,
    append = false,
  ): Promise<void> => {
    loadingSegments.value = true
    segmentsError.value = null

    try {
      const response = await fetchResourceSegments(projectId, resourceId, {
        status: segmentStatusFilter.value === 'all' ? undefined : segmentStatusFilter.value,
        search: segmentSearch.value.trim() || undefined,
        cursor: append ? (segmentsCursor.value ?? undefined) : undefined,
        limit: 50,
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

  /** 为指定资源 ID 列表预加载段落数据以填充进度缓存（后台静默执行） */
  const preloadDirectoryProgress = async (
    projectId: number,
    resourceIds: number[],
  ): Promise<void> => {
    const CONCURRENT = 3

    const loadResourceSegments = async (resourceId: number): Promise<void> => {
      let cursor: string | undefined
      const collected: Segment[] = []

      do {
        const response = await fetchResourceSegments(projectId, resourceId, {
          cursor,
          limit: 100,
        })
        collected.push(...response.items)
        cursor = response.next_cursor ?? undefined
      } while (cursor)

      updateSegmentProgressCache(resourceId, collected)
    }

    for (let i = 0; i < resourceIds.length; i += CONCURRENT) {
      const batch = resourceIds.slice(i, i + CONCURRENT)
      await Promise.allSettled(batch.map((id) => loadResourceSegments(id)))
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
      return segment
    } catch (error) {
      actionError.value = getErrorMessage(error, t('api.errors.updateSegmentFailed'))
      throw error
    } finally {
      editingSegmentIds.value = editingSegmentIds.value.filter((id) => id !== segmentId)
    }
  }

  // ── 工具方法 ──

  /** 清空段落列表和游标（供跨域协调调用） */
  const resetSegments = (): void => {
    segments.value = []
    segmentsCursor.value = null
  }

  const reset = (): void => {
    segments.value = []
    segmentsCursor.value = null
    segmentsError.value = null
    segmentSearch.value = ''
    segmentStatusFilter.value = 'all'
    segmentProgressCache.value = new Map()
    actionError.value = null
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
    translatedSegmentCount,
    updateSegmentProgressCache,
    getResourceProgress,
    loadSegments,
    preloadDirectoryProgress,
    updateSegment,
    resetSegments,
    reset,
  }
})
