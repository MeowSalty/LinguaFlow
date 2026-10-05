import { apiClient, type ApiPaths, type ApiSchemas } from './client-core'
import { captureSession, assertSessionCurrent } from './session-context'
import { storageRequestError, storageTransportFailure } from './storage-errors'
import { ApiError, getContentDispositionFilename, type DownloadFileResult } from './utils'
import {
  requireStorageId,
  requireStorageGeneration,
  requireIdempotencyKey,
} from '@/utils/storage-contract'
import { t } from '@/i18n'

export type StorageScope = { kind: 'user' } | { kind: 'org'; id: number } | { kind: 'site' }
export type StorageRequestOptions = { signal?: AbortSignal }
type Result<T> = { data?: T; response: Response; error?: unknown }
const pick = <T, K extends keyof T>(body: T, keys: readonly K[]): Pick<T, K> =>
  Object.fromEntries(
    keys.filter((key) => body[key] !== undefined).map((key) => [key, body[key]]),
  ) as Pick<T, K>

const read = async <T>(request: Promise<Result<T>>): Promise<T> => {
  const session = captureSession()
  try {
    const result = await request
    assertSessionCurrent(session)
    if (!result.response.ok || result.data === undefined)
      throw storageRequestError(result.response, result.error)
    return result.data
  } catch (error) {
    assertSessionCurrent(session)
    throw storageTransportFailure(error)
  }
}
const empty = async (request: Promise<Result<unknown>>): Promise<void> => {
  const session = captureSession()
  try {
    const result = await request
    assertSessionCurrent(session)
    if (!result.response.ok) throw storageRequestError(result.response, result.error)
  } catch (error) {
    assertSessionCurrent(session)
    throw storageTransportFailure(error)
  }
}
const projectPath = (projectId: number) => ({ projectId: requireStorageId(projectId) })
const resourcePath = (projectId: number, resourceId: number) => ({
  ...projectPath(projectId),
  resourceId: requireStorageId(resourceId),
})
const taskPath = (projectId: number, taskId: number) => ({
  ...projectPath(projectId),
  taskId: requireStorageId(taskId),
})
const artifactPath = (projectId: number, artifactId: number) => ({
  ...projectPath(projectId),
  artifactId: requireStorageId(artifactId),
})
const connectionPath = (connectionId: number) => ({ connectionId: requireStorageId(connectionId) })
const idempotencyHeader = (key: string) => ({ 'Idempotency-Key': requireIdempotencyKey(key) })

export const getStoragePolicy = (options?: StorageRequestOptions) =>
  read(apiClient.GET('/admin/storage/policy', options))

export const setStoragePolicy = (
  body: ApiSchemas['StoragePolicy'],
  options?: StorageRequestOptions,
) => {
  requireStorageGeneration(body.generation)
  requireStorageId(body.logical_limit_bytes)
  return read(
    apiClient.PUT('/admin/storage/policy', {
      ...options,
      body: pick(body, ['mode', 'default_choice', 'generation', 'logical_limit_bytes']),
    }),
  )
}

export const listStorageConnections = (scope: StorageScope, options?: StorageRequestOptions) => {
  switch (scope.kind) {
    case 'site':
      return read(apiClient.GET('/admin/storage/connections', options))
    case 'org':
      return read(
        apiClient.GET('/orgs/{orgId}/storage/connections', {
          ...options,
          params: { path: { orgId: requireStorageId(scope.id) } },
        }),
      )
    case 'user':
      return read(apiClient.GET('/storage/connections', options))
  }
}

