import { t } from '@/i18n'

import type { ApiClient, ApiSchemas } from './client'
import { apiClient } from './client'
import { buildRequestFailureError } from './utils'

type Template = ApiSchemas['Template']
type CreateTemplateRequest = ApiSchemas['CreateTemplateRequest']
type UpdateTemplateRequest = ApiSchemas['UpdateTemplateRequest']
type CopyTemplateRequest = ApiSchemas['CopyTemplateRequest']

export const fetchTemplates = async (
  client: ApiClient = apiClient,
): Promise<ApiSchemas['TemplateListResponse']> => {
  const { data, error, response } = await client.GET('/templates')

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchTemplatesFailed'), error, response)
  }

  return data
}

export const createTemplate = async (
  payload: CreateTemplateRequest,
  client: ApiClient = apiClient,
): Promise<Template> => {
  const { data, error, response } = await client.POST('/templates', {
    body: payload,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.createTemplateFailed'), error, response)
  }

  return data
}

export const getTemplate = async (
  templateId: number,
  client: ApiClient = apiClient,
): Promise<Template> => {
  const { data, error, response } = await client.GET('/templates/{templateId}', {
    params: { path: { templateId } },
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchTemplateFailed'), error, response)
  }

  return data
}

export const updateTemplate = async (
  templateId: number,
  payload: UpdateTemplateRequest,
  client: ApiClient = apiClient,
): Promise<Template> => {
  const { data, error, response } = await client.PUT('/templates/{templateId}', {
    params: { path: { templateId } },
    body: payload,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.updateTemplateFailed'), error, response)
  }

  return data
}

export const deleteTemplate = async (
  templateId: number,
  client: ApiClient = apiClient,
): Promise<void> => {
  const { error, response } = await client.DELETE('/templates/{templateId}', {
    params: { path: { templateId } },
  })

  if (response && !response.ok) {
    throw buildRequestFailureError(t('api.errors.deleteTemplateFailed'), error, response)
  }
}

export const copyTemplate = async (
  templateId: number,
  payload?: CopyTemplateRequest,
  client: ApiClient = apiClient,
): Promise<Template> => {
  const { data, error, response } = await client.POST('/templates/{templateId}/copy', {
    params: { path: { templateId } },
    body: payload ?? {},
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.copyTemplateFailed'), error, response)
  }

  return data
}

// ─── 组织模板 CRUD（预埋，本轮不实现 UI） ───────────────────

export const fetchOrgTemplates = async (
  orgId: number,
  client: ApiClient = apiClient,
): Promise<ApiSchemas['TemplateListResponse']> => {
  const { data, error, response } = await client.GET('/orgs/{orgId}/templates', {
    params: { path: { orgId } },
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchTemplatesFailed'), error, response)
  }

  return data
}

export const createOrgTemplate = async (
  orgId: number,
  payload: CreateTemplateRequest,
  client: ApiClient = apiClient,
): Promise<Template> => {
  const { data, error, response } = await client.POST('/orgs/{orgId}/templates', {
    params: { path: { orgId } },
    body: payload,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.createTemplateFailed'), error, response)
  }

  return data
}

export const updateOrgTemplate = async (
  orgId: number,
  templateId: number,
  payload: UpdateTemplateRequest,
  client: ApiClient = apiClient,
): Promise<Template> => {
  const { data, error, response } = await client.PUT('/orgs/{orgId}/templates/{templateId}', {
    params: { path: { orgId, templateId } },
    body: payload,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.updateTemplateFailed'), error, response)
  }

  return data
}

export const deleteOrgTemplate = async (
  orgId: number,
  templateId: number,
  client: ApiClient = apiClient,
): Promise<void> => {
  const { error, response } = await client.DELETE('/orgs/{orgId}/templates/{templateId}', {
    params: { path: { orgId, templateId } },
  })

  if (response && !response.ok) {
    throw buildRequestFailureError(t('api.errors.deleteTemplateFailed'), error, response)
  }
}
