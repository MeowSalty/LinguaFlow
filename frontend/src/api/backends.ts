import { apiClient, type ApiClient, type ApiSchemas } from './client-core'
import { safeCredentialError } from './credential-errors'
import { backendMetadata } from '@/utils/backend-form'

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
export async function fetchBackends(
  client: ApiClient = apiClient,
  orgId?: number,
  signal?: AbortSignal,
): Promise<ApiSchemas['BackendListResponse']> {
  const data = await read(() =>
    orgId === undefined
      ? client.GET('/backends', { signal })
      : client.GET('/orgs/{orgId}/backends', { params: { path: { orgId } }, signal }),
  )
  return { items: data.items.map(backendMetadata) }
}
export const listBackendModels = (
  payload: ApiSchemas['ListBackendModelsRequest'],
  client: ApiClient = apiClient,
  signal?: AbortSignal,
): Promise<ApiSchemas['BackendModelListResponse']> =>
  read(() => client.POST('/backends/models', { body: payload, signal }))
export async function createBackend(
  payload: ApiSchemas['CreateBackendRequest'],
  client: ApiClient = apiClient,
  orgId?: number,
): Promise<ApiSchemas['Backend']> {
  const data = await read(() =>
    orgId === undefined
      ? client.POST('/backends', { body: payload })
      : client.POST('/orgs/{orgId}/backends', { params: { path: { orgId } }, body: payload }),
  )
  return backendMetadata(data)
}
export async function updateBackend(
  backendId: number,
  payload: ApiSchemas['UpdateBackendRequest'],
  client: ApiClient = apiClient,
  orgId?: number,
): Promise<ApiSchemas['Backend']> {
  const data = await read(() =>
    orgId === undefined
      ? client.PUT('/backends/{backendId}', { params: { path: { backendId } }, body: payload })
      : client.PUT('/orgs/{orgId}/backends/{backendId}', {
          params: { path: { orgId, backendId } },
          body: payload,
        }),
  )
  return backendMetadata(data)
}
export async function deleteBackend(
  backendId: number,
  client: ApiClient = apiClient,
  orgId?: number,
): Promise<void> {
  try {
    const { error, response } =
      orgId === undefined
        ? await client.DELETE('/backends/{backendId}', { params: { path: { backendId } } })
        : await client.DELETE('/orgs/{orgId}/backends/{backendId}', {
            params: { path: { orgId, backendId } },
          })
    if (!response.ok) throw safeCredentialError(error, response)
  } catch (cause) {
    throw safeCredentialError(cause)
  }
}