export const createStorageConnection = (
  scope: Exclude<StorageScope, { kind: 'site' }>,
  body: ApiSchemas['StorageConnectionRequest'],
  options?: StorageRequestOptions,
) => {
  const config = pick(body, ['name', 'endpoint', 'region', 'path_style'])
  if (scope.kind === 'org') {
    const orgId = requireStorageId(scope.id)
    return read(
      apiClient.POST('/orgs/{orgId}/storage/connections', {
        ...options,
        params: { path: { orgId } },
        body: { ...config, scope: 'org', owner_id: orgId },
      }),
    )
  }
  // The personal route establishes the owner; never forward an arbitrary owner.
  return read(
    apiClient.POST('/storage/connections', { ...options, body: { ...config, scope: 'user' } }),
  )
}

export const setStorageConnectionState = (
  connectionId: number,
  body: ApiSchemas['StorageConnectionStateRequest'],
  options?: StorageRequestOptions,
) => {
  requireStorageGeneration(body.expected_generation)
  if (body.status !== 'enabled' && body.status !== 'disabled') throw storageRequestError()
  return read(
    apiClient.PATCH('/storage/connections/{connectionId}', {
      ...options,
      params: { path: connectionPath(connectionId) },
      body: pick(body, ['status', 'expected_generation']),
    }),
  )
}
export const listStorageSpaces = (connectionId: number, options?: StorageRequestOptions) =>
  read(
    apiClient.GET('/storage/connections/{connectionId}/spaces', {
      ...options,
      params: { path: connectionPath(connectionId) },
    }),
  )
export const createStorageSpace = (
  connectionId: number,
  body: ApiSchemas['StorageSpaceRequest'],
  options?: StorageRequestOptions,
) => {
  requireStorageId(body.capacity_bytes)
  return read(
    apiClient.POST('/storage/connections/{connectionId}/spaces', {
      ...options,
      params: { path: connectionPath(connectionId) },
      body: pick(body, ['name', 'bucket', 'prefix', 'capacity_bytes']),
    }),
  )
}
export const setStorageSpaceState = (
  spaceId: number,
  body: ApiSchemas['StorageSpaceStateRequest'],
  options?: StorageRequestOptions,
) => {
  requireStorageGeneration(body.expected_generation)
  if (!['active', 'read_only', 'disabled'].includes(body.status)) throw storageRequestError()
  return read(
    apiClient.PATCH('/storage/spaces/{spaceId}', {
      ...options,
      params: { path: { spaceId: requireStorageId(spaceId) } },
      body: pick(body, ['status', 'expected_generation']),
    }),
  )
}
export const authorizeStorage = (
  connectionId: number,
  body: ApiSchemas['StorageAuthorizationRequest'],
  options?: StorageRequestOptions,
) => {
  requireStorageGeneration(body.expected_management_generation)
  return read(
    apiClient.POST('/storage/connections/{connectionId}/authorize', {
      ...options,
      params: { path: connectionPath(connectionId) },
      body: pick(body, [
        'expected_management_generation',
        'access_key_id',
        'secret_access_key',
        'session_token',
        'expires_at',
        'write_check',
      ]),
    }),
  )
}
export const checkStorageConnection = (
  connectionId: number,
  body: { write_check: boolean; expected_generation: number },
  options?: StorageRequestOptions,
) => {
  requireStorageGeneration(body.expected_generation)
  return read(
    apiClient.POST('/storage/connections/{connectionId}/check', {
      ...options,
      params: { path: connectionPath(connectionId) },
      body: pick(body, ['write_check', 'expected_generation']),
    }),
  )
}
export const revokeStorageAuthorization = (
  connectionId: number,
  body: ApiSchemas['StorageRevokeRequest'],
  options?: StorageRequestOptions,
) => {
  requireStorageGeneration(body.expected_generation)
  return read(
    apiClient.POST('/storage/connections/{connectionId}/revoke', {
      ...options,
      params: { path: connectionPath(connectionId) },
      body: { expected_generation: body.expected_generation },
    }),
  )
}
export const getProjectStorage = (projectId: number, options?: StorageRequestOptions) =>
  read(
    apiClient.GET('/projects/{projectId}/storage', {
      ...options,
      params: { path: projectPath(projectId) },
    }),
  )
