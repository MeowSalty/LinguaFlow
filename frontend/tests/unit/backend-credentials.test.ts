import { describe, expect, it, vi } from 'vitest'
import {
  backendBindingUnchanged,
  backendMetadata,
  buildBackendPayload,
  createBackendForm,
} from '@/utils/backend-form'
import type { ApiSchemas } from '@/api/client-core'
import { isUnknownCredentialWrite, safeCredentialError } from '@/api/credential-errors'

vi.mock('@/i18n', () => ({ t: (key: string) => key }))
const backend: ApiSchemas['Backend'] = {
  id: 1,
  scope: 'user',
  owner_user_id: 1,
  name: 'test',
  type: 'openai',
  options: { type: 'openai', model: 'model', base_url: ' https://api.test/v1 ' },
  has_secret: true,
  credential: { id: 7, version: 2 },
}
function form() {
  return {
    ...createBackendForm(),
    name: ' test ',
    type: 'openai' as const,
    model: ' model ',
    base_url: 'https://api.test/v1',
    secret: ' synthetic-key ',
    probeSecret: 'temporary-only',
  }
}
describe('Backend credential request contract', () => {
  it('sends a write-only top-level secret and only provider-supported options', () => {
    const draft = { ...form(), api_key: 'legacy', arbitrary: 'private' }
    const body = buildBackendPayload(draft, null)
    expect(body.secret).toBe('synthetic-key')
    expect(body).not.toHaveProperty('credential_id')
    expect(body.options).toEqual({
      type: 'openai',
      model: 'model',
      base_url: 'https://api.test/v1',
      timeout: 60,
    })
    expect(JSON.stringify(body)).not.toMatch(/api_key|legacy|temporary-only|arbitrary/)
  })
  it('distinguishes create/rebind existing, rebind new and keep', () => {
    const draft = form()
    draft.credentialMode = 'existing'
    draft.credentialId = 9
    for (const target of [null, backend])
      expect(buildBackendPayload(draft, target)).toMatchObject({ credential_id: 9 })
    expect(buildBackendPayload(draft, backend)).not.toHaveProperty('secret')
    draft.credentialMode = 'keep'
    expect(buildBackendPayload(draft, backend)).not.toHaveProperty('secret')
    expect(buildBackendPayload(draft, backend)).not.toHaveProperty('credential_id')
    expect(() => buildBackendPayload(draft, null)).toThrow()
    draft.credentialMode = 'new'
    expect(buildBackendPayload(draft, backend)).toHaveProperty('secret', 'synthetic-key')
  })
  it('requires rebind when provider or trimmed endpoint differs, without normalizing URLs', () => {
    const draft = form()
    draft.credentialMode = 'keep'
    expect(backendBindingUnchanged(draft, backend)).toBe(true)
    draft.base_url += '/'
    expect(() => buildBackendPayload(draft, backend)).toThrow('Rebinding')
    draft.base_url = 'https://api.test/v1'
    draft.type = 'openai'
    expect(backendBindingUnchanged({ ...draft, type: 'google' }, backend)).toBe(false)
    expect(buildBackendPayload(draft, backend)).not.toHaveProperty('credential_id')
  })
  it('rejects blank secrets and invalid credential IDs; probe secret cannot satisfy binding', () => {
    expect(() => buildBackendPayload({ ...form(), secret: ' ' }, null)).toThrow('Secret')
    for (const credentialId of [null, 0, -1, 0.2])
      expect(() =>
        buildBackendPayload({ ...form(), credentialMode: 'existing', credentialId }, null),
      ).toThrow('Credential')
  })
  it('never carries secrets from a response into metadata', () => {
    const raw = {
      ...backend,
      secret: 'hidden-top',
      options: { ...backend.options!, api_key: 'hidden-option' },
      credential: { ...backend.credential, secret: 'hidden-binding' },
    }
    expect(JSON.stringify(backendMetadata(raw))).not.toContain('hidden')
    expect(backendMetadata(raw)).toEqual(backend)
  })
  it('honors provider parameter differences and explicit zero values', () => {
    const draft = {
      ...form(),
      type: 'anthropic' as const,
      thinkingEnabled: true,
      thinking_level: 'low' as const,
      temperatureEnabled: true,
      temperature: 0,
      top_pEnabled: true,
      top_p: 0,
      enable_prompt_cache: false,
      timeoutEnabled: false,
    }
    const body = buildBackendPayload(draft, null)
    expect(body.options).not.toHaveProperty('temperature')
    expect(body.options).not.toHaveProperty('top_p')
    expect(body.options).toMatchObject({
      timeout: 0,
      enable_prompt_cache: false,
      thinking_level: 'low',
    })
    expect(buildBackendPayload({ ...draft, thinking_level: 'off' }, null).options).toMatchObject({
      temperature: 0,
      top_p: 0,
    })
    expect(buildBackendPayload({ ...draft, type: 'google' }, null).options).not.toHaveProperty(
      'enable_prompt_cache',
    )
  })
  it('does not disclose Problem contents and recognizes uncertain writes', () => {
    const error = safeCredentialError(
      { title: 'secret leaked', detail: 'synthetic-key' },
      new Response(null, { status: 400 }),
    )
    expect(error.message).toBe('configurationCredentials.bindingFailure')
    expect(JSON.stringify(error)).not.toContain('synthetic-key')
    expect(isUnknownCredentialWrite(error)).toBe(false)
    expect(isUnknownCredentialWrite(new TypeError('network'))).toBe(true)
    expect(isUnknownCredentialWrite(new DOMException('aborted', 'AbortError'))).toBe(true)
  })
})
