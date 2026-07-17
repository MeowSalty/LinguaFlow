import { t } from '@/i18n'

import type { ApiClient, ApiSchemas } from './client'
import { apiClient } from './client'
import { buildRequestFailureError } from './utils'

type GlossaryEntry = ApiSchemas['GlossaryEntry']
type CreateGlossaryEntryRequest = ApiSchemas['CreateGlossaryEntryRequest']
type UpdateGlossaryEntryRequest = ApiSchemas['UpdateGlossaryEntryRequest']
type UpdateGlossaryEntryResponse = ApiSchemas['UpdateGlossaryEntryResponse']
type SyncImpactRequest = ApiSchemas['GlossarySyncImpactRequest']
type SyncImpactResponse = ApiSchemas['GlossarySyncImpactResponse']
type SyncExecuteRequest = ApiSchemas['GlossarySyncExecuteRequest']
type SyncExecuteResponse = ApiSchemas['GlossarySyncExecuteResponse']
type SyncTaskStatusResponse = ApiSchemas['GlossarySyncTaskStatusResponse']
type SyncTaskCancelResponse = ApiSchemas['GlossarySyncTaskCancelResponse']
type GlossaryPruneRequest = ApiSchemas['GlossaryPruneRequest']
type GlossaryPrunePreview = ApiSchemas['GlossaryPrunePreview']
type GlossaryPruneApplyRequest = ApiSchemas['GlossaryPruneApplyRequest']
type GlossaryPruneApplyResult = ApiSchemas['GlossaryPruneApplyResult']

export const fetchGlossaryEntries = async (
  projectId: number,
  client: ApiClient = apiClient,
): Promise<ApiSchemas['GlossaryListResponse']> => {
  const { data, error, response } = await client.GET('/projects/{projectId}/glossary', {
    params: { path: { projectId } },
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchGlossaryFailed'), error, response)
  }

  return data
}

export const createGlossaryEntry = async (
  projectId: number,
  payload: CreateGlossaryEntryRequest,
  client: ApiClient = apiClient,
): Promise<GlossaryEntry> => {
  const { data, error, response } = await client.POST('/projects/{projectId}/glossary', {
    params: { path: { projectId } },
    body: payload,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.createGlossaryEntryFailed'), error, response)
  }

  return data
}

export const updateGlossaryEntry = async (
  projectId: number,
  entryId: number,
  payload: UpdateGlossaryEntryRequest,
  client: ApiClient = apiClient,
): Promise<UpdateGlossaryEntryResponse> => {
  const { data, error, response } = await client.PUT('/projects/{projectId}/glossary/{entryId}', {
    params: { path: { projectId, entryId } },
    body: payload,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.updateGlossaryEntryFailed'), error, response)
  }

  return data
}

export const deleteGlossaryEntry = async (
  projectId: number,
  entryId: number,
  client: ApiClient = apiClient,
): Promise<void> => {
  const { error, response } = await client.DELETE('/projects/{projectId}/glossary/{entryId}', {
    params: { path: { projectId, entryId } },
  })

  if (response && !response.ok) {
    throw buildRequestFailureError(t('api.errors.deleteGlossaryEntryFailed'), error, response)
  }
}

export const importGlossaryCSV = async (
  projectId: number,
  file: File,
  client: ApiClient = apiClient,
): Promise<ApiSchemas['GlossaryImportResult']> => {
  const { data, error, response } = await client.POST('/projects/{projectId}/glossary/import', {
    params: { path: { projectId } },
    body: { file },
    bodySerializer: () => {
      const formData = new FormData()
      formData.append('file', file)
      return formData
    },
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.importGlossaryFailed'), error, response)
  }

  return data
}

export const exportGlossaryCSV = async (
  projectId: number,
  client: ApiClient = apiClient,
): Promise<Blob> => {
  const { data, error, response } = await client.GET('/projects/{projectId}/glossary/export', {
    params: { path: { projectId } },
    parseAs: 'blob',
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.exportGlossaryFailed'), error, response)
  }

  return data as unknown as Blob
}

/**
 * 分析术语译文变更对已翻译段落的影响。
 * @param projectId - 项目 ID
 * @param entryId - 术语条目 ID
 * @param payload - 包含 old_target、可选 new_target 和 resource_ids
 * @returns 影响分析结果（受影响段落数、资源分布）
 */
export const analyzeGlossarySyncImpact = async (
  projectId: number,
  entryId: number,
  payload: SyncImpactRequest,
  client: ApiClient = apiClient,
): Promise<SyncImpactResponse> => {
  const { data, error, response } = await client.POST(
    '/projects/{projectId}/glossary/{entryId}/sync-impact',
    {
      params: { path: { projectId, entryId } },
      body: payload,
    },
  )

  if (!data) {
    throw buildRequestFailureError(t('api.errors.glossarySyncImpactFailed'), error, response)
  }

  return data
}

/**
 * 提交异步同步任务。
 * @returns 包含 task_id 和 status_url 的响应（HTTP 202）
 */
export const executeGlossarySync = async (
  projectId: number,
  entryId: number,
  payload: SyncExecuteRequest,
  client: ApiClient = apiClient,
): Promise<SyncExecuteResponse> => {
  const { data, error, response } = await client.POST(
    '/projects/{projectId}/glossary/{entryId}/sync-execute',
    {
      params: { path: { projectId, entryId } },
      body: payload,
    },
  )

  if (!data) {
    throw buildRequestFailureError(t('api.errors.glossarySyncExecuteFailed'), error, response)
  }

  return data
}

/**
 * 查询同步任务的当前状态和进度。
 */
export const getGlossarySyncTaskStatus = async (
  projectId: number,
  taskId: string,
  client: ApiClient = apiClient,
): Promise<SyncTaskStatusResponse> => {
  const { data, error, response } = await client.GET('/projects/{projectId}/sync-tasks/{taskId}', {
    params: { path: { projectId, taskId } },
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.glossarySyncStatusFailed'), error, response)
  }

  return data
}

/**
 * 取消正在执行的同步任务。
 */
export const cancelGlossarySyncTask = async (
  projectId: number,
  taskId: string,
  client: ApiClient = apiClient,
): Promise<SyncTaskCancelResponse> => {
  const { data, error, response } = await client.POST(
    '/projects/{projectId}/sync-tasks/{taskId}/cancel',
    {
      params: { path: { projectId, taskId } },
    },
  )

  if (!data) {
    throw buildRequestFailureError(t('api.errors.glossarySyncCancelFailed'), error, response)
  }

  return data
}

export const previewGlossaryPrune = async (
  projectId: number,
  payload: GlossaryPruneRequest,
  client: ApiClient = apiClient,
): Promise<GlossaryPrunePreview> => {
  const { data, error, response } = await client.POST('/projects/{projectId}/glossary/prune', {
    params: { path: { projectId } },
    body: payload,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.glossaryPrunePreviewFailed'), error, response)
  }

  return data
}

export const applyGlossaryPrune = async (
  projectId: number,
  payload: GlossaryPruneApplyRequest,
  client: ApiClient = apiClient,
): Promise<GlossaryPruneApplyResult> => {
  const { data, error, response } = await client.POST(
    '/projects/{projectId}/glossary/prune/apply',
    {
      params: { path: { projectId } },
      body: payload,
    },
  )

  if (!data) {
    throw buildRequestFailureError(t('api.errors.glossaryPruneApplyFailed'), error, response)
  }

  return data
}