const validateBinding = (body: ApiSchemas['StorageBindingRequest']) => {
  requireStorageId(body.space_id)
  requireStorageGeneration(body.expected_generation)
}
export const bindProjectStorage = (
  projectId: number,
  body: ApiSchemas['StorageBindingRequest'],
  options?: StorageRequestOptions,
) => {
  validateBinding(body)
  return empty(
    apiClient.PUT('/projects/{projectId}/storage', {
      ...options,
      params: { path: projectPath(projectId) },
      body: pick(body, ['space_id', 'expected_generation']),
    }),
  )
}
export const migrateProjectStorage = (
  projectId: number,
  body: ApiSchemas['StorageMigrationRequest'],
  options?: StorageRequestOptions,
) => {
  validateBinding(body)
  requireIdempotencyKey(body.idempotency_key)
  return read(
    apiClient.POST('/projects/{projectId}/storage/migrations', {
      ...options,
      params: { path: projectPath(projectId) },
      body: pick(body, ['space_id', 'expected_generation', 'idempotency_key']),
    }),
  )
}
export const listStorageTasks = (projectId: number, options?: StorageRequestOptions) =>
  read(
    apiClient.GET('/projects/{projectId}/storage/tasks', {
      ...options,
      params: { path: projectPath(projectId) },
    }),
  )
export const createStorageIntent = (
  projectId: number,
  body: ApiSchemas['StorageIntent'],
  options?: StorageRequestOptions,
) => {
  requireStorageGeneration(body.storage_generation)
  requireStorageGeneration(body.size)
  requireIdempotencyKey(body.idempotency_key)
  const common = pick(body, ['kind', 'idempotency_key', 'size', 'storage_generation'])
  let intent: ApiSchemas['StorageIntent']
  switch (body.kind) {
    case 'upload':
      if (typeof body.path !== 'string' || !body.path.trim())
        throw storageRequestError({ status: 400 })
      intent = { ...common, kind: 'upload', path: body.path }
      break
    case 'source_update':
      intent = {
        ...common,
        kind: 'source_update',
        resource_id: requireStorageId(body.resource_id),
        source_generation: requireStorageGeneration(body.source_generation),
        translation_generation: requireStorageGeneration(body.translation_generation),
      }
      break
    case 'repair':
      intent = {
        ...common,
        kind: 'repair',
        resource_id: requireStorageId(body.resource_id),
        source_revision_id: requireStorageId(body.source_revision_id),
        location_generation: requireStorageGeneration(body.location_generation),
        target_space_id: requireStorageId(body.target_space_id),
      }
      break
    default:
      throw storageRequestError({ status: 400 })
  }
  return read(
    apiClient.POST('/projects/{projectId}/storage/tasks', {
      ...options,
      params: { path: projectPath(projectId) },
      body: intent,
    }),
  )
}
export const getStorageTask = (
  projectId: number,
  taskId: number,
  options?: StorageRequestOptions,
) =>
  read(
    apiClient.GET('/projects/{projectId}/storage/tasks/{taskId}', {
      ...options,
      params: { path: taskPath(projectId, taskId) },
    }),
  )

// OpenAPI represents binary bodies as string. The serializer supplies the actual
// Blob without JSON conversion; browsers own Content-Length and transfer framing.
export const receiveStorageContent = (
  projectId: number,
  taskId: number,
  file: Blob,
  options?: StorageRequestOptions,
) =>
  read(
    apiClient.PUT('/projects/{projectId}/storage/tasks/{taskId}/content', {
      ...options,
      params: { path: taskPath(projectId, taskId) },
      body: '',
      headers: { 'Content-Type': 'application/octet-stream' },
      bodySerializer: () => file,
    }),
  )
export const cancelStorageTask = (
  projectId: number,
  taskId: number,
  options?: StorageRequestOptions,
) =>
  read(
    apiClient.POST('/projects/{projectId}/storage/tasks/{taskId}/cancel', {
      ...options,
      params: { path: taskPath(projectId, taskId) },
    }),
  )
