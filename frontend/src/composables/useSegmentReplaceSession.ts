import { computed, onScopeDispose, ref, shallowRef, watch } from 'vue'

import type { ApiSchemas } from '@/api/client'
import {
  applyResourceSegmentsSearchReplace,
  isSearchReplaceApplyError,
  previewResourceSegmentsSearchReplace,
  undoResourceSegmentsSearchReplace,
  type SearchReplaceParams,
  type SegmentMatchMode,
} from '@/api/projects'
import { t } from '@/i18n'
import {
  collectSegmentSearchMatches,
  MAX_SEGMENT_SEARCH_MATCHES,
  SegmentSearchCollectionError,
  type SegmentSearchScope,
} from '@/utils/segmentSearchScope'
import { countUnicodeCodePoints, SEGMENT_SEARCH_MAX_LENGTH } from '@/utils/unicode'

type Segment = ApiSchemas['Segment']
type PreviewResponse = ApiSchemas['SearchReplacePreviewResponse']
type ApplyResponse = ApiSchemas['SearchReplaceApplyResponse']
type UndoResponse = ApiSchemas['SearchReplaceUndoResponse']

export interface ReplaceSessionContext {
  projectId: number
  resourceId: number
}

export interface ReplaceSessionInput extends ReplaceSessionContext {
  find: string
  replaceWith: string
  matchMode: SegmentMatchMode
  caseSensitive: boolean
  wholeWord: boolean
  searchField: 'source' | 'target' | 'both'
  scope: SegmentSearchScope
  scopeLabel: string
}

export interface ReplaceApplyConfirmation extends ReplaceSessionContext {
  token: number
  /** When present, apply only this preview item while keeping the full preview visible. */
  segmentId?: number
  find: string
  replaceWith: string
  matchedSegmentCount: number
  totalReplacements: number
  scopeLabel: string
}

export interface ReplaceUndoConfirmation extends ReplaceSessionContext {
  token: number
  operationId: string
}

interface ReplaceSessionOptions {
  snapshot: () => ReplaceSessionInput | null
  /** Resource identity remains available when the find input is empty or invalid. */
  context: () => ReplaceSessionContext | null
  getOperation: () => string | null
  onOperation: (operationId: string | null, context: ReplaceSessionContext) => void
  onApplied: (payload: ReplaceSessionContext & { items: Segment[] }) => void
}

interface FrozenPreview {
  token: number
  epoch: number
  version: number
  fingerprint: string
  input: ReplaceSessionInput
  request: SearchReplaceParams & { segment_ids: number[] }
  segments: Map<number, Segment>
}

const contextKey = (context: ReplaceSessionContext | null): string =>
  context ? `${context.projectId}:${context.resourceId}` : ''

/** Make an owned, deterministic snapshot; selection order has no scope semantics. */
const copyInput = (input: ReplaceSessionInput): ReplaceSessionInput => ({
  projectId: input.projectId,
  resourceId: input.resourceId,
  find: input.find,
  replaceWith: input.replaceWith,
  matchMode: input.matchMode,
  caseSensitive: input.caseSensitive,
  wholeWord: input.wholeWord,
  searchField: input.searchField,
  scope: {
    filters: {
      group_key: input.scope.filters?.group_key,
      status: input.scope.filters?.status,
      quality_issues: input.scope.filters?.quality_issues,
      quality_severity: input.scope.filters?.quality_severity,
      quality_code: input.scope.filters?.quality_code,
    },
    segmentIds: input.scope.segmentIds
      ? [...new Set(input.scope.segmentIds)].sort((left, right) => left - right)
      : undefined,
  },
  scopeLabel: input.scopeLabel,
})

/**
 * A preview fixes matching options and the complete set of target IDs. The API has no
 * content/version token: later server edits can still produce skipped or changed results.
 */
