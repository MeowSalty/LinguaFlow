import { t } from '@/i18n'
import { apiClient, type ApiClient, type ApiPaths, type ApiSchemas } from './client-core'
import { isAuthAttributeCatalog, isLocalAccountCapabilities } from './auth-contract'
import { AuthConnectionError, authConfigurationResult } from './auth-connections'

type LocalPaths = ApiPaths['/admin/local-account']
export type LocalAccount = LocalPaths['get']['responses'][200]['content']['application/json']
export type LocalAccountPolicy = ApiSchemas['LocalAccountPolicyInput']
export type LocalAccountUpdate = LocalPaths['patch']['requestBody']['content']['application/json']
export type LocalAccountPreviewRequest = ApiSchemas['LocalAccountPreviewRequest']
export type LocalAccountPreview =
  ApiPaths['/admin/local-account/preview']['post']['responses'][200]['content']['application/json']
export type LocalAccountImpactRequest =
  ApiPaths['/admin/local-account/impact']['post']['requestBody']['content']['application/json']
export type LocalAccountImpact = ApiSchemas['AuthConnectionImpactResult']

const object = (value: unknown): value is Record<string, unknown> =>
  !!value && typeof value === 'object' && !Array.isArray(value)

export function isLocalAccount(value: unknown): value is LocalAccount {
  if (!object(value) || !object(value.policy) || !object(value.policy.signup)) return false
  const policy = value.policy
  const signup = policy.signup as Record<string, unknown>
  return (
    Number.isSafeInteger(value.revision) &&
    Number(value.revision) >= 1 &&
    [
      'password_login_enabled',
      'username_login_enabled',
      'email_login_enabled',
      'password_enrollment_enabled',
    ].every((key) => typeof policy[key] === 'boolean') &&
    typeof signup.enabled === 'boolean' &&
    ['optional', 'required'].includes(String(signup.email_requirement)) &&
    object(signup.rules) &&
    (signup.rules.mode === 'unrestricted' ||
      (signup.rules.mode === 'conditions' &&
        (signup.rules.match === 'all' || signup.rules.match === 'any') &&
        Array.isArray(signup.rules.conditions))) &&
    isLocalAccountCapabilities(value.capabilities) &&
    Array.isArray(value.issues)
  )
}

function localResult(value: LocalAccount): LocalAccount {
  if (!isLocalAccount(value))
    throw new AuthConnectionError({ detail: t('authAdmin.requestFailed') })
  return value
}

export async function fetchLocalAccount(client: ApiClient = apiClient): Promise<LocalAccount> {
  const { data, error, response } = await client.GET('/admin/local-account')
  return localResult(authConfigurationResult(data, error, response))
}

/** The fixed local resource always writes the entire policy. */
export async function updateLocalAccount(
  body: LocalAccountUpdate,
  client: ApiClient = apiClient,
): Promise<LocalAccount> {
  const { data, error, response } = await client.PATCH('/admin/local-account', { body })
  return localResult(authConfigurationResult(data, error, response))
}

export async function fetchLocalAccountAttributes(revision: number, client: ApiClient = apiClient) {
  const { data, error, response } = await client.GET('/admin/local-account/attributes', {
    params: { query: { expected_revision: revision } },
  })
  const catalog = authConfigurationResult(data, error, response)
  if (!isAuthAttributeCatalog(catalog, 'signup', revision))
    throw new AuthConnectionError({ detail: t('authAdmin.catalogFailed') }, response.status)
  return catalog
}

export async function previewLocalAccount(
  body: LocalAccountPreviewRequest,
  client: ApiClient = apiClient,
): Promise<LocalAccountPreview> {
  const { data, error, response } = await client.POST('/admin/local-account/preview', { body })
  return authConfigurationResult(data, error, response)
}

export async function fetchLocalAccountImpact(
  body: LocalAccountImpactRequest,
  client: ApiClient = apiClient,
): Promise<LocalAccountImpact> {
  const { data, error, response } = await client.POST('/admin/local-account/impact', { body })
  return authConfigurationResult(data, error, response)
}