export const retryStorageTask = (
  projectId: number,
  taskId: number,
  options?: StorageRequestOptions,
) =>
  read(
    apiClient.POST('/projects/{projectId}/storage/tasks/{taskId}/retry', {
      ...options,
      params: { path: taskPath(projectId, taskId) },
    }),
  )
export const listSourceVersions = (
  projectId: number,
  resourceId: number,
  options?: StorageRequestOptions,
) =>
  read(
    apiClient.GET('/projects/{projectId}/resources/{resourceId}/versions', {
      ...options,
      params: { path: resourcePath(projectId, resourceId) },
    }),
  )
export const previewSourceUpdate = (
  projectId: number,
  resourceId: number,
  file: Blob,
  idempotencyKey: string,
  options?: StorageRequestOptions,
) =>
  read(
    apiClient.POST('/projects/{projectId}/resources/{resourceId}/source-preview', {
      ...options,
      params: {
        path: resourcePath(projectId, resourceId),
        header: idempotencyHeader(idempotencyKey),
      },
      headers: { 'Content-Type': 'application/octet-stream' },
      body: '',
      bodySerializer: () => file,
    }),
  )
export const commitSourceUpdate = (
  projectId: number,
  resourceId: number,
  body: ApiSchemas['SourceUpdateCommit'],
  options?: StorageRequestOptions,
) => {
  requireStorageId(body.task_id)
  requireStorageGeneration(body.expected_source_generation)
  requireStorageGeneration(body.expected_translation_generation)
  return read(
    apiClient.POST('/projects/{projectId}/resources/{resourceId}/source-commit', {
      ...options,
      params: { path: resourcePath(projectId, resourceId) },
      body: pick(body, [
        'task_id',
        'expected_source_generation',
        'expected_translation_generation',
      ]),
    }),
  )
}
export const listExportArtifacts = (
  projectId: number,
  resourceId: number,
  options?: StorageRequestOptions & { includeDeleted?: boolean },
) =>
  read(
    apiClient.GET('/projects/{projectId}/resources/{resourceId}/exports', {
      ...options,
      params: {
        path: resourcePath(projectId, resourceId),
        query: { include_deleted: options?.includeDeleted },
      },
    }),
  )
export const createExportArtifact = (
  projectId: number,
  resourceId: number,
  idempotencyKey: string,
  options?: StorageRequestOptions,
) =>
  read(
    apiClient.POST('/projects/{projectId}/resources/{resourceId}/exports', {
      ...options,
      params: {
        path: resourcePath(projectId, resourceId),
        header: idempotencyHeader(idempotencyKey),
      },
    }),
  )
export const rebuildExportArtifact = (
  projectId: number,
  artifactId: number,
  idempotencyKey: string,
  options?: StorageRequestOptions,
) =>
  read(
    apiClient.POST('/projects/{projectId}/exports/{artifactId}/rebuild', {
      ...options,
      params: {
        path: artifactPath(projectId, artifactId),
        header: idempotencyHeader(idempotencyKey),
      },
    }),
  )
export const deleteExportArtifact = (
  projectId: number,
  artifactId: number,
  options?: StorageRequestOptions,
) =>
  empty(
    apiClient.DELETE('/projects/{projectId}/exports/{artifactId}', {
      ...options,
      params: { path: artifactPath(projectId, artifactId) },
    }),
  )
