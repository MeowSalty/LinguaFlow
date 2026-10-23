import { apiClient, createApiClient, type ApiSchemas } from '@/api/client-core'
import { assertSessionCurrent, captureSession, changeSessionContext } from '@/api/session-context'
import { clearAuthTokens, getAuthEpoch } from '@/api/token-storage'
import { buildRequestFailureError } from '@/api/utils'
import { isExternalConfirmation } from '@/api/auth-contract'
import { canRetryExternalConfirmation } from '@/auth/errors'
import { t } from '@/i18n'
import { safeReturnPath } from './return-path'

type Action = ApiSchemas['ReauthenticationRequest']['action']
type Purpose = 'login' | 'link' | 'reauthenticate'
type Confirmation = ApiSchemas['ExternalConfirmationResponse']
export interface ExternalFlow {
  baseUrl: string
  epoch: string
  userId: number | null
  purpose: Purpose
  connectionId?: string
  action?: Action
  targetId?: string
  verifier: string
  transactionId: string
  expiresAt: string
  returnTo: string
  stage: 'started' | 'exchanging' | 'confirming' | 'completing'
  confirmation?: Confirmation
}
interface ProofResult {
  action: Action
  targetId: string
  proof: string
  expiresAt: string
}
interface StoredResult {
  baseUrl: string
  epoch: string
  userId: number | null
  expiresAt: string
  proof?: ProofResult
}
export const EXTERNAL_FLOW_KEY = 'linguaflow.external-flow.v1'
const RESULT_KEY = 'linguaflow.external-result.v1'
const invalid = () => new Error(t('authentication.flowInvalid'))

function write(key: string, value: unknown): void {
  try {
    sessionStorage.setItem(key, JSON.stringify(value))
  } catch {
    throw new Error(t('authentication.storageUnavailable'))
  }
}
function read<T>(key: string): T | null {
  try {
    const raw = sessionStorage.getItem(key)
    return raw ? (JSON.parse(raw) as T) : null
  } catch {
    return null
  }
}
function remove(key: string): void {
  try {
    sessionStorage.removeItem(key)
  } catch {
    /* Unavailable storage has no resumable flow. */
  }
}
function matches(value: Pick<ExternalFlow, 'baseUrl' | 'epoch' | 'userId' | 'expiresAt'>): boolean {
  const context = captureSession()
  return (
    value.baseUrl === context.baseUrl &&
    value.epoch === getAuthEpoch() &&
    value.userId === context.userId &&
    Date.parse(value.expiresAt) > Date.now()
  )
}
export function readExternalFlow(): ExternalFlow | null {
  const flow = read<ExternalFlow>(EXTERNAL_FLOW_KEY)
  if (
    !flow ||
    !matches(flow) ||
    typeof flow.verifier !== 'string' ||
    !flow.transactionId ||
    !['login', 'link', 'reauthenticate'].includes(flow.purpose) ||
    (flow.confirmation !== undefined &&
      (!isExternalConfirmation(flow.confirmation) ||
        flow.confirmation.transaction_id !== flow.transactionId ||
        flow.confirmation.profile.connection_id !== flow.connectionId ||
        (flow.purpose === 'login' && flow.confirmation.kind !== 'signup_confirmation') ||
        (flow.purpose !== 'login' && flow.confirmation.kind !== 'link_confirmation')))
  ) {
    remove(EXTERNAL_FLOW_KEY)
    return null
  }
  return flow
}
export function clearExternalFlow(): void {
  remove(EXTERNAL_FLOW_KEY)
}
function requireFlow(): ExternalFlow {
  const flow = readExternalFlow()
  if (!flow) throw invalid()
  return flow
}
function assertFlow(flow: ExternalFlow): void {
  const current = readExternalFlow()
  if (!current || current.transactionId !== flow.transactionId) throw invalid()
}
const flowClient = (flow: ExternalFlow) =>
  flow.userId === null
    ? createApiClient({ baseUrl: flow.baseUrl, getAccessToken: () => null })
    : apiClient

