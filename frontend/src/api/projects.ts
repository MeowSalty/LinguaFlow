import { t } from '@/i18n'

import type { ApiClient, ApiSchemas } from './client'
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
    status?: 'ready' | 'processing' | 'error'
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

export const uploadProjectResources = async (
  projectId: number,
  files: File[],
  client: ApiClient = apiClient,
): Promise<ApiSchemas['ResourceUploadResponse']> => {
  const { data, error, response } = await client.POST('/projects/{projectId}/resources', {
    params: { path: { projectId } },
    body: buildFilesFormData(files, 'files') as unknown as {
      files: File[]
    },
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.uploadResourcesFailed'), error, response)
  }

  return data
}

export const uploadProjectResourcesWithProgress = async (
  projectId: number,
  files: File[],
  callbacks?: UploadProgressCallbacks,
): Promise<ApiSchemas['ResourceUploadResponse']> => {
  const baseUrl = readStoredApiBaseUrl() ?? '/api/v1'
  const normalizedBaseUrl = baseUrl.endsWith('/') ? baseUrl.slice(0, -1) : baseUrl
  const url = `${normalizedBaseUrl}/projects/${projectId}/resources`

  const formData = buildFilesFormData(files, 'files')
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
          const data = JSON.parse(xhr.responseText) as ApiSchemas['ResourceUploadResponse']
          resolve(data)
        } catch {
          reject(new Error(t('api.errors.uploadResourcesFailed')))
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
      body: buildFilesFormData([file], 'file') as unknown as {
        file: File
      },
    },
  )

  if (!data) {
    throw buildRequestFailureError(t('api.errors.replaceResourceFailed'), error, response)
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

export const fetchResourceSegments = async (
  projectId: number,
  resourceId: number,
  params?: {
    status?: 'pending' | 'translated' | 'reviewed' | 'rejected'
    search?: string
    cursor?: string
    limit?: number
  },
  client: ApiClient = apiClient,
): Promise<ApiSchemas['ResourceSegmentListResponse']> => {
  const { data, error, response } = await client.GET(
    '/projects/{projectId}/resources/{resourceId}/segments',
    {
      params: { path: { projectId, resourceId }, query: params },
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