export const downloadExportArtifact = async (
  projectId: number,
  artifactId: number,
  options?: StorageRequestOptions,
): Promise<DownloadFileResult> => {
  const session = captureSession()
  const result = await apiClient
    .GET('/projects/{projectId}/exports/{artifactId}/download', {
      ...options,
      params: { path: artifactPath(projectId, artifactId) },
      parseAs: 'blob',
    })
    .catch((error: unknown) => {
      assertSessionCurrent(session)
      throw storageTransportFailure(error)
    })
  assertSessionCurrent(session)
  const download = await storageDownloadResult(result)
  assertSessionCurrent(session)
  return download
}
export const storageDownloadResult = async (
  result: {
    data?: Blob
    response: Response
    error?: unknown
  },
  legacyJson = false,
): Promise<DownloadFileResult> => {
  if (!result.response.ok) {
    const body =
      result.error ??
      (await result.response
        .clone()
        .json()
        .catch(() => undefined))
    throw storageRequestError(result.response, body)
  }
  const contentType = result.response.headers.get('content-type') ?? ''
  if (
    !result.data ||
    (legacyJson
      ? !/^application\/json(?:;|$)/i.test(contentType) ||
        !/attachment/i.test(result.response.headers.get('content-disposition') ?? '')
      : /application\/(?:problem\+)?json/i.test(contentType))
  )
    throw new ApiError(t('storageErrors.invalidDownload'))
  let filename: string | undefined
  try {
    filename = getContentDispositionFilename(result.response)
  } catch {
    /* Invalid filename: use caller's fallback. */
  }
  return { blob: result.data, filename }
}

export const getStorageOptions = (
  scope: Exclude<StorageScope, { kind: 'site' }>,
  options?: StorageRequestOptions,
) =>
  read(
    apiClient.GET('/storage/options', {
      ...options,
      params: {
        query:
          scope.kind === 'org'
            ? { scope: 'org', organization_id: requireStorageId(scope.id) }
            : { scope: 'user' },
      },
    }),
  )

export type ProjectStoragePurpose =
  | { purpose: 'bind' | 'migrate' }
  | { purpose: 'repair'; source_revision_id: number }
export const getProjectStorageOptions = (
  projectId: number,
  query: ProjectStoragePurpose,
  options?: StorageRequestOptions,
) => {
  const params =
    query.purpose === 'repair'
      ? { purpose: query.purpose, source_revision_id: requireStorageId(query.source_revision_id) }
      : { purpose: query.purpose }
  return read(
    apiClient.GET('/projects/{projectId}/storage/options', {
      ...options,
      params: { path: projectPath(projectId), query: params },
    }),
  )
}
export const listStorageChecks = (
  connectionId: number,
  query: { cursor?: number; limit?: number } = {},
  options?: StorageRequestOptions,
) =>
  read(
    apiClient.GET('/storage/connections/{connectionId}/checks', {
      ...options,
      params: { path: connectionPath(connectionId), query },
    }),
  )
export const getStorageCheck = (
  connectionId: number,
  checkId: number,
  options?: StorageRequestOptions,
) =>
  read(
    apiClient.GET('/storage/connections/{connectionId}/checks/{checkId}', {
      ...options,
      params: { path: { ...connectionPath(connectionId), checkId: requireStorageId(checkId) } },
    }),
  )

export const downloadLegacySourceSnapshot = async (
  projectId: number,
  taskId: number,
  options?: StorageRequestOptions,
): Promise<DownloadFileResult> => {
  const session = captureSession()
  try {
    const result = await apiClient.GET(
      '/projects/{projectId}/storage/tasks/{taskId}/legacy-snapshot',
      {
        ...options,
        params: { path: taskPath(projectId, taskId) },
        parseAs: 'blob',
      },
    )
    const download = await storageDownloadResult(result, true)
    assertSessionCurrent(session)
    return download
  } catch (error) {
    assertSessionCurrent(session)
    throw storageTransportFailure(error)
  }
}
export type StorageDiagnosticsQuery = NonNullable<
  ApiPaths['/admin/storage/diagnostics']['get']['parameters']['query']
>
export const getStorageDiagnostics = (
  query: StorageDiagnosticsQuery = {},
  options?: StorageRequestOptions,
) => read(apiClient.GET('/admin/storage/diagnostics', { ...options, params: { query } }))
