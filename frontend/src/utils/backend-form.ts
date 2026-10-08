import type { ApiSchemas } from '@/api/client-core'

export type Backend = ApiSchemas['Backend']
export type BackendType = Backend['type']
export type CredentialMode = 'keep' | 'new' | 'existing'
export interface BackendFormModel {
  name: string
  type: BackendType | null
  secret: string
  probeSecret: string
  credentialMode: CredentialMode
  credentialId: number | null
  base_url: string
  model: string
  temperatureEnabled: boolean
  temperature: number
  top_pEnabled: boolean
  top_p: number
  maxTokensEnabled: boolean
  max_tokens: number
  timeoutEnabled: boolean
  timeout: number
  response_format: ApiSchemas['ResponseFormat']
  enable_prompt_cache: boolean
  stream: boolean
  thinkingEnabled: boolean
  thinking_level: ApiSchemas['ThinkingLevel']
  rate_limit_per_minute: number
}
export const createBackendForm = (): BackendFormModel => ({
  name: '',
  type: null,
  secret: '',
  probeSecret: '',
  credentialMode: 'new',
  credentialId: null,
  base_url: '',
  model: '',
  temperatureEnabled: false,
  temperature: 0.2,
  top_pEnabled: false,
  top_p: 1,
  maxTokensEnabled: false,
  max_tokens: 0,
  timeoutEnabled: true,
  timeout: 60,
  response_format: 'json_schema',
  enable_prompt_cache: true,
  stream: false,
  thinkingEnabled: false,
  thinking_level: 'low',
  rate_limit_per_minute: 0,
})

export function backendBindingUnchanged(form: BackendFormModel, backend: Backend | null): boolean {
  return (
    !!backend &&
    form.type === backend.type &&
    form.base_url.trim() === (backend.options?.base_url ?? '').trim()
  )
}
export function buildBackendPayload(
  form: BackendFormModel,
  backend: Backend | null,
): ApiSchemas['CreateBackendRequest'] {
  if (!form.type || !form.name.trim() || !form.model.trim()) throw new Error('Invalid backend')
  const options: ApiSchemas['BackendOptions'] = {
    type: form.type,
    model: form.model.trim(),
    timeout: form.timeoutEnabled ? form.timeout : 0,
  }
  if (form.base_url.trim()) options.base_url = form.base_url.trim()
  const sampling =
    form.type !== 'anthropic' || !form.thinkingEnabled || form.thinking_level === 'off'
  if (sampling && form.temperatureEnabled) options.temperature = form.temperature
  if (sampling && form.top_pEnabled) options.top_p = form.top_p
  if (form.maxTokensEnabled) options.max_tokens = form.max_tokens
  if (form.response_format !== 'json_schema') options.response_format = form.response_format
  if (form.type === 'anthropic' && !form.enable_prompt_cache)
    (options as ApiSchemas['AnthropicBackendOptions']).enable_prompt_cache = false
  if (form.stream) options.stream = true
  if (form.thinkingEnabled) options.thinking_level = form.thinking_level
  const payload: ApiSchemas['CreateBackendRequest'] = {
    name: form.name.trim(),
    type: form.type,
    options,
    rate_limit_per_minute: form.rate_limit_per_minute,
  }
  if (form.credentialMode === 'keep') {
    if (!backendBindingUnchanged(form, backend)) throw new Error('Rebinding required')
  } else if (form.credentialMode === 'new') {
    if (!form.secret.trim()) throw new Error('Secret required')
    payload.secret = form.secret.trim()
  } else {
    if (!Number.isSafeInteger(form.credentialId) || (form.credentialId ?? 0) < 1)
      throw new Error('Credential required')
    payload.credential_id = form.credentialId!
  }
  return payload
}

/** Read metadata without forwarding unexpected write-only fields to the entity store. */
export function backendMetadata(value: Backend): Backend {
  const result: Backend = {
    id: value.id,
    scope: value.scope,
    name: value.name,
    type: value.type,
    credential: { id: value.credential.id, version: value.credential.version },
    has_secret: value.has_secret,
  }
  if (value.owner_org_id !== undefined) result.owner_org_id = value.owner_org_id
  if (value.owner_user_id !== undefined) result.owner_user_id = value.owner_user_id
  if (value.rate_limit_per_minute !== undefined)
    result.rate_limit_per_minute = value.rate_limit_per_minute
  if (value.options) {
    const options: Record<string, unknown> = {}
    const keys = [
      'type',
      'model',
      'base_url',
      'max_tokens',
      'timeout',
      'response_format',
      'temperature',
      'top_p',
      'stream',
      'thinking_level',
    ]
    if (value.type === 'anthropic') keys.push('enable_prompt_cache')
    for (const key of keys)
      if (Object.hasOwn(value.options, key))
        options[key] = (value.options as unknown as Record<string, unknown>)[key]
    result.options = options as ApiSchemas['BackendOptions']
  }
  return result
}
