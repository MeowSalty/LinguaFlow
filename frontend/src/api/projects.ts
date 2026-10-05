import { t } from '@/i18n'
import { countUnicodeCodePoints, SEGMENT_SEARCH_MAX_LENGTH } from '@/utils/unicode'

import type { ApiClient, ApiPaths, ApiSchemas } from './client-core'
import { apiClient, isLocalMode, recoverUnauthorizedResponse } from './client-core'
import {
  buildFilesFormData,
  buildRequestFailureError,
  type DownloadFileResult,
  ApiError,
} from './utils'
import { getAccessToken } from './token-storage'
import { captureSession, assertSessionCurrent, isSessionCurrent } from './session-context'
import {
  storageRequestError,
  storageTransportFailure,
  safeStorageProblem,
  storageTaskErrorMessage,
} from './storage-errors'
import { storageDownloadResult } from './storage'
import { requireStorageId, requireIdempotencyKey } from '@/utils/storage-contract'

export interface UploadProgressCallbacks {
  /** 上传进度回调，percent 范围 0-100 */
  onProgress?: (percent: number) => void
  /** 文件发送完毕，服务端处理中 */
  onServerProcessing?: () => void
}

export interface UploadRequestOptions extends UploadProgressCallbacks {
  idempotencyKey?: string
  signal?: AbortSignal
}

export interface ResourceConflictError extends Error {
  readonly isResourceConflict: true
  readonly status: 409
  readonly conflictData: ApiSchemas['Problem']
}

export interface SegmentTranslationPreviewError extends Error {
  readonly isSegmentTranslationPreviewError: true
  readonly status: number
  readonly retryAfterSeconds?: number
  readonly problem?: ApiSchemas['Problem']
}

export interface SearchReplaceApplyError extends Error {
  readonly isSearchReplaceApplyError: true
  readonly status: number
  readonly problem?: ApiSchemas['Problem']
}

export interface DownloadTranslatedError extends Error {
  readonly isDownloadTranslatedError: true
  readonly status: number
  readonly problem?: ApiSchemas['Problem']
}

export type FetchResourceSegmentsParams = NonNullable<
  ApiPaths['/projects/{projectId}/resources/{resourceId}/segments']['get']['parameters']['query']
>

export type ResourceSegmentQualityCode = NonNullable<FetchResourceSegmentsParams['quality_code']>

/** 搜索/搜索替换的匹配模式（由后端 schema 导出） */
export type SegmentMatchMode = ApiSchemas['SegmentMatchMode']

/** @deprecated 兼容别名，改用 SegmentMatchMode */
export type SearchReplaceMatchMode = SegmentMatchMode

export const isResourceConflictError = (error: unknown): error is ResourceConflictError =>
  error instanceof Error &&
  'isResourceConflict' in error &&
  (error as ResourceConflictError).isResourceConflict === true

export const isSegmentTranslationPreviewError = (
  error: unknown,
): error is SegmentTranslationPreviewError =>
  error instanceof Error &&
  'isSegmentTranslationPreviewError' in error &&
  (error as SegmentTranslationPreviewError).isSegmentTranslationPreviewError === true

export const isSearchReplaceApplyError = (error: unknown): error is SearchReplaceApplyError =>
  error instanceof Error &&
  'isSearchReplaceApplyError' in error &&
  (error as SearchReplaceApplyError).isSearchReplaceApplyError === true

export const isDownloadTranslatedError = (error: unknown): error is DownloadTranslatedError =>
  error instanceof Error &&
  'isDownloadTranslatedError' in error &&
  (error as DownloadTranslatedError).isDownloadTranslatedError === true

export const fetchCurrentUser = async (
  client: ApiClient = apiClient,
): Promise<ApiSchemas['User']> => {
  const { data, error, response } = await client.GET('/users/me')

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchCurrentUserFailed'), error, response)
  }

  return data
}

