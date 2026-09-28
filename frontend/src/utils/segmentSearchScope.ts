import type { ApiSchemas } from '@/api/client'
import type { FetchResourceSegmentsParams } from '@/api/projects'

type Segment = ApiSchemas['Segment']

export type SegmentSearchScope = {
  filters?: Pick<
    FetchResourceSegmentsParams,
    'group_key' | 'status' | 'quality_issues' | 'quality_severity' | 'quality_code'
  >
  segmentIds?: number[]
}

export const MAX_SEGMENT_SEARCH_MATCHES = 10_000
const SEARCH_PAGE_SIZE = 100
const MAX_SEARCH_PAGES = 10_000

export type SegmentSearchCollectionErrorCode = 'limit_exceeded' | 'pagination_stalled' | 'cancelled'

/** Callers translate the code; incomplete collections must never become an apply scope. */
export class SegmentSearchCollectionError extends Error {
  readonly code: SegmentSearchCollectionErrorCode

  constructor(code: SegmentSearchCollectionErrorCode) {
    super(code)
    this.name = 'SegmentSearchCollectionError'
    this.code = code
  }
}

type FetchSearchPage = (
  projectId: number,
  resourceId: number,
  params: FetchResourceSegmentsParams,
) => Promise<ApiSchemas['ResourceSegmentListResponse']>

export type CollectSegmentSearchMatchesOptions = {
  segmentIds?: number[]
  isCurrent?: () => boolean
  onProgress?: (count: number) => void
  fetchPage?: FetchSearchPage
}

/**
 * Collect a complete, deduplicated snapshot of server matches before intersecting selection.
 * Pagination inputs are intentionally replaced so a visible page cannot restrict the snapshot.
 * The collection cap applies to all matching IDs scanned, including matches outside selection.
 */
export const collectSegmentSearchMatches = async (
  projectId: number,
  resourceId: number,
  params: FetchResourceSegmentsParams,
  options: CollectSegmentSearchMatchesOptions = {},
): Promise<Segment[]> => {
  const ensureCurrent = (): void => {
    if (options.isCurrent?.() === false) {
      throw new SegmentSearchCollectionError('cancelled')
    }
  }
  ensureCurrent()

  const selectedIds = options.segmentIds ? new Set(options.segmentIds) : undefined
  if (selectedIds?.size === 0) return []

  const matchingParams = { ...params }
  const fetchPage = options.fetchPage ?? (await import('@/api/projects')).fetchResourceSegments
  const matches = new Map<number, Segment>()
  const cursors = new Set<string>()
  let cursor: string | undefined
  let pageCount = 0
  let selectedMatchCount = 0

  while (true) {
    ensureCurrent()
    if (pageCount >= MAX_SEARCH_PAGES) {
      throw new SegmentSearchCollectionError('pagination_stalled')
    }
    const response = await fetchPage(projectId, resourceId, {
      ...matchingParams,
      cursor,
      anchor_segment_id: undefined,
      direction: 'asc',
      limit: SEARCH_PAGE_SIZE,
      include_total: false,
    })
    ensureCurrent()
    pageCount++

    for (const segment of response.items) {
      if (!matches.has(segment.id) && (!selectedIds || selectedIds.has(segment.id))) {
        selectedMatchCount++
      }
      matches.set(segment.id, segment)
      if (matches.size > MAX_SEGMENT_SEARCH_MATCHES) {
        throw new SegmentSearchCollectionError('limit_exceeded')
      }
    }
    options.onProgress?.(selectedMatchCount)
    ensureCurrent()

    const nextCursor = response.next_cursor
    if (!nextCursor) break
    if (cursors.has(nextCursor)) {
      throw new SegmentSearchCollectionError('pagination_stalled')
    }
    cursors.add(nextCursor)
    cursor = nextCursor
  }

  const items = [...matches.values()]
  return selectedIds ? items.filter((segment) => selectedIds.has(segment.id)) : items
}
