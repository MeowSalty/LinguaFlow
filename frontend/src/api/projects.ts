import { t } from '@/i18n'

import type { ApiClient, ApiPaths, ApiSchemas } from './client'
import { apiClient } from './client'
import {
  buildFilesFormData,
  buildRequestFailureError,
  type DownloadFileResult,
  getContentDispositionFilename,
} from './utils'
import { getAccessToken, readStoredApiBaseUrl } from './token-storage'

export interface UploadProgressCallbacks {
  /** 上传进度回调，percent 范围 0-100 */
  onProgress?: (percent: number) => void
  /** 文件发送完毕，服务端处理中 */
  onServerProcessing?: () => void
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

export type FetchResourceSegmentsParams = NonNullable<
  ApiPaths['/projects/{projectId}/resources/{resourceId}/segments']['get']['parameters']['query']
>

export type ResourceSegmentQualityCode = NonNullable<FetchResourceSegmentsParams['quality_code']>

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
): Promise<ApiSchemas['ProjectListResponse']> => {
  const { data, error, response } = await client.GET('/projects')

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchProjectsFailed'), error, response)
  }

  return data
}

export const fetchProject = async (
  projectId: number,
  client: ApiClient = apiClient,
): Promise<ApiSchemas['Project']> => {
  const { data, error, response } = await client.GET('/projects/{projectId}', {
    params: { path: { projectId } },
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
  const formData = new FormData()
  for (const path of paths) {
    formData.append('paths', path)
  }

  const { data, error, response } = await client.POST('/projects/{projectId}/resources/precheck', {
    params: { path: { projectId } },
    body: formData as unknown as {
      paths: string[]
    },
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.precheckResourcesFailed'), error, response)
  }

  return data
}

export const uploadProjectResources = async (
  projectId: number,
  files: File[],
  paths?: string[],
  client: ApiClient = apiClient,
): Promise<ApiSchemas['ResourceUploadBatchResponse']> => {
  const formData = buildFilesFormData(files, 'files')
  appendUploadPaths(formData, paths)

  const { data, error, response } = await client.POST('/projects/{projectId}/resources', {
    params: { path: { projectId } },
    body: formData as unknown as NonNullable<
      ApiPaths['/projects/{projectId}/resources']['post']['requestBody']
    >['content']['multipart/form-data'],
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.uploadResourcesFailed'), error, response)
  }

  return data
}

export const uploadProjectResourcesWithProgress = async (
  projectId: number,
  files: File[],
  paths?: string[],
  callbacks?: UploadProgressCallbacks,
): Promise<ApiSchemas['ResourceUploadBatchResponse']> => {
  const baseUrl = readStoredApiBaseUrl() ?? '/api/v1'
  const normalizedBaseUrl = baseUrl.endsWith('/') ? baseUrl.slice(0, -1) : baseUrl
  const url = `${normalizedBaseUrl}/projects/${projectId}/resources`

  const formData = buildFilesFormData(files, 'files')
  appendUploadPaths(formData, paths)
  const accessToken = getAccessToken()

  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest()
    let serverProcessingNotified = false

    xhr.upload.addEventListener('progress', (event) => {
      if (event.lengthComputable && callbacks?.onProgress) {
        const percent = Math.round((event.loaded / event.total) * 100)
        callbacks.onProgress(percent)
        if (percent >= 100 && !serverProcessingNotified) {
          serverProcessingNotified = true
          callbacks?.onServerProcessing?.()
        }
      }
    })

    xhr.addEventListener('load', () => {
      if (xhr.status >= 200 && xhr.status < 300) {
        try {
          const data = JSON.parse(xhr.responseText) as ApiSchemas['ResourceUploadBatchResponse']
          resolve(data)
        } catch {
          reject(new Error(t('api.errors.uploadResourcesFailed')))
        }
      } else if (xhr.status === 409) {
        try {
          const conflictData = JSON.parse(xhr.responseText) as ApiSchemas['Problem']
          const conflictError = new Error(
            t('api.errors.uploadResourceConflict'),
          ) as ResourceConflictError
          Object.defineProperty(conflictError, 'isResourceConflict', {
            value: true as const,
            enumerable: false,
          })
          Object.defineProperty(conflictError, 'status', {
            value: 409 as const,
            enumerable: false,
          })
          Object.defineProperty(conflictError, 'conflictData', {
            value: conflictData,
            enumerable: false,
          })
          reject(conflictError)
        } catch {
          reject(
            buildRequestFailureError(
              t('api.errors.uploadResourcesFailed'),
              undefined,
              new Response(null, { status: xhr.status }),
            ),
          )
        }
      } else {
        reject(
          buildRequestFailureError(
            t('api.errors.uploadResourcesFailed'),
            undefined,
            new Response(null, { status: xhr.status }),
          ),
        )
      }
    })

    xhr.addEventListener('error', () => {
      reject(buildRequestFailureError(t('api.errors.uploadResourcesFailed')))
    })

    xhr.addEventListener('abort', () => {
      reject(buildRequestFailureError(t('api.errors.uploadResourcesFailed')))
    })

    xhr.open('POST', url)

    if (accessToken) {
      xhr.setRequestHeader('Authorization', `Bearer ${accessToken}`)
    }

    xhr.send(formData)
  })
}

export const replaceProjectResource = async (
  projectId: number,
  resourceId: number,
  file: File,
  client: ApiClient = apiClient,
): Promise<ApiSchemas['Resource']> => {
  const { data, error, response } = await client.PUT(
    '/projects/{projectId}/resources/{resourceId}',
    {
      params: { path: { projectId, resourceId } },
      body: buildFilesFormData([file], 'file') as unknown as NonNullable<
        ApiPaths['/projects/{projectId}/resources/{resourceId}']['put']['requestBody']
      >['content']['multipart/form-data'],
    },
  )

  if (!data) {
    throw buildRequestFailureError(t('api.errors.replaceResourceFailed'), error, response)
  }

  return data
}

export const incrementalUpdateResource = async (
  projectId: number,
  resourceId: number,
  file: File,
  client: ApiClient = apiClient,
): Promise<ApiSchemas['IncrementalUpdateResponse']> => {
  const { data, error, response } = await client.POST(
    '/projects/{projectId}/resources/{resourceId}',
    {
      params: { path: { projectId, resourceId } },
      body: buildFilesFormData([file], 'file') as unknown as NonNullable<
        ApiPaths['/projects/{projectId}/resources/{resourceId}']['post']['requestBody']
      >['content']['multipart/form-data'],
    },
  )

  if (!data) {
    throw buildRequestFailureError(t('api.errors.incrementalUpdateFailed'), error, response)
  }

  return data
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
    throw buildRequestFailureError(t('api.errors.deleteResourceFailed'), error, response)
  }
}

export const downloadProjectResource = async (
  projectId: number,
  resourceId: number,
  client: ApiClient = apiClient,
): Promise<DownloadFileResult> => {
  const { data, error, response } = await client.GET(
    '/projects/{projectId}/resources/{resourceId}/download',
    {
      params: { path: { projectId, resourceId } },
      parseAs: 'blob',
    },
  )

  if (!data) {
    throw buildRequestFailureError(t('api.errors.downloadResourceFailed'), error, response)
  }

  return {
    blob: data as Blob,
    filename: getContentDispositionFilename(response),
  }
}

export const downloadResourceResult = async (
  projectId: number,
  resourceId: number,
  client: ApiClient = apiClient,
): Promise<DownloadFileResult> => {
  const { data, error, response } = await client.GET(
    '/projects/{projectId}/resources/{resourceId}/download-translated',
    {
      params: { path: { projectId, resourceId } },
      parseAs: 'blob',
    },
  )

  if (!data) {
    throw buildRequestFailureError(t('api.errors.downloadResourceResultFailed'), error, response)
  }

  return {
    blob: data as Blob,
    filename: getContentDispositionFilename(response),
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

export const fetchResourceSegments = async (
  projectId: number,
  resourceId: number,
  params?: FetchResourceSegmentsParams,
  client: ApiClient = apiClient,
): Promise<ApiSchemas['ResourceSegmentListResponse']> => {
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
): Promise<ApiSchemas['ProjectListResponse']> => {
  const { data, error, response } = await client.GET('/orgs/{orgId}/projects', {
    params: { path: { orgId } },
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchProjectsFailed'), error, response)
  }

  return data
}
