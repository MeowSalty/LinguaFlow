import { t } from '@/i18n'

import type { ApiClient, ApiSchemas } from './client'
import { apiClient } from './client'
import { buildRequestFailureError } from './utils'

type GlossaryEntry = ApiSchemas['GlossaryEntry']
type CreateGlossaryEntryRequest = ApiSchemas['CreateGlossaryEntryRequest']
type UpdateGlossaryEntryRequest = ApiSchemas['UpdateGlossaryEntryRequest']

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
): Promise<GlossaryEntry> => {
  const { data, error, response } = await client.PUT(
    '/projects/{projectId}/glossary/{entryId}',
    {
      params: { path: { projectId, entryId } },
      body: payload,
    },
  )

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
  const { error, response } = await client.DELETE(
    '/projects/{projectId}/glossary/{entryId}',
    {
      params: { path: { projectId, entryId } },
    },
  )

  if (response && !response.ok) {
    throw buildRequestFailureError(t('api.errors.deleteGlossaryEntryFailed'), error, response)
  }
}

export const importGlossaryCSV = async (
  projectId: number,
  file: File,
  client: ApiClient = apiClient,
): Promise<ApiSchemas['GlossaryImportResult']> => {
  const { data, error, response } = await client.POST(
    '/projects/{projectId}/glossary/import',
    {
      params: { path: { projectId } },
      body: { file },
      bodySerializer: () => {
        const formData = new FormData()
        formData.append('file', file)
        return formData
      },
    },
  )

  if (!data) {
    throw buildRequestFailureError(t('api.errors.importGlossaryFailed'), error, response)
  }

  return data
}

export const exportGlossaryCSV = async (
  projectId: number,
  client: ApiClient = apiClient,
): Promise<Blob> => {
  const { data, error, response } = await client.GET(
    '/projects/{projectId}/glossary/export',
    {
      params: { path: { projectId } },
      parseAs: 'blob',
    },
  )

  if (!data) {
    throw buildRequestFailureError(t('api.errors.exportGlossaryFailed'), error, response)
  }

  return data as unknown as Blob
}
