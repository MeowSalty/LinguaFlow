import { t } from '@/i18n'

import type { ApiClient, ApiSchemas } from './client-core'
import { apiClient } from './client-core'
import { buildRequestFailureError } from './utils'

export type LoginMethods = ApiSchemas['LoginMethodsResponse']
export type LoginMethod = ApiSchemas['LoginMethod']
export type ReauthenticationAction = ApiSchemas['ReauthenticationRequest']['action']

/**
 * 读取当前账号的密码、邮箱、已绑定方式与可绑定连接及其能力位。
 *
 * 页面按 `can_set` / `can_delete` / `can_unlink` / `can_reauthenticate` / `can_link`
 * 决定按钮可用性，不自行推断后端策略。
 */
export const fetchLoginMethods = async (client: ApiClient = apiClient): Promise<LoginMethods> => {
  const { data, error, response } = await client.GET('/users/me/login-methods')

  if (!response.ok || !data) {
    throw buildRequestFailureError(t('accountAuth.security.loadFailed'), error, response)
  }

  return data
}

/** 首次设置密码（POST）。需要来自其他登录方式的重新认证证明。 */
export const setInitialPassword = async (
  payload: ApiSchemas['InitialPasswordRequest'],
  client: ApiClient = apiClient,
): Promise<void> => {
  const { error, response } = await client.POST('/users/me/password', {
    body: payload,
  })

  if (!response.ok) {
    throw buildRequestFailureError(t('accountAuth.security.setForm.failed'), error, response)
  }
}

/** 删除密码（DELETE）。证明不能来自待删除的密码本身。 */
export const deleteCurrentUserPassword = async (
  proof: string,
  client: ApiClient = apiClient,
): Promise<void> => {
  const { error, response } = await client.DELETE('/users/me/password', {
    body: { reauthentication_proof: proof },
  })

  if (!response.ok) {
    throw buildRequestFailureError(
      t('accountAuth.security.errors.removePasswordFailed'),
      error,
      response,
    )
  }
}

/** 解绑外部登录方式（DELETE）。证明不能来自目标绑定自身。 */
export const unlinkLoginBinding = async (
  bindingId: number,
  proof: string,
  client: ApiClient = apiClient,
): Promise<void> => {
  const { error, response } = await client.DELETE('/users/me/login-bindings/{bindingId}', {
    params: { path: { bindingId } },
    body: { reauthentication_proof: proof },
  })

  if (!response.ok) {
    throw buildRequestFailureError(t('accountAuth.security.errors.unlinkFailed'), error, response)
  }
}

/**
 * 使用当前密码换取一次性的重新认证证明。
 *
 * 密码只在本次调用的请求体中传递，不写入任何持久化存储。
 * 需要外部渠道换取的证明请改用 `startExternalReauthentication`。
 */
export const reauthenticateWithPassword = async (
  payload: { password: string; action: ReauthenticationAction; targetId: string },
  client: ApiClient = apiClient,
): Promise<ApiSchemas['ReauthenticationProofResponse']> => {
  const { data, error, response } = await client.POST('/auth/reauthenticate', {
    body: {
      method: 'password',
      password: payload.password,
      action: payload.action,
      target_id: payload.targetId,
    },
  })

  if (!response.ok || !data || !('reauthentication_proof' in data)) {
    throw buildRequestFailureError(t('accountAuth.security.reauth.failed'), error, response)
  }

  return data
}
