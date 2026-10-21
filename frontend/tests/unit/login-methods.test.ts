import { describe, expect, it, vi } from 'vitest'
import type { ApiClient } from '../../src/api/client-core'
import {
  deleteCurrentUserPassword,
  fetchLoginMethods,
  reauthenticateWithPassword,
  setInitialPassword,
  unlinkLoginBinding,
} from '../../src/api/login-methods'
vi.mock('@/i18n', () => ({ t: (key: string) => key }))
vi.mock('@/api/client-core', () => ({ apiClient: {} }))

describe('login methods API', () => {
  it('keeps fetchLoginMethods contract-guarded on successful responses', async () => {
    const methods = {
      password: {
        is_set: false,
        can_login: false,
        can_set: true,
        can_delete: false,
        verified: false,
      },
      email: { address: null, verified: false, can_recover: false },
      bindings: [],
      available_connections: [],
    }
    const GET = vi.fn().mockResolvedValue({
      data: { ...methods, password: { is_set: true } },
      response: new Response(null, { status: 200 }),
    })
    await expect(fetchLoginMethods({ GET } as unknown as ApiClient)).rejects.toThrow()
    GET.mockResolvedValue({ data: methods, response: new Response(null, { status: 200 }) })
    await expect(fetchLoginMethods({ GET } as unknown as ApiClient)).resolves.toEqual(methods)
  })

  const writes = [
    [
      'set',
      (client: ApiClient) =>
        setInitialPassword(
          { new_password: ' untrimmed ', reauthentication_proof: 'proof' },
          client,
        ),
    ],
    ['delete password', (client: ApiClient) => deleteCurrentUserPassword('proof', client)],
    ['unlink', (client: ApiClient) => unlinkLoginBinding(7, 'proof', client)],
  ] as const
  it.each(writes)('rejects an empty 503 response for %s', async (_name, write) => {
    const request = vi.fn().mockResolvedValue({ response: new Response(null, { status: 503 }) })
    await expect(
      write({ POST: request, DELETE: request } as unknown as ApiClient),
    ).rejects.toMatchObject({ status: 503 })
  })
  it.each(writes)('accepts empty 204 response for %s', async (_name, write) => {
    const request = vi.fn().mockResolvedValue({ response: new Response(null, { status: 204 }) })
    await expect(
      write({ POST: request, DELETE: request } as unknown as ApiClient),
    ).resolves.toBeUndefined()
  })
  it('keeps proof errors typed for retry without clearing the session', async () => {
    const request = vi.fn().mockResolvedValue({
      error: { title: 'invalid_proof' },
      response: new Response(null, { status: 403 }),
    })
    await expect(
      deleteCurrentUserPassword('proof', { DELETE: request } as unknown as ApiClient),
    ).rejects.toMatchObject({ status: 403 })
    expect(request).toHaveBeenCalledWith('/users/me/password', {
      body: { reauthentication_proof: 'proof' },
    })
  })
  it('scopes local password proof to action and target while preserving whitespace', async () => {
    const result = { reauthentication_proof: 'one-use', expires_at: '2099-01-01T00:00:00Z' }
    const request = vi
      .fn()
      .mockResolvedValue({ data: result, response: new Response(null, { status: 200 }) })
    await expect(
      reauthenticateWithPassword({ password: ' password ', action: 'unlink', targetId: '7' }, {
        POST: request,
      } as unknown as ApiClient),
    ).resolves.toEqual(result)
    expect(request).toHaveBeenCalledWith('/auth/reauthenticate', {
      body: { method: 'password', password: ' password ', action: 'unlink', target_id: '7' },
    })
  })
  it('rejects external-start responses when local password proof was requested', async () => {
    const request = vi.fn().mockResolvedValue({
      data: { authorization_url: 'https://idp.test' },
      response: new Response(null, { status: 200 }),
    })
    await expect(
      reauthenticateWithPassword({ password: 'password', action: 'link', targetId: 'new' }, {
        POST: request,
      } as unknown as ApiClient),
    ).rejects.toThrow()
  })
  it('does not render capabilities from a failed HTTP response', async () => {
    const request = vi
      .fn()
      .mockResolvedValue({ data: {}, response: new Response(null, { status: 503 }) })
    await expect(fetchLoginMethods({ GET: request } as unknown as ApiClient)).rejects.toMatchObject(
      { status: 503 },
    )
  })
})