const base64url = (bytes: Uint8Array) =>
  btoa(String.fromCharCode(...bytes))
    .replace(/\+/g, '-')
    .replace(/\//g, '_')
    .replace(/=+$/, '')
export async function createBrowserChallenge(): Promise<{
  verifier: string
  browser_challenge: string
}> {
  if (!globalThis.crypto?.subtle) throw new Error(t('authentication.secureContextRequired'))
  const verifier = base64url(crypto.getRandomValues(new Uint8Array(32)))
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(verifier))
  return { verifier, browser_challenge: base64url(new Uint8Array(digest)) }
}
async function start(
  purpose: Purpose,
  details: Pick<ExternalFlow, 'connectionId' | 'action' | 'targetId'>,
  returnTo: string,
  submit: (
    input: ApiSchemas['ExternalBrowserInput'],
  ) => Promise<ApiSchemas['ExternalStartResponse']>,
): Promise<void> {
  if (purpose !== 'login' && captureSession().userId === null) throw invalid()
  const context = captureSession()
  const epoch = getAuthEpoch()
  const challenge = await createBrowserChallenge()
  assertSessionCurrent(context)
  // Verify storage availability before creating a transaction with a one-use proof.
  write(EXTERNAL_FLOW_KEY, null)
  remove(RESULT_KEY)
  const result = await submit({
    browser_challenge: challenge.browser_challenge,
    completion_url: new URL('/auth/complete', location.origin).href,
    return_to: safeReturnPath(returnTo),
  })
  assertSessionCurrent(context)
  if (getAuthEpoch() !== epoch) throw invalid()
  const url = new URL(result.authorization_url)
  if (!['https:', 'http:'].includes(url.protocol) || url.username || url.password) throw invalid()
  write(EXTERNAL_FLOW_KEY, {
    ...details,
    baseUrl: context.baseUrl,
    epoch,
    userId: context.userId,
    purpose,
    verifier: challenge.verifier,
    transactionId: result.transaction_id,
    expiresAt: result.expires_at,
    returnTo: safeReturnPath(returnTo),
    stage: 'started',
  } satisfies ExternalFlow)
  location.assign(url.href)
}
export async function startExternalLogin(connectionId: string, returnTo = '/'): Promise<void> {
  clearAuthTokens()
  changeSessionContext(captureSession().baseUrl, null, true)
  await start('login', { connectionId }, returnTo, async (input) => {
    const { data, error, response } = await apiClient.POST('/auth/external/{connectionId}/start', {
      params: { path: { connectionId } },
      body: input,
    })
    if (!data) throw buildRequestFailureError(t('authentication.failed'), error, response)
    return data
  })
}
export async function startExternalLink(
  connectionId: string,
  proof: string,
  returnTo = '/settings/security',
): Promise<void> {
  await start('link', { connectionId }, returnTo, async (input) => {
    const { data, error, response } = await apiClient.POST(
      '/users/me/login-bindings/{connectionId}/start',
      {
        params: { path: { connectionId } },
        body: { ...input, reauthentication_proof: proof },
      },
    )
    if (!data) throw buildRequestFailureError(t('authentication.failed'), error, response)
    return data
  })
}
export async function startExternalReauthentication(
  bindingId: number,
  action: Action,
  targetId: string,
): Promise<void> {
  await start('reauthenticate', { action, targetId }, '/settings/security', async (input) => {
    const { data, error, response } = await apiClient.POST('/auth/reauthenticate', {
      body: { ...input, method: 'oidc', binding_id: bindingId, action, target_id: targetId },
    })
    if (!data) throw buildRequestFailureError(t('authentication.failed'), error, response)
    if (!('authorization_url' in data)) throw invalid()
    return data
  })
}
export async function exchangeExternalTicket(
  ticket?: string,
): Promise<ApiSchemas['ExternalExchangeResponse']> {
  const flow = requireFlow()
  if (!ticket && flow.stage === 'confirming' && flow.confirmation) return flow.confirmation
  if (!ticket || flow.stage !== 'started') throw invalid()
  write(EXTERNAL_FLOW_KEY, { ...flow, stage: 'exchanging' })
  const { data, error, response } = await flowClient(flow).POST('/auth/external/exchange', {
    body: { ticket, verifier: flow.verifier },
  })
  assertFlow(flow)
  if (!data) throw buildRequestFailureError(t('authentication.failed'), error, response)
  const allowed = {
    login: ['session', 'signup_confirmation'],
    link: ['link_confirmation'],
    reauthenticate: ['reauthentication_proof'],
  }
  if (!allowed[flow.purpose].includes(data.kind)) {
    clearExternalFlow()
    throw invalid()
  }
  if (data.kind === 'signup_confirmation' || data.kind === 'link_confirmation') {
    if (
      !isExternalConfirmation(data) ||
      data.transaction_id !== flow.transactionId ||
      data.profile.connection_id !== flow.connectionId
    ) {
      clearExternalFlow()
      throw invalid()
    }
    write(EXTERNAL_FLOW_KEY, {
      ...flow,
      stage: 'confirming',
      confirmation: data,
      expiresAt: new Date(
        Math.min(Date.parse(flow.expiresAt), Date.parse(data.expires_at)),
      ).toISOString(),
    })
  }
  return data
}
export async function completeExternalConfirmation(
  input: Pick<
    ApiSchemas['ExternalCompleteRequest'],
    'username' | 'display_name' | 'email' | 'initial_password'
  > = {},
): Promise<ApiSchemas['ExternalCompleteResponse']> {
  const flow = requireFlow()
  if (flow.stage !== 'confirming' || !flow.confirmation) throw invalid()
  write(EXTERNAL_FLOW_KEY, { ...flow, stage: 'completing' })
  try {
    const { data, error, response } = await flowClient(flow).POST('/auth/external/complete', {
      body: {
        ...(flow.purpose === 'login' ? input : {}),
        confirmation_token: flow.confirmation.confirmation_token,
        verifier: flow.verifier,
      },
    })
    assertFlow(flow)
    if (!data) throw buildRequestFailureError(t('authentication.failed'), error, response)
    if (
      (flow.purpose === 'login' && data.kind !== 'session') ||
      (flow.purpose === 'link' && data.kind !== 'linked')
    )
      throw invalid()
    return data
  } catch (error) {
    // Only explicit unconsumed input and username conflicts may resume this confirmation.
    if (
      canRetryExternalConfirmation(error) &&
      readExternalFlow()?.transactionId === flow.transactionId
    )
      write(EXTERNAL_FLOW_KEY, flow)
    else clearExternalFlow()
    throw error
  }
}
export async function cancelExternalFlow(): Promise<void> {
  const flow = requireFlow()
  if (flow.stage === 'completing' || flow.stage === 'exchanging') {
    clearExternalFlow()
    return
  }
  try {
    const { error, response } = await flowClient(flow).POST('/auth/external/cancel', {
      body: { transaction_id: flow.transactionId, verifier: flow.verifier },
    })
    if (!response.ok)
      throw buildRequestFailureError(t('authentication.cancelFailed'), error, response)
  } finally {
    if (readExternalFlow()?.transactionId === flow.transactionId) clearExternalFlow()
  }
}
export function saveExternalResult(result: ApiSchemas['ExternalProofResponse']): void {
  const flow = requireFlow()
  const stored: StoredResult = {
    baseUrl: flow.baseUrl,
    epoch: flow.epoch,
    userId: flow.userId,
    expiresAt: new Date(Math.min(Date.parse(flow.expiresAt), Date.now() + 300_000)).toISOString(),
  }
  if (flow.purpose !== 'reauthenticate' || !flow.action || !flow.targetId) throw invalid()
  stored.expiresAt = result.expires_at
  stored.proof = {
    action: flow.action,
    targetId: flow.targetId,
    proof: result.reauthentication_proof,
    expiresAt: result.expires_at,
  }
  write(RESULT_KEY, stored)
  clearExternalFlow()
}
function takeResult(): StoredResult | null {
  const result = read<StoredResult>(RESULT_KEY)
  remove(RESULT_KEY)
  return result && matches(result) ? result : null
}
export function takeExternalProof(): ProofResult | null {
  return takeResult()?.proof ?? null
}
