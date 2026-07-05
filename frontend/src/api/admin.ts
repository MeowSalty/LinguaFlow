import { t } from '@/i18n'

import type { ApiClient, ApiSchemas } from './client'
import { apiClient } from './client'
import { buildRequestFailureError } from './utils'

export const fetchAdminStats = async (
  client: ApiClient = apiClient,
): Promise<ApiSchemas['SystemStats']> => {
  const { data, error, response } = await client.GET('/admin/stats')

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchAdminStatsFailed'), error, response)
  }

  return data
}

export const fetchAdminUsers = async (
  params?: {
    search?: string
    role?: string
    active?: boolean
    cursor?: string
    limit?: number
  },
  client: ApiClient = apiClient,
): Promise<ApiSchemas['AdminUserListResponse']> => {
  const { data, error, response } = await client.GET('/admin/users', {
    params: { query: params },
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchAdminUsersFailed'), error, response)
  }

  return data
}

export const fetchAdminUser = async (
  userId: number,
  client: ApiClient = apiClient,
): Promise<ApiSchemas['User']> => {
  const { data, error, response } = await client.GET('/admin/users/{userId}', {
    params: { path: { userId } },
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchAdminUserFailed'), error, response)
  }

  return data
}

export const createAdminUser = async (
  body: ApiSchemas['AdminCreateUserRequest'],
  client: ApiClient = apiClient,
): Promise<ApiSchemas['User']> => {
  const { data, error, response } = await client.POST('/admin/users', { body })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.createAdminUserFailed'), error, response)
  }

  return data
}

export const updateAdminUser = async (
  userId: number,
  body: ApiSchemas['AdminUpdateUserRequest'],
  client: ApiClient = apiClient,
): Promise<ApiSchemas['User']> => {
  const { data, error, response } = await client.PATCH('/admin/users/{userId}', {
    params: { path: { userId } },
    body,
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.updateAdminUserFailed'), error, response)
  }

  return data
}

export const disableAdminUser = async (
  userId: number,
  client: ApiClient = apiClient,
): Promise<void> => {
  const { error, response } = await client.DELETE('/admin/users/{userId}', {
    params: { path: { userId } },
  })

  if (error) {
    throw buildRequestFailureError(t('api.errors.disableAdminUserFailed'), error, response)
  }
}

export const resetAdminUserPassword = async (
  userId: number,
  body: ApiSchemas['AdminResetPasswordRequest'],
  client: ApiClient = apiClient,
): Promise<void> => {
  const { error, response } = await client.PUT('/admin/users/{userId}/password', {
    params: { path: { userId } },
    body,
  })

  if (error) {
    throw buildRequestFailureError(t('api.errors.resetAdminUserPasswordFailed'), error, response)
  }
}

export const fetchAdminAuditLogs = async (
  params?: { cursor?: string; limit?: number },
  client: ApiClient = apiClient,
): Promise<ApiSchemas['AdminAuditLogListResponse']> => {
  const { data, error, response } = await client.GET('/admin/audit-logs', {
    params: { query: params },
  })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchAdminAuditLogsFailed'), error, response)
  }

  return data
}

export const fetchAdminSettings = async (
  client: ApiClient = apiClient,
): Promise<ApiSchemas['SystemSettingsResponse']> => {
  const { data, error, response } = await client.GET('/admin/settings')

  if (!data) {
    throw buildRequestFailureError(t('api.errors.fetchAdminSettingsFailed'), error, response)
  }

  return data
}

export const updateAdminSettings = async (
  body: ApiSchemas['UpdateSystemSettingsRequest'],
  client: ApiClient = apiClient,
): Promise<ApiSchemas['SystemSettingsResponse']> => {
  const { data, error, response } = await client.PATCH('/admin/settings', { body })

  if (!data) {
    throw buildRequestFailureError(t('api.errors.updateAdminSettingsFailed'), error, response)
  }

  return data
}