export function useSegmentReplaceSession(options: ReplaceSessionOptions) {
  const preview = shallowRef<PreviewResponse | null>(null)
  const previewStale = ref(false)
  const previewing = ref(false)
  const applying = ref(false)
  const undoing = ref(false)
  const errorMessage = ref<string | null>(null)
  const errorStatus = ref<number | null>(null)
  const progress = ref(0)
  const lastResult = shallowRef<ApplyResponse | UndoResponse | null>(null)
  const frozen = shallowRef<FrozenPreview | null>(null)
  const busy = computed(() => previewing.value || applying.value || undoing.value)
  const frozenScopeLabel = computed(() => frozen.value?.input.scopeLabel ?? '')
  const inputVersion = ref(0)
  let resourceEpoch = 0
  let requestSequence = 0
  let nextToken = 0
  let operationVersion = 0
  let disposed = false
  let pendingUndo:
    | (ReplaceUndoConfirmation & {
        context: ReplaceSessionContext
        epoch: number
        operationVersion: number
      })
    | null = null

  const fingerprint = (): string => {
    const input = options.snapshot()
    return input ? JSON.stringify(copyInput(input)) : ''
  }
  const currentContext = (context: ReplaceSessionContext, epoch: number): boolean =>
    !disposed && epoch === resourceEpoch && contextKey(context) === contextKey(options.context())
  const isFrozenCurrent = (session: FrozenPreview): boolean =>
    currentContext(session.input, session.epoch) &&
    session.version === inputVersion.value &&
    session.fingerprint === fingerprint()

  const clearError = (): void => {
    errorMessage.value = null
    errorStatus.value = null
  }
  const invalidate = (): void => {
    inputVersion.value++
    requestSequence++
    previewing.value = false
    previewStale.value = preview.value !== null
    progress.value = 0
    clearError()
  }
  const clearPreview = (): void => {
    invalidate()
    preview.value = null
    frozen.value = null
    previewStale.value = false
  }

  watch(fingerprint, invalidate, { flush: 'sync' })
  watch(
    () => contextKey(options.context()),
    () => {
      resourceEpoch++
      pendingUndo = null
      clearPreview()
      lastResult.value = null
      // In-flight writes keep their busy flag until their own finally block.
    },
    { flush: 'sync' },
  )
  watch(
    options.getOperation,
    () => {
      operationVersion++
      pendingUndo = null
    },
    { flush: 'sync' },
  )
  onScopeDispose(() => {
    disposed = true
    resourceEpoch++
    requestSequence++
    pendingUndo = null
  })

  const recordError = (error: unknown, fallback: string): void => {
    errorStatus.value = isSearchReplaceApplyError(error) ? error.status : null
    if (error instanceof SegmentSearchCollectionError) {
      if (error.code === 'cancelled') return
      errorMessage.value =
        error.code === 'limit_exceeded'
          ? t('api.errors.segmentSearchCollectionLimitExceeded', {
              max: MAX_SEGMENT_SEARCH_MATCHES,
            })
          : t('api.errors.segmentSearchCollectionPaginationStalled')
      return
    }
    errorMessage.value = error instanceof Error ? error.message : fallback
  }

  const canApply = computed(() =>
    Boolean(
      frozen.value &&
      isFrozenCurrent(frozen.value) &&
      !previewStale.value &&
      preview.value &&
      preview.value.matched_segment_count > 0 &&
      frozen.value.request.segment_ids.length > 0 &&
      !busy.value,
    ),
  )

  const previewChanges = async (singleSegmentId?: number): Promise<PreviewResponse | null> => {
    const current = options.snapshot()
    if (disposed || busy.value || !current || !current.find.trim()) return null
    if (current.searchField === 'source') return null
    if (contextKey(current) !== contextKey(options.context())) return null
    if (countUnicodeCodePoints(current.find) > SEGMENT_SEARCH_MAX_LENGTH) {
      errorMessage.value = t('api.errors.searchReplaceFindTooLong', {
        max: SEGMENT_SEARCH_MAX_LENGTH,
      })
      return null
    }
    clearPreview()
    lastResult.value = null
    const input = copyInput(current)
    const version = inputVersion.value
    const epoch = resourceEpoch
    const requestId = ++requestSequence
    const inputFingerprint = fingerprint()
    const isCurrent = (): boolean =>
      currentContext(input, epoch) &&
      requestId === requestSequence &&
      version === inputVersion.value &&
      inputFingerprint === fingerprint()
    previewing.value = true

    try {
      const scopeIds = input.scope.segmentIds
      const segmentIds =
        singleSegmentId === undefined
          ? scopeIds
          : !scopeIds || scopeIds.includes(singleSegmentId)
            ? [singleSegmentId]
            : []
      const segments = await collectSegmentSearchMatches(
        input.projectId,
        input.resourceId,
        {
          ...input.scope.filters,
          search: input.find,
          search_field: 'target',
          match_mode: input.matchMode,
          case_sensitive: input.caseSensitive,
          whole_word: input.wholeWord,
        },
        {
          segmentIds,
          isCurrent,
          onProgress: (count) => {
            if (isCurrent()) progress.value = count
          },
        },
      )
      if (!isCurrent()) return null
      const request = {
        find: input.find,
        replace_with: input.replaceWith,
        match_mode: input.matchMode,
        case_sensitive: input.caseSensitive,
        whole_word: input.wholeWord,
        segment_ids: [...new Set(segments.map((segment) => segment.id))],
      }
      // Empty IDs are never sent: the apply API interprets omission as the whole resource.
      const response: PreviewResponse = request.segment_ids.length
        ? await previewResourceSegmentsSearchReplace(input.projectId, input.resourceId, {
            ...input.scope.filters,
            ...request,
            segment_ids: [...request.segment_ids],
            max_results: 100,
          })
        : { matched_segment_count: 0, total_replacements: 0, items: [] }
      if (!isCurrent()) return null
      const singleSegment =
        singleSegmentId === undefined
          ? undefined
          : segments.find((segment) => segment.id === singleSegmentId)
      if (singleSegment) {
        input.scopeLabel = t('workspace.segment.findReplace.segmentScope', {
          index: singleSegment.segment_index,
        })
      }
      frozen.value = {
        token: ++nextToken,
        epoch,
        version,
        fingerprint: inputFingerprint,
        input,
        request,
        segments: new Map(segments.map((segment) => [segment.id, segment])),
      }
      preview.value = response
      previewStale.value = false
      return response
    } catch (error) {
      if (isCurrent()) recordError(error, t('api.errors.previewSearchReplaceFailed'))
      return null
    } finally {
      if (requestId === requestSequence) previewing.value = false
    }
  }

  const getApplyConfirmation = (segmentId?: number): ReplaceApplyConfirmation | null => {
    const session = frozen.value
    const result = preview.value
    if (!canApply.value || !session || !result) return null
    const item = segmentId === undefined
      ? null
      : result.items.find(candidate => candidate.segment_id === segmentId)
    if (segmentId !== undefined && (!item || !session.request.segment_ids.includes(segmentId))) return null
    return {
      token: session.token,
      projectId: session.input.projectId,
      resourceId: session.input.resourceId,
      find: session.input.find,
      replaceWith: session.input.replaceWith,
      segmentId,
      matchedSegmentCount: item ? 1 : result.matched_segment_count,
      totalReplacements: item ? item.match_count : result.total_replacements,
      scopeLabel: item
        ? t('workspace.segment.findReplace.segmentScope', { index: item.segment_index })
        : session.input.scopeLabel,
    }
  }

  const applyPreview = async (token: number, segmentId?: number): Promise<ApplyResponse | null> => {
    const session = frozen.value
    if (!canApply.value || !session || session.token !== token || !isFrozenCurrent(session)) {
      return null
    }
    if (segmentId !== undefined && !session.request.segment_ids.includes(segmentId)) return null
    const context = { projectId: session.input.projectId, resourceId: session.input.resourceId }
    applying.value = true
    clearError()
    try {
      const result = await applyResourceSegmentsSearchReplace(
        context.projectId,
        context.resourceId,
        {
          ...session.request,
          segment_ids: segmentId === undefined ? [...session.request.segment_ids] : [segmentId],
        },
      )
      if (currentContext(context, session.epoch)) {
        clearPreview()
        lastResult.value = result
        options.onOperation(result.operation_id, context)
      }
      if (!disposed) options.onApplied({ ...context, items: result.items })
      return result
    } catch (error) {
      if (currentContext(context, session.epoch)) {
        recordError(error, t('api.errors.applySearchReplaceFailed'))
        // A transport failure may have occurred after the server applied the write.
        previewStale.value = preview.value !== null
      }
      return null
    } finally {
      applying.value = false
    }
  }

  const getUndoConfirmation = (): ReplaceUndoConfirmation | null => {
    const context = options.context()
    const operationId = options.getOperation()
    if (disposed || busy.value || !context || !operationId) return null
    pendingUndo = {
      token: ++nextToken,
      operationId,
      ...context,
      context: { ...context },
      epoch: resourceEpoch,
      operationVersion,
    }
    return { token: pendingUndo.token, operationId, ...context }
  }

  const undo = async (token: number): Promise<UndoResponse | null> => {
    const session = pendingUndo
    if (
      busy.value ||
      !session ||
      session.token !== token ||
      !currentContext(session.context, session.epoch) ||
      session.operationVersion !== operationVersion ||
      session.operationId !== options.getOperation()
    ) {
      return null
    }
    pendingUndo = null
    const { context, operationId, epoch } = session
    undoing.value = true
    clearError()
    try {
      const result = await undoResourceSegmentsSearchReplace(
        context.projectId,
        context.resourceId,
        operationId,
      )
      if (currentContext(context, epoch)) {
        clearPreview()
        lastResult.value = result
        if (
          session.operationVersion === operationVersion &&
          options.getOperation() === operationId
        ) {
          options.onOperation(result.undo_operation_id, context)
        }
      }
      if (!disposed) options.onApplied({ ...context, items: result.items })
      return result
    } catch (error) {
      if (currentContext(context, epoch)) {
        if (
          isSearchReplaceApplyError(error) &&
          (error.status === 404 || error.status === 409) &&
          session.operationVersion === operationVersion &&
          options.getOperation() === operationId
        ) {
          options.onOperation(null, context)
        }
        recordError(error, t('api.errors.undoSearchReplaceFailed'))
        previewStale.value = preview.value !== null
      }
      return null
    } finally {
      undoing.value = false
    }
  }

  return {
    preview,
    previewStale,
    previewing,
    applying,
    undoing,
    busy,
    errorMessage,
    errorStatus,
    progress,
    lastResult,
    frozenScopeLabel,
    canApply,
    previewChanges,
    getPreviewSegment: (id: number): Segment | undefined => frozen.value?.segments.get(id),
    getApplyConfirmation,
    applyPreview,
    getUndoConfirmation,
    undo,
    clearPreview,
    invalidate,
  }
}
