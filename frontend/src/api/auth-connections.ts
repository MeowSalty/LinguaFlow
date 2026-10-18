import { t } from '@/i18n'
import { apiClient, type ApiClient, type ApiSchemas } from './client-core'
import { ApiError } from './utils'

export type AuthConnection = ApiSchemas['AuthConnectionRecord']
export type AuthConnectionSnapshot = ApiSchemas['AuthConnectionSnapshot']
export type AuthConnectionPolicy = ApiSchemas['AuthConnectionPolicy']
export type AuthConnectionImpact = ApiSchemas['AuthConnectionImpact']
export type AuthConnectionCheck = ApiSchemas['AuthConnectionCheck']
export type AuthAttributeField = ApiSchemas['AuthAttributeField']
export type AuthRuleSet = ApiSchemas['AuthRuleSet']
export type AuthConnectionChanges = ApiSchemas['AuthConnectionChanges']
export type AuthConnectionCandidate = ApiSchemas['AuthConnectionCandidateRequest']
export type AuthConnectionAction = ApiSchemas['AuthConnectionActionRequest']
export type AuthConnectionProblem = Omit<
  Partial<ApiSchemas['AuthConnectionErrorProblem']>,
  'status'
> & {
  status?: number
  remediation?: string
}

/** Keep the complete problem, including safety impact, instead of flattening a 409. */
export class AuthConnectionError extends ApiError {
  declare readonly problem: AuthConnectionProblem | undefined
  constructor(problem: AuthConnectionProblem | undefined, status?: number) {
    super(problem?.detail || problem?.title || t('authAdmin.requestFailed'), status, problem)
    this.name = 'AuthConnectionError'
  }
  get code(): ApiSchemas['AuthConnectionErrorCode'] | undefined {
    const prefix = 'urn:linguaflow:auth-connection-'
    return this.problem?.type?.startsWith(prefix)
      ? (this.problem.type.slice(prefix.length) as ApiSchemas['AuthConnectionErrorCode'])
      : undefined
  }
}

function result<T>(data: T | undefined, error: unknown, response: Response): T {
  if (error || data === undefined || !response.ok) {
    throw new AuthConnectionError(
      error && typeof error === 'object' ? (error as AuthConnectionProblem) : undefined,
      response.status,
    )
  }
  return data
}

export async function fetchAuthConnections(client: ApiClient = apiClient) {
  const { data, error, response } = await client.GET('/admin/auth-connections')
  return result(data, error, response).items
}

export async function uploadAuthConnectionLogo(
  connectionId: string,
  revision: number,
  file: File,
  client: ApiClient = apiClient,
) {
  if (!['image/png', 'image/webp'].includes(file.type) || file.size === 0 || file.size > 256 * 1024)
    throw new Error(t('authAdmin.logoInvalid'))
  const { data, error, response } = await client.POST(
    '/admin/auth-connections/{connectionId}/logo',
    {
      params: { path: { connectionId }, query: { expected_revision: revision } },
      headers: { 'Content-Type': file.type },
      // OpenAPI's binary schema is generated as string; the transport sends the original bytes.
      body: file as unknown as string,
      bodySerializer: () => file,
    },
  )
  return result(data, error, response)
}

export async function fetchAuthConnectionLogo(
  connectionId: string,
  client: ApiClient = apiClient,
): Promise<Blob> {
  const { data, error, response } = await client.GET(
    '/admin/auth-connections/{connectionId}/logo',
    {
      params: { path: { connectionId } },
      parseAs: 'blob',
    },
  )
  return result(data, error, response)
}

export async function fetchAuthConnection(connectionId: string, client: ApiClient = apiClient) {
  const { data, error, response } = await client.GET('/admin/auth-connections/{connectionId}', {
    params: { path: { connectionId } },
  })
  return result(data, error, response)
}

