import { t } from '@/i18n'

import type { ApiClient, ApiSchemas } from './client'
import { apiClient } from './client'
import { buildRequestFailureError } from './utils'

type TranslationProfile = ApiSchemas['TranslationProfile']
type CreateTranslationProfileRequest = ApiSchemas['CreateTranslationProfileRequest']
type UpdateTranslationProfileRequest = ApiSchemas['UpdateTranslationProfileRequest']

export const fetchTranslationProfiles = async (
  client: ApiClient = apiClient,
): Promise<ApiSchemas['TranslationProfileListResponse']> => {
  const { data, error, response } = await client.GET('/translation-profiles')

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchTranslationProfilesFailed'), error, response)
  }

  return data
}

export const createTranslationProfile = async (
  payload: CreateTranslationProfileRequest,
  client: ApiClient = apiClient,
): Promise<TranslationProfile> => {
  const { data, error, response } = await client.POST('/translation-profiles', {
    body: payload,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.createTranslationProfileFailed'), error, response)
  }

  return data
}

export const getTranslationProfile = async (
  translationProfileId: number,
  client: ApiClient = apiClient,
): Promise<TranslationProfile> => {
  const { data, error, response } = await client.GET(
    '/translation-profiles/{translationProfileId}',
    {
      params: { path: { translationProfileId } },
    },
  )

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchTranslationProfileFailed'), error, response)
  }

  return data
}

export const updateTranslationProfile = async (
  translationProfileId: number,
  payload: UpdateTranslationProfileRequest,
  client: ApiClient = apiClient,
): Promise<TranslationProfile> => {
  const { data, error, response } = await client.PUT(
    '/translation-profiles/{translationProfileId}',
    {
      params: { path: { translationProfileId } },
      body: payload,
    },
  )

  if (!data) {
    throw buildRequestFailureError(t('api.errors.updateTranslationProfileFailed'), error, response)
  }

  return data
}

export const deleteTranslationProfile = async (
  translationProfileId: number,
  client: ApiClient = apiClient,
): Promise<void> => {
  const { error, response } = await client.DELETE('/translation-profiles/{translationProfileId}', {
    params: { path: { translationProfileId } },
  })

  if (response && !response.ok) {
    throw buildRequestFailureError(t('api.errors.deleteTranslationProfileFailed'), error, response)
  }
}
