import { t } from '@/i18n'

import type { ApiClient, ApiSchemas } from './client'
import { apiClient } from './client'
import { buildRequestFailureError } from './utils'

type ResourceSegmentGroup = ApiSchemas['ResourceSegmentGroup']
type ResourceSegmentGroupListResponse = ApiSchemas['ResourceSegmentGroupListResponse']

/**
 * 获取资源的段落分组列表（按章节）
 *
 * 适用于 EPUB 等多章节资源。非 EPUB 资源返回包含所有 segments 的单一组。
 *
 * @param projectId - 项目 ID
 * @param resourceId - 资源 ID
 * @param client - 可选的 API 客户端实例
 * @returns 分组列表，每组包含 group_key、group_title、segment_count、translated_count
 */
export const fetchSegmentGroups = async (
  projectId: number,
  resourceId: number,
  client: ApiClient = apiClient,
): Promise<ResourceSegmentGroupListResponse> => {
  const { data, error, response } = await client.GET(
    '/projects/{projectId}/resources/{resourceId}/segments/groups',
    {
      params: { path: { projectId, resourceId } },
    },
  )

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchSegmentGroupsFailed'), error, response)
  }

  return data
}

// Re-export 类型供 Store 使用
export type { ResourceSegmentGroup, ResourceSegmentGroupListResponse }