export async function createAuthConnection(
  body: ApiSchemas['CreateAuthConnectionRequest'],
  client: ApiClient = apiClient,
) {
  const { data, error, response } = await client.POST('/admin/auth-connections', { body })
  return result(data, error, response)
}

export async function updateAuthConnection(
  connectionId: string,
  body: ApiSchemas['UpdateAuthConnectionRequest'],
  client: ApiClient = apiClient,
) {
  const { data, error, response } = await client.PATCH('/admin/auth-connections/{connectionId}', {
    params: { path: { connectionId } },
    body,
  })
  return result(data, error, response)
}

export async function fetchAuthConnectionAttributes(
  connectionId: string,
  revision: number,
  client: ApiClient = apiClient,
) {
  const { data, error, response } = await client.GET(
    '/admin/auth-connections/{connectionId}/attributes',
    {
      params: { path: { connectionId }, query: { expected_revision: revision } },
    },
  )
  return result(data, error, response)
}

export async function previewAuthConnection(
  connectionId: string,
  body: ApiSchemas['AuthRulesPreviewRequest'],
  client: ApiClient = apiClient,
) {
  const { data, error, response } = await client.POST(
    '/admin/auth-connections/{connectionId}/preview',
    {
      params: { path: { connectionId } },
      body,
    },
  )
  return result(data, error, response)
}

export async function previewAuthConnectionAttributes(
  connectionId: string,
  body: AuthConnectionCandidate,
  client: ApiClient = apiClient,
) {
  const { data, error, response } = await client.POST(
    '/admin/auth-connections/{connectionId}/attributes',
    { params: { path: { connectionId } }, body },
  )
  return result(data, error, response)
}

export async function checkAuthConnection(
  connectionId: string,
  body: AuthConnectionCandidate,
  client: ApiClient = apiClient,
) {
  const { data, error, response } = await client.POST(
    '/admin/auth-connections/{connectionId}/check',
    {
      params: { path: { connectionId } },
      body,
    },
  )
  return result(data, error, response)
}

export async function fetchAuthConnectionImpact(
  connectionId: string,
  body: AuthConnectionCandidate,
  client: ApiClient = apiClient,
) {
  const { data, error, response } = await client.POST(
    '/admin/auth-connections/{connectionId}/impact',
    {
      params: { path: { connectionId } },
      body,
    },
  )
  return result(data, error, response)
}

export async function configureAuthConnection(
  connectionId: string,
  body: AuthConnectionAction,
  client: ApiClient = apiClient,
) {
  const { data, error, response } = await client.POST(
    '/admin/auth-connections/{connectionId}/configure',
    {
      params: { path: { connectionId } },
      body,
    },
  )
  return result(data, error, response)
}

export async function enableAuthConnection(
  connectionId: string,
  body: AuthConnectionAction,
  client: ApiClient = apiClient,
) {
  const { data, error, response } = await client.POST(
    '/admin/auth-connections/{connectionId}/enable',
    { params: { path: { connectionId } }, body },
  )
  return result(data, error, response)
}

export async function disableAuthConnection(
  connectionId: string,
  body: AuthConnectionAction,
  client: ApiClient = apiClient,
) {
  const { data, error, response } = await client.POST(
    '/admin/auth-connections/{connectionId}/disable',
    { params: { path: { connectionId } }, body },
  )
  return result(data, error, response)
}

export async function archiveAuthConnection(
  connectionId: string,
  revision: number,
  client: ApiClient = apiClient,
) {
  const { data, error, response } = await client.POST(
    '/admin/auth-connections/{connectionId}/archive',
    {
      params: { path: { connectionId } },
      body: { expected_revision: revision },
    },
  )
  return result(data, error, response)
}

export async function restoreAuthConnection(
  connectionId: string,
  revision: number,
  client: ApiClient = apiClient,
) {
  const { data, error, response } = await client.POST(
    '/admin/auth-connections/{connectionId}/restore',
    {
      params: { path: { connectionId } },
      body: { expected_revision: revision },
    },
  )
  return result(data, error, response)
}