export const fetchStatsSummary = async (
  client: ApiClient = apiClient,
): Promise<ApiSchemas['UsageStats']> => {
  const { data, error, response } = await client.GET('/stats/summary')

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchStatsFailed'), error, response)
  }

  return data
}

export const fetchProjects = async (
  client: ApiClient = apiClient,
  signal?: AbortSignal,
): Promise<ApiSchemas['ProjectListResponse']> => {
  const { data, error, response } = await client.GET('/projects', { signal })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchProjectsFailed'), error, response)
  }

  return data
}

export const fetchProject = async (
  projectId: number,
  client: ApiClient = apiClient,
  signal?: AbortSignal,
): Promise<ApiSchemas['Project']> => {
  const { data, error, response } = await client.GET('/projects/{projectId}', {
    params: { path: { projectId } },
    signal,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchProjectFailed'), error, response)
  }

  return data
}

export const createProject = async (
  payload: ApiSchemas['CreateProjectRequest'],
  client: ApiClient = apiClient,
): Promise<ApiSchemas['Project']> => {
  const { data, error, response } = await client.POST('/projects', {
    body: payload,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.createProjectFailed'), error, response)
  }

  return data
}

export const updateProject = async (
  projectId: number,
  payload: ApiSchemas['UpdateProjectRequest'],
  client: ApiClient = apiClient,
): Promise<ApiSchemas['Project']> => {
  const { data, error, response } = await client.PUT('/projects/{projectId}', {
    params: { path: { projectId } },
    body: payload,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.updateProjectFailed'), error, response)
  }

  return data
}

export const deleteProject = async (
  projectId: number,
  client: ApiClient = apiClient,
): Promise<void> => {
  const { error, response } = await client.DELETE('/projects/{projectId}', {
    params: { path: { projectId } },
  })

  if (error || response.status !== 204) {
    throw buildRequestFailureError(t('api.errors.deleteProjectFailed'), error, response)
  }
}

export const fetchProjectResources = async (
  projectId: number,
  params?: {
    format?: string
    search?: string
    cursor?: string
    limit?: number
  },
  client: ApiClient = apiClient,
): Promise<ApiSchemas['ResourceListResponse']> => {
  const { data, error, response } = await client.GET('/projects/{projectId}/resources', {
    params: { path: { projectId }, query: params },
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchResourcesFailed'), error, response)
  }

  return data
}

const appendUploadPaths = (formData: FormData, paths?: string[]): void => {
  if (!paths || paths.length === 0) {
    return
  }

  for (const path of paths) {
    formData.append('paths', path)
  }
}

export const precheckProjectResources = async (
  projectId: number,
  paths: string[],
  client: ApiClient = apiClient,
): Promise<ApiSchemas['ResourcePrecheckBatchResponse']> => {
  requireStorageId(projectId)
  const session = captureSession()
  const formData = new FormData()
  for (const path of paths) {
    formData.append('paths', path)
  }

  const { data, response } = await client
    .POST('/projects/{projectId}/resources/precheck', {
      params: { path: { projectId } },
      body: { paths },
      bodySerializer: () => formData,
    })
    .catch((error: unknown) => {
      assertSessionCurrent(session)
      throw storageTransportFailure(error)
    })
  assertSessionCurrent(session)
  if (!response.ok || !data) {
    throw storageRequestError(response)
  }

  return data
}

// Keep the ordered batch, logical paths and key together in the calling session.
export const uploadProjectResources = async (
  projectId: number,
  files: File[],
  paths?: string[],
  client: ApiClient = apiClient,
  options: UploadRequestOptions = {},
): Promise<ApiSchemas['ResourceUploadBatchResponse']> => {
  requireStorageId(projectId)
  const key = requireIdempotencyKey(options.idempotencyKey ?? crypto.randomUUID())
  const session = captureSession()
  const formData = buildFilesFormData(files, 'files')
  appendUploadPaths(formData, paths)
  const { data, response, error } = await client
    .POST('/projects/{projectId}/resources', {
      params: { path: { projectId }, header: { 'Idempotency-Key': key } },
      signal: AbortSignal.any([session.signal, ...(options.signal ? [options.signal] : [])]),
      body: { files: files.map((file) => file.name), paths },
      bodySerializer: () => formData,
    })
    .catch((error: unknown) => {
      assertSessionCurrent(session)
      throw storageTransportFailure(error)
    })
  assertSessionCurrent(session)
  if (!response.ok || !data) throw storageRequestError(response, error)
  return sanitizeUploadBatch(data, files.length)
}

export const sanitizeUploadBatch = (
  input: unknown,
  count: number,
): ApiSchemas['ResourceUploadBatchResponse'] => {
  if (
    !input ||
    typeof input !== 'object' ||
    !('items' in input) ||
    !Array.isArray(input.items) ||
    input.items.length !== count
  )
    throw storageRequestError()
  const items = input.items.map((value: unknown) => {
    if (
      !value ||
      typeof value !== 'object' ||
      !('action' in value) ||
      !('path' in value) ||
      typeof value.path !== 'string' ||
      !['created', 'conflict', 'failed'].includes(String(value.action))
    )
      throw storageRequestError()
    const item = value as ApiSchemas['ResourceUploadFileResult']
    const safe = safeStorageProblem(value)
    return {
      path: item.path,
      action: item.action,
      resource: item.resource,
      existing_resource: item.existing_resource,
      error_code: safe.error_code,
      error:
        item.error || item.action === 'failed'
          ? storageTaskErrorMessage(safe.error_code)
          : undefined,
    }
  })
  return {
    items,
    ...('operation_id' in input ? { operation_id: safeStorageProblem(input).operation_id } : {}),
  }
}

export const uploadProjectResourcesWithProgress = async (
  projectId: number,
  files: File[],
  paths?: string[],
  callbacks: UploadRequestOptions = {},
): Promise<ApiSchemas['ResourceUploadBatchResponse']> => {
  requireStorageId(projectId)
  const session = captureSession()
  const key = requireIdempotencyKey(callbacks.idempotencyKey ?? crypto.randomUUID())
  const signal = AbortSignal.any([session.signal, ...(callbacks.signal ? [callbacks.signal] : [])])
  const url = session.baseUrl + '/projects/' + projectId + '/resources'
  // Freeze the input before any authentication wait.
  const batchFiles = [...files]
  const batchPaths = paths ? [...paths] : undefined

  const send = (token: string | null): Promise<{ status: number; text: string }> =>
    new Promise((resolve, reject) => {
      signal.throwIfAborted()
      assertSessionCurrent(session)
      const formData = buildFilesFormData(batchFiles, 'files')
      appendUploadPaths(formData, batchPaths)
      const xhr = new XMLHttpRequest()
      let settled = false
      let processing = false
      const finish = (result?: { status: number; text: string }, failure?: unknown) => {
        if (settled) return
        settled = true
        signal.removeEventListener('abort', abort)
        try {
          assertSessionCurrent(session)
          signal.throwIfAborted()
          if (failure) reject(failure)
          else if (result) resolve(result)
        } catch (error) {
          reject(error)
        }
      }
      const abort = () => {
        xhr.abort()
        finish(undefined, signal.reason ?? new DOMException('Aborted', 'AbortError'))
      }
      signal.addEventListener('abort', abort, { once: true })
      xhr.upload.addEventListener('progress', (event) => {
        if (settled || signal.aborted || !isSessionCurrent(session)) return
        if (event.lengthComputable) {
          const percent = Math.min(100, Math.round((event.loaded / event.total) * 100))
          callbacks.onProgress?.(percent)
          if (percent === 100 && !processing) {
            processing = true
            callbacks.onServerProcessing?.()
          }
        }
      })
      xhr.addEventListener('load', () => finish({ status: xhr.status, text: xhr.responseText }))
      xhr.addEventListener('error', () => finish(undefined, storageRequestError()))
      xhr.addEventListener('abort', () =>
        finish(undefined, new DOMException('Aborted', 'AbortError')),
      )
      xhr.open('POST', url)
      if (token && !isLocalMode()) xhr.setRequestHeader('Authorization', 'Bearer ' + token)
      xhr.setRequestHeader('Idempotency-Key', key)
      xhr.send(formData)
    })

  const first = await send(getAccessToken())
  const result = await recoverUnauthorizedResponse(first, session, send).catch((error: unknown) => {
    assertSessionCurrent(session)
    signal.throwIfAborted()
    if (error instanceof Error && error.name === 'AbortError') throw error
    throw storageRequestError(
      error instanceof ApiError && error.status !== undefined
        ? { status: error.status }
        : undefined,
    )
  })
  assertSessionCurrent(session)
  signal.throwIfAborted()
  if (result.status < 200 || result.status >= 300) throw storageRequestError(result, result.text)
  let parsed: unknown
  try {
    parsed = JSON.parse(result.text)
  } catch {
    throw storageRequestError()
  }
  return sanitizeUploadBatch(parsed, batchFiles.length)
}

export const deleteProjectResource = async (
  projectId: number,
  resourceId: number,
  client: ApiClient = apiClient,
): Promise<void> => {
  const { error, response } = await client.DELETE('/projects/{projectId}/resources/{resourceId}', {
    params: { path: { projectId, resourceId } },
  })

  if (error || response.status !== 204) {
    throw storageRequestError(response, error)
  }
}

export const downloadProjectResource = async (
  projectId: number,
  resourceId: number,
  client: ApiClient = apiClient,
): Promise<DownloadFileResult> => {
  requireStorageId(projectId)
  requireStorageId(resourceId)
  const session = captureSession()
  try {
    const result = await client.GET('/projects/{projectId}/resources/{resourceId}/download', {
      params: { path: { projectId, resourceId } },
      parseAs: 'blob',
    })
    assertSessionCurrent(session)
    const download = await storageDownloadResult(result)
    assertSessionCurrent(session)
    return download
  } catch (error) {
    assertSessionCurrent(session)
    throw error instanceof ApiError && !error.problem ? error : storageTransportFailure(error)
  }
}

export const downloadResourceResult = async (
  projectId: number,
  resourceId: number,
  client: ApiClient = apiClient,
): Promise<DownloadFileResult> => {
  requireStorageId(projectId)
  requireStorageId(resourceId)
  const session = captureSession()
  try {
    const result = await client.GET(
      '/projects/{projectId}/resources/{resourceId}/download-translated',
      {
        params: { path: { projectId, resourceId } },
        parseAs: 'blob',
      },
    )
    assertSessionCurrent(session)
    const download = await storageDownloadResult(result)
    assertSessionCurrent(session)
    return download
  } catch (error) {
    assertSessionCurrent(session)
    const failure =
      error instanceof ApiError && !error.problem ? error : storageTransportFailure(error)
    Object.defineProperty(failure, 'isDownloadTranslatedError', { value: true, enumerable: false })
    throw failure
  }
}

export const fetchProjectResourceTree = async (
  projectId: number,
  client: ApiClient = apiClient,
): Promise<ApiSchemas['ResourceTreeResponse']> => {
  const { data, error, response } = await client.GET('/projects/{projectId}/resources/tree', {
    params: { path: { projectId } },
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchResourceTreeFailed'), error, response)
  }

  return data
}

/**
 * 搜索/查找文本超限时本地失败：不发网络请求，避免后端 400 与未知副作用。
 * 仅检查长度，不 trim/截断；按 Unicode code point 计与 API 一致。
 */
const assertSegmentSearchLength = (value: string, tooLongMessage: string): void => {
  if (countUnicodeCodePoints(value) > SEGMENT_SEARCH_MAX_LENGTH) {
    throw new RangeError(tooLongMessage)
  }
}

export const fetchResourceSegments = async (
  projectId: number,
  resourceId: number,
  params?: FetchResourceSegmentsParams,
  client: ApiClient = apiClient,
): Promise<ApiSchemas['ResourceSegmentListResponse']> => {
  if (params?.search) {
    assertSegmentSearchLength(
      params.search,
      t('api.errors.segmentSearchTooLong', { max: SEGMENT_SEARCH_MAX_LENGTH }),
    )
  }

  const { data, error, response } = await client.GET(
    '/projects/{projectId}/resources/{resourceId}/segments',
    {
      params: {
        path: { projectId, resourceId },
        query: params,
      },
    },
  )

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchSegmentsFailed'), error, response)
  }

  return data
}

export const updateResourceSegment = async (
  projectId: number,
  resourceId: number,
  segmentId: number,
  payload: ApiSchemas['ResourceSegmentUpdateRequest'],
  client: ApiClient = apiClient,
): Promise<ApiSchemas['Segment']> => {
  const { data, error, response } = await client.PATCH(
    '/projects/{projectId}/resources/{resourceId}/segments/{segmentId}',
    {
      params: { path: { projectId, resourceId, segmentId } },
      body: payload,
    },
  )

  if (!data) {
    throw buildRequestFailureError(t('api.errors.updateSegmentFailed'), error, response)
  }

  return data
}

const buildSegmentTranslationPreviewError = (
  fallbackMessage: string,
  error: unknown,
  response?: Response,
): SegmentTranslationPreviewError => {
  const failure = buildRequestFailureError(fallbackMessage, error, response)
  const previewError = failure as SegmentTranslationPreviewError
  const problem =
    error && typeof error === 'object' && 'title' in error
      ? (error as ApiSchemas['Problem'])
      : undefined
  const retryAfterHeader = response?.headers.get('Retry-After')
  const parsedRetryAfter = retryAfterHeader === null ? Number.NaN : Number(retryAfterHeader)

  Object.defineProperties(previewError, {
    isSegmentTranslationPreviewError: { value: true, enumerable: false },
    status: { value: response?.status ?? problem?.status ?? 0, enumerable: false },
    retryAfterSeconds: {
      value: Number.isFinite(parsedRetryAfter) ? parsedRetryAfter : undefined,
      enumerable: false,
    },
    problem: { value: problem, enumerable: false },
  })

  return previewError
}

export const previewResourceSegmentTranslation = async (
  projectId: number,
  resourceId: number,
  segmentId: number,
  executionPlanId: number,
  signal?: AbortSignal,
  client: ApiClient = apiClient,
): Promise<ApiSchemas['SegmentTranslationPreviewResponse']> => {
  const { data, error, response } = await client.POST(
    '/projects/{projectId}/resources/{resourceId}/segments/{segmentId}/translation-preview',
    {
      params: { path: { projectId, resourceId, segmentId } },
      body: { execution_plan_id: executionPlanId },
      signal,
    },
  )

  if (!data) {
    throw buildSegmentTranslationPreviewError(
      t('api.errors.previewSegmentTranslationFailed'),
      error,
      response,
    )
  }

  return data
}

export const previewResourceSegmentRevision = async (
  projectId: number,
  resourceId: number,
  segmentId: number,
  executionPlanId: number,
  issueCodes?: NonNullable<ApiSchemas['SegmentRevisionPreviewRequest']['issue_codes']>,
  signal?: AbortSignal,
  client: ApiClient = apiClient,
): Promise<ApiSchemas['SegmentRevisionPreviewResponse']> => {
  const { data, error, response } = await client.POST(
    '/projects/{projectId}/resources/{resourceId}/segments/{segmentId}/revision-preview',
    {
      params: { path: { projectId, resourceId, segmentId } },
      body: {
        execution_plan_id: executionPlanId,
        ...(issueCodes && issueCodes.length > 0 ? { issue_codes: issueCodes } : {}),
      },
      signal,
    },
  )

  if (!data) {
    throw buildSegmentTranslationPreviewError(
      t('api.errors.previewSegmentRevisionFailed'),
      error,
      response,
    )
  }

  return data
}

export const applyResourceSegmentTranslationPreview = async (
  projectId: number,
  resourceId: number,
  segmentId: number,
  applyToken: string,
  targetText: string,
  client: ApiClient = apiClient,
): Promise<ApiSchemas['Segment']> => {
  const { data, error, response } = await client.POST(
    '/projects/{projectId}/resources/{resourceId}/segments/{segmentId}/translation-preview/apply',
    {
      params: { path: { projectId, resourceId, segmentId } },
      body: { apply_token: applyToken, target_text: targetText },
    },
  )

  if (!data) {
    throw buildSegmentTranslationPreviewError(
      t('api.errors.applySegmentTranslationPreviewFailed'),
      error,
      response,
    )
  }

  return data
}

export const batchReviewSegments = async (
  projectId: number,
  resourceId: number,
  segmentIds: number[],
  action: 'approve' | 'reject',
  comment?: string,
  client: ApiClient = apiClient,
): Promise<ApiSchemas['BatchReviewResponse']> => {
  const { data, error, response } = await client.POST(
    '/projects/{projectId}/resources/{resourceId}/segments/batch-review',
    {
      params: { path: { projectId, resourceId } },
      body: { segment_ids: segmentIds, action, comment },
    },
  )

  if (!data) {
    throw buildRequestFailureError(t('api.errors.batchReviewSegmentsFailed'), error, response)
  }

  return data
}

export const approveAllSegments = async (
  projectId: number,
  resourceId: number,
  client: ApiClient = apiClient,
): Promise<ApiSchemas['ApproveAllResponse']> => {
  const { data, error, response } = await client.POST(
    '/projects/{projectId}/resources/{resourceId}/segments/approve-all',
    {
      params: { path: { projectId, resourceId } },
    },
  )

  if (!data) {
    throw buildRequestFailureError(t('api.errors.approveAllSegmentsFailed'), error, response)
  }

  return data
}

export const setResourceSegmentIssueDisposition = async (
  projectId: number,
  resourceId: number,
  segmentId: number,
  payload: ApiSchemas['IssueDispositionRequest'],
  client: ApiClient = apiClient,
): Promise<ApiSchemas['Segment']> => {
  const { data, error, response } = await client.POST(
    '/projects/{projectId}/resources/{resourceId}/segments/{segmentId}/issues/disposition',
    {
      params: { path: { projectId, resourceId, segmentId } },
      body: payload,
    },
  )

  if (!data) {
    throw buildRequestFailureError(t('api.errors.setIssueDispositionFailed'), error, response)
  }

  return data
}

/** 搜索替换预览/应用请求的公共参数 */
export interface SearchReplaceParams {
  find: string
  replace_with: string
  match_mode: SearchReplaceMatchMode
  case_sensitive: boolean
  whole_word: boolean
}

const buildSearchReplaceProblemError = (
  fallbackMessage: string,
  error: unknown,
  response?: Response,
): SearchReplaceApplyError => {
  const failure = buildRequestFailureError(fallbackMessage, error, response)
  const problemError = failure as SearchReplaceApplyError
  const problem =
    error && typeof error === 'object' && 'title' in error
      ? (error as ApiSchemas['Problem'])
      : undefined

  Object.defineProperties(problemError, {
    isSearchReplaceApplyError: { value: true, enumerable: false },
    status: { value: response?.status ?? problem?.status ?? 0, enumerable: false },
    problem: { value: problem, enumerable: false },
  })

  return problemError
}

export const previewResourceSegmentsSearchReplace = async (
  projectId: number,
  resourceId: number,
  payload: SearchReplaceParams & Partial<ApiSchemas['SearchReplacePreviewRequest']>,
  client: ApiClient = apiClient,
): Promise<ApiSchemas['SearchReplacePreviewResponse']> => {
  const { find, replace_with, match_mode, case_sensitive, whole_word, ...filters } = payload
  assertSegmentSearchLength(
    find,
    t('api.errors.searchReplaceFindTooLong', { max: SEGMENT_SEARCH_MAX_LENGTH }),
  )
  const { data, error, response } = await client.POST(
    '/projects/{projectId}/resources/{resourceId}/segments/search-replace/preview',
    {
      params: { path: { projectId, resourceId } },
      body: {
        find,
        replace_with,
        match_mode,
        case_sensitive,
        whole_word,
        ...filters,
        max_results: filters.max_results ?? 20,
      },
    },
  )

  if (!data) {
    throw buildSearchReplaceProblemError(
      t('api.errors.previewSearchReplaceFailed'),
      error,
      response,
    )
  }

  return data
}

export const applyResourceSegmentsSearchReplace = async (
  projectId: number,
  resourceId: number,
  payload: SearchReplaceParams & Pick<ApiSchemas['SearchReplaceApplyRequest'], 'segment_ids'>,
  client: ApiClient = apiClient,
): Promise<ApiSchemas['SearchReplaceApplyResponse']> => {
  const { find, replace_with, match_mode, case_sensitive, whole_word, ...rest } = payload
  // 空选择不能因省略 segment_ids 而扩大成整个资源。
  if (rest.segment_ids?.length === 0) {
    throw new Error(t('api.errors.searchReplaceEmptyScope'))
  }
  assertSegmentSearchLength(
    find,
    t('api.errors.searchReplaceFindTooLong', { max: SEGMENT_SEARCH_MAX_LENGTH }),
  )
  const { data, error, response } = await client.POST(
    '/projects/{projectId}/resources/{resourceId}/segments/search-replace/apply',
    {
      params: { path: { projectId, resourceId } },
      body: {
        find,
        replace_with,
        match_mode,
        case_sensitive,
        whole_word,
        ...(rest.segment_ids && rest.segment_ids.length > 0
          ? { segment_ids: rest.segment_ids }
          : {}),
      },
    },
  )

  if (!data) {
    throw buildSearchReplaceProblemError(t('api.errors.applySearchReplaceFailed'), error, response)
  }

  return data
}

export const undoResourceSegmentsSearchReplace = async (
  projectId: number,
  resourceId: number,
  operationId: string,
  client: ApiClient = apiClient,
): Promise<ApiSchemas['SearchReplaceUndoResponse']> => {
  const { data, error, response } = await client.POST(
    '/projects/{projectId}/resources/{resourceId}/segments/search-replace/{operationId}/undo',
    {
      params: { path: { projectId, resourceId, operationId } },
    },
  )

  if (!data) {
    throw buildSearchReplaceProblemError(t('api.errors.undoSearchReplaceFailed'), error, response)
  }

  return data
}

export const qaRecheck = async (
  projectId: number,
  payload: ApiSchemas['QaRecheckRequest'],
  client: ApiClient = apiClient,
): Promise<ApiSchemas['QaRecheckResult']> => {
  const { data, error, response } = await client.POST('/projects/{projectId}/qa-recheck', {
    params: { path: { projectId } },
    body: payload,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.qaRecheckFailed'), error, response)
  }

  return data
}

export const createOrgProject = async (
  orgId: number,
  payload: ApiSchemas['CreateProjectRequest'],
  client: ApiClient = apiClient,
): Promise<ApiSchemas['Project']> => {
  const { data, error, response } = await client.POST('/orgs/{orgId}/projects', {
    params: { path: { orgId } },
    body: payload,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.createProjectFailed'), error, response)
  }

  return data
}

export const fetchOrgProjects = async (
  orgId: number,
  client: ApiClient = apiClient,
  signal?: AbortSignal,
): Promise<ApiSchemas['ProjectListResponse']> => {
  const { data, error, response } = await client.GET('/orgs/{orgId}/projects', {
    params: { path: { orgId } },
    signal,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchProjectsFailed'), error, response)
  }

  return data
}
