import { apiClient, type ApiClient, type ApiSchemas, type ApiPaths } from './client-core'
import { safeCredentialError } from './credential-errors'

export type Credential = ApiSchemas['Credential']
export type CredentialVersion = ApiSchemas['CredentialVersion']
export type CollectResult =
  ApiPaths['/credentials/{credentialId}/collect']['post']['responses'][200]['content']['application/json']

async function read<T>(
  action: () => Promise<{ data?: T; error?: unknown; response: Response }>,
): Promise<T> {
  try {
    const { data, error, response } = await action()
    if (!response.ok || data === undefined) throw safeCredentialError(error, response)
    return data
  } catch (cause) {
    throw safeCredentialError(cause)
  }
}

// Reconstruct metadata explicitly: even an unexpected server field cannot enter the cache.
export const credentialMetadata = (value: Credential): Credential => ({
  id: value.id,
  scope: value.scope,
  owner_id: value.owner_id,
  provider: value.provider,
  endpoint: value.endpoint,
  current_version: value.current_version,
})
export const versionMetadata = (value: CredentialVersion): CredentialVersion => ({
  version: value.version,
  revoked: value.revoked,
  created_at: value.created_at,
})

export async function fetchCredentials(
  orgId: number | null,
  signal?: AbortSignal,
  client: ApiClient = apiClient,
): Promise<ApiSchemas['CredentialList']> {
  const result = await read(() =>
    orgId === null
      ? client.GET('/credentials', { signal })
      : client.GET('/orgs/{orgId}/credentials', { params: { path: { orgId } }, signal }),
  )
  return { items: result.items.map(credentialMetadata) }
}
export async function createCredential(
  body: ApiSchemas['CreateCredentialRequest'],
  orgId: number | null,
  client: ApiClient = apiClient,
): Promise<Credential> {
  const result = await read(() =>
    orgId === null
      ? client.POST('/credentials', { body })
      : client.POST('/orgs/{orgId}/credentials', { params: { path: { orgId } }, body }),
  )
  return credentialMetadata(result)
}
export async function fetchCredentialVersions(
  credentialId: number,
  signal?: AbortSignal,
  client: ApiClient = apiClient,
): Promise<ApiSchemas['CredentialVersionList']> {
  const result = await read(() =>
    client.GET('/credentials/{credentialId}/versions', {
      params: { path: { credentialId } },
      signal,
    }),
  )
  return { items: result.items.map(versionMetadata) }
}
export const rotateCredential = (
  credentialId: number,
  body: ApiSchemas['RotateCredentialRequest'],
  client: ApiClient = apiClient,
): Promise<ApiSchemas['CredentialBinding']> =>
  read(() =>
    client.POST('/credentials/{credentialId}/versions', {
      params: { path: { credentialId } },
      body,
    }),
  )

export async function revokeCredentialVersion(
  credentialId: number,
  version: number,
  client: ApiClient = apiClient,
): Promise<void> {
  try {
    const { error, response } = await client.POST(
      '/credentials/{credentialId}/versions/{version}/revoke',
      { params: { path: { credentialId, version } } },
    )
    if (!response.ok) throw safeCredentialError(error, response)
  } catch (cause) {
    throw safeCredentialError(cause)
  }
}
export const collectCredentialVersions = (
  credentialId: number,
  client: ApiClient = apiClient,
): Promise<CollectResult> =>
  read(() =>
    client.POST('/credentials/{credentialId}/collect', { params: { path: { credentialId } } }),
  )
