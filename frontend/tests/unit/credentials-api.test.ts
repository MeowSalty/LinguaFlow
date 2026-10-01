import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createApiClient, setLocalMode } from '@/api/client-core'
import { changeSessionContext } from '@/api/session-context'
import {
  collectCredentialVersions,
  createCredential,
  fetchCredentials,
  fetchCredentialVersions,
  revokeCredentialVersion,
  rotateCredential,
} from '@/api/credentials'
import { createBackend, fetchBackends, listBackendModels, updateBackend } from '@/api/backends'
vi.mock('@/i18n', () => ({ t: (key: string) => key }))
const credential = {
  id: 8,
  scope: 'user',
  owner_id: 1,
  provider: 'openai',
  endpoint: 'https://api.test/v1',
  current_version: 2,
}
const backend = {
  id: 1,
  scope: 'user',
  owner_user_id: 1,
  name: 'Backend',
  type: 'openai',
  options: { type: 'openai', model: 'model' },
  has_secret: true,
  credential: { id: 8, version: 2 },
}
const json = (data: unknown, status = 200) =>
  new Response(JSON.stringify(data), { status, headers: { 'Content-Type': 'application/json' } })
beforeEach(() => {
  changeSessionContext('https://api.test/api/v1', 1, true)
  setLocalMode(true)
})
afterEach(() => {
  vi.unstubAllGlobals()
  setLocalMode(false)
})
describe('credential API wire contract', () => {
  it('uses generated personal/org DTOs and shared version paths, accepting empty 204 and collection zero', async () => {
    const requests: { path: string; method: string; body?: unknown }[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn(async (request: Request) => {
        const path = new URL(request.url).pathname.replace('/api/v1', '')
        requests.push({
          path,
          method: request.method,
          ...(request.method === 'POST' && request.headers.get('content-type')?.includes('json')
            ? { body: await request.json() }
            : {}),
        })
        if (path.endsWith('/revoke')) return new Response(null, { status: 204 })
        if (path.endsWith('/collect')) return json({ deleted_versions: 0 })
        if (path.endsWith('/versions'))
          return request.method === 'GET'
            ? json({
                items: [
                  {
                    version: 2,
                    revoked: true,
                    created_at: '2026-10-01T00:00:00Z',
                    secret: 'unexpected',
                  },
                ],
              })
            : json({ id: 8, version: 3 }, 201)
        return request.method === 'GET'
          ? json({ items: [{ ...credential, secret: 'unexpected' }] })
          : json(credential, 201)
      }),
    )
    const client = createApiClient({ baseUrl: 'https://api.test/api/v1' })
    expect(await fetchCredentials(null, undefined, client)).toEqual({ items: [credential] })
    await fetchCredentials(7, undefined, client)
    await createCredential({ provider: 'openai', secret: 'ephemeral' }, null, client)
    await createCredential(
      { provider: 'openai', endpoint: 'https://api.test/v1', secret: 'ephemeral' },
      7,
      client,
    )
    expect(await fetchCredentialVersions(8, undefined, client)).toEqual({
      items: [{ version: 2, revoked: true, created_at: '2026-10-01T00:00:00Z' }],
    })
    expect(await rotateCredential(8, { secret: 'rotated' }, client)).toEqual({ id: 8, version: 3 })
    await expect(revokeCredentialVersion(8, 2, client)).resolves.toBeUndefined()
    expect(await collectCredentialVersions(8, client)).toEqual({ deleted_versions: 0 })
    expect(requests.map(({ path, method }) => `${method} ${path}`)).toEqual([
      'GET /credentials',
      'GET /orgs/7/credentials',
      'POST /credentials',
      'POST /orgs/7/credentials',
      'GET /credentials/8/versions',
      'POST /credentials/8/versions',
      'POST /credentials/8/versions/2/revoke',
      'POST /credentials/8/collect',
    ])
    expect(requests[2]?.body).toEqual({ provider: 'openai', secret: 'ephemeral' })
    expect(requests[5]?.body).toEqual({ secret: 'rotated' })
  })
  it('never caches unexpected Backend secrets and sends top-level probe credentials', async () => {
    const requests: Record<string, unknown>[] = []
    vi.stubGlobal(
      'fetch',
      vi.fn(async (request: Request) => {
        if (request.method === 'GET')
          return json({
            items: [{ ...backend, options: { ...backend.options, api_key: 'unexpected' } }],
          })
        requests.push(await request.json())
        if (request.url.endsWith('/models')) return json({ items: [] })
        return json({ ...backend, secret: 'unexpected' })
      }),
    )
    const client = createApiClient({ baseUrl: 'https://api.test/api/v1' })
    expect(JSON.stringify(await fetchBackends(client))).not.toContain('unexpected')
    const payload = {
      name: 'Backend',
      type: 'openai' as const,
      options: { type: 'openai' as const, model: 'model' },
      secret: 'only-request',
    }
    expect(await createBackend(payload, client, 7)).toEqual(backend)
    await updateBackend(1, { ...payload, secret: undefined }, client, 7)
    await listBackendModels({ type: 'openai', secret: 'probe-only' }, client)
    expect(requests[0]).toHaveProperty('secret', 'only-request')
    expect(requests[1]).not.toHaveProperty('secret')
    expect(requests[2]).toEqual({ type: 'openai', secret: 'probe-only' })
  })
  it('sanitizes errors and does not retry an interrupted credential POST', async () => {
    const fetch = vi
      .fn()
      .mockResolvedValueOnce(json({ title: 'failure', detail: 'leaked-secret-value' }, 400))
      .mockRejectedValueOnce(new TypeError('leaked-secret-value'))
    vi.stubGlobal('fetch', fetch)
    const client = createApiClient({ baseUrl: 'https://api.test/api/v1' })
    await expect(
      createCredential({ provider: 'openai', secret: 'leaked-secret-value' }, null, client),
    ).rejects.toMatchObject({ message: 'configurationCredentials.bindingFailure', status: 400 })
    await expect(
      createCredential({ provider: 'openai', secret: 'leaked-secret-value' }, null, client),
    ).rejects.toMatchObject({ message: 'configurationCredentials.failure', status: undefined })
    expect(fetch).toHaveBeenCalledTimes(2)
  })
})
