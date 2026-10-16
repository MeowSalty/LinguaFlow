import { describe, expect, it, vi } from 'vitest'
import type { ApiClient } from '../../src/api/client-core'
import {
  AuthConnectionError,
  fetchAuthConnectionAttributes,
  fetchAuthConnectionImpact,
  publishAuthConnection,
  updateAuthConnection,
  uploadAuthConnectionLogo,
  type AuthConnection,
  type AuthAttributeField,
} from '../../src/api/auth-connections'
import {
  clone,
  connectionUpdate,
  hasUnsafeRuleNumbers,
  mergeDraftAttributes,
  newConnectionSnapshot,
  parsePreviewSources,
} from '../../src/components/admin/authentication/auth-connection-model'
vi.mock('@/i18n', () => ({ t: (key: string) => key }))
vi.mock('@/api/client-core', () => ({ apiClient: {} }))
const record = (): AuthConnection => ({
  id: 'test-id',
  kind: 'oidc',
  aggregate_revision: 9,
  draft_revision: 8,
  archived: false,
  has_client_secret: true,
  draft: { ...newConnectionSnapshot(), revision: 8 },
})

describe('authentication connection writes', () => {
  it('uploads image bytes with the declared media type and current aggregate revision', async () => {
    const file = new File([new Uint8Array([137, 80, 78, 71])], 'logo.png', { type: 'image/png' })
    const POST = vi.fn().mockResolvedValue({ data: record(), response: new Response() })
    await uploadAuthConnectionLogo('test-id', 9, file, { POST } as unknown as ApiClient)
    const options = POST.mock.calls[0]?.[1]
    expect(options.params.query.expected_revision).toBe(9)
    expect(options.headers['Content-Type']).toBe('image/png')
    expect(options.bodySerializer()).toBe(file)
  })
  it('rejects unsupported, empty and oversized logo files before sending', async () => {
    const POST = vi.fn()
    for (const file of [
      new File(['<svg />'], 'logo.svg', { type: 'image/svg+xml' }),
      new File([], 'logo.png', { type: 'image/png' }),
      new File([new Uint8Array(256 * 1024 + 1)], 'logo.png', { type: 'image/png' }),
    ]) {
      await expect(
        uploadAuthConnectionLogo('test-id', 9, file, { POST } as unknown as ApiClient),
      ).rejects.toThrow()
    }
    expect(POST).not.toHaveBeenCalled()
  })
  it('rejects unsupported password conditions instead of silently replacing them', () => {
    const r = { ...record(), kind: 'password' as const }
    r.draft!.policy.login_rules = {
      mode: 'conditions',
      match: 'all',
      conditions: [{ field_id: 'id', operator: 'eq', value: 12 }],
    }
    expect(() => connectionUpdate(r, r.draft!, 'keep', '')).toThrow(
      'Password rules must be unrestricted',
    )
  })
  it('preserves, replaces and explicitly clears secrets without conflating empty input', () => {
    const r = record()
    expect(connectionUpdate(r, r.draft!, 'keep', 'ignored')).not.toHaveProperty('client_secret')
    expect(connectionUpdate(r, r.draft!, 'replace', ' secret ')).toMatchObject({
      expected_revision: 9,
      client_secret: ' secret ',
    })
    expect(connectionUpdate(r, r.draft!, 'clear', '')).toHaveProperty('client_secret', null)
    expect(() => connectionUpdate(r, r.draft!, 'replace', '')).toThrow()
    expect(connectionUpdate(r, r.draft!, 'keep', '')).not.toHaveProperty('oauth2')
    expect(connectionUpdate(r, r.draft!, 'keep', '')).not.toHaveProperty('identity_authority_id')
  })
  it('never sends external config, secrets or conditional rules for built-in password', () => {
    const r = { ...record(), kind: 'password' as const }
    const body = connectionUpdate(r, r.draft!, 'replace', 'secret')
    expect(body).not.toHaveProperty('client_secret')
    expect(body).not.toHaveProperty('attribute_mappings')
    expect(body).not.toHaveProperty('oidc')
    for (const purpose of ['login_rules', 'signup_rules', 'link_rules'] as const)
      expect(body.policy?.[purpose]).toEqual({ mode: 'unrestricted' })
  })
  it('blocks an already rounded unsafe integer even when the draft deletes the condition', () => {
    const r = record()
    r.draft!.policy.login_rules = {
      mode: 'conditions',
      match: 'all',
      conditions: [{ field_id: 'id', operator: 'eq', value: JSON.parse('9007199254740993') }],
    }
    expect(hasUnsafeRuleNumbers(r.draft)).toBe(true)
    const edited = clone(r.draft!)
    edited.policy.login_rules = { mode: 'unrestricted' }
    expect(() => connectionUpdate(r, edited, 'keep', '')).toThrow('Unsafe rule number')
  })
  it('retains safe integers and finite fractional values', () => {
    const r = record()
    r.draft!.policy.login_rules = {
      mode: 'conditions',
      match: 'all',
      conditions: [
        { field_id: 'id', operator: 'eq', value: Number.MAX_SAFE_INTEGER },
        { field_id: 'score', operator: 'gt', value: 0.125 },
      ],
    }
    expect(hasUnsafeRuleNumbers(r.draft)).toBe(false)
    expect(connectionUpdate(r, r.draft!, 'keep', '').policy).toEqual(r.draft!.policy)
  })
  it('uses aggregate revision for writes and catalogue queries, not draft revision', async () => {
    const r = record()
    const PATCH = vi.fn().mockResolvedValue({ data: r, response: new Response() })
    const POST = vi
      .fn()
      .mockResolvedValue({ data: { can_publish: true }, response: new Response() })
    const GET = vi.fn().mockResolvedValue({
      data: { revision: 8, fields: [], operators: {} },
      response: new Response(),
    })
    const client = { PATCH, POST, GET } as unknown as ApiClient
    await updateAuthConnection(r.id, connectionUpdate(r, r.draft!, 'keep', ''), client)
    await fetchAuthConnectionAttributes(r.id, 9, client)
    await fetchAuthConnectionImpact(r.id, 9, client)
    expect(PATCH.mock.calls[0]![1].body.expected_revision).toBe(9)
    expect(GET.mock.calls[0]![1].params.query).toEqual({ expected_revision: 9 })
    expect(POST.mock.calls[0]![1].body).toEqual({ expected_revision: 9 })
  })
  it('retains the complete 409 problem and never retries publishing', async () => {
    const problem = {
      title: 'Safety conflict',
      detail: 'Fallback needed',
      status: 409,
      remediation: 'Verify another method',
      impact: {
        affected_users: [4],
        blocked_users: [4],
        affected_sessions: 2,
        can_publish: false,
        remediation: 'Repair binding',
      },
    }
    const POST = vi
      .fn()
      .mockResolvedValue({ error: problem, response: new Response(null, { status: 409 }) })
    const client = { POST } as unknown as ApiClient
    const failure = await publishAuthConnection('test-id', 9, client).catch((error) => error)
    expect(failure).toBeInstanceOf(AuthConnectionError)
    expect(failure.problem).toEqual(problem)
    expect(failure.status).toBe(409)
    expect(POST).toHaveBeenCalledTimes(1)
  })
})

describe('rule draft sources', () => {
  it('accepts only the preview sources declared by the contract', () => {
    expect(parsePreviewSources('{"oidc_id_token":{"team":"infra"}}')).toEqual({
      oidc_id_token: { team: 'infra' },
    })
    for (const input of [
      'null',
      '[]',
      '{"oauth2_profile":{}}',
      '{"oidc_id_token":[]}',
      '{"oidc_userinfo":null}',
    ])
      expect(() => parsePreviewSources(input)).toThrow()
  })
  it('merges unsaved fields and removes deleted custom fields without removing built-in ones', () => {
    const field = (id: string): AuthAttributeField => ({
      field_id: id,
      label: id,
      type: 'string',
      source: 'oidc_id_token',
      pointer: '/' + id,
    })
    const builtin = field('subject'),
      saved = field('custom'),
      added = field('new')
    expect(mergeDraftAttributes([builtin, saved], [saved], [added])).toEqual([builtin, added])
    expect(
      mergeDraftAttributes([builtin, saved], [saved], [{ ...saved, type: 'integer' }]),
    ).toEqual([builtin, { ...saved, type: 'integer' }])
  })
})
