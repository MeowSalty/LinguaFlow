import { apiClient, createApiClient, type ApiSchemas } from '@/api/client-core'
import {
  assertSessionCurrent,
  captureSession,
  changeSessionContext,
  isSessionCurrent,
  onSessionChange,
  type SessionSnapshot,
} from '@/api/session-core'
import { clearAuthTokens, getAuthEpoch } from '@/api/token-storage'
import { buildRequestFailureError } from '@/api/utils'
import { isExternalConfirmation, isReauthenticationProof } from '@/api/auth-contract'
import { canRetryExternalConfirmation } from '@/auth/errors'
import { t } from '@/i18n-runtime'
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
  transactionId: string
  expiresAt: string
  returnTo: string
  stage: 'started' | 'exchanging' | 'confirming' | 'completing'
}
export type ExternalConfirmation = Omit<Confirmation, 'confirmation_token'>
export type ExternalExchangeResult =
  | Exclude<ApiSchemas['ExternalExchangeResponse'], Confirmation>
  | ExternalConfirmation
interface RedirectFlow extends Omit<ExternalFlow, 'stage'> {
  stage: 'started'
  verifier: string
}
interface RuntimeFlow extends ExternalFlow {
  verifier: string
  confirmation?: Confirmation
  returnedProof?: ApiSchemas['ExternalProofResponse']
  context: SessionSnapshot
  version: number
}
interface ProofResult {
  action: Action
  targetId: string
  proof: string
  expiresAt: string
}
interface ProofHandoff {
  baseUrl: string
  epoch: string
  userId: number | null
  expiresAt: string
  proof: ProofResult
  context: SessionSnapshot
}
export const EXTERNAL_FLOW_KEY = 'linguaflow.external-flow.v2'
const LEGACY_KEYS = ['linguaflow.external-flow.v1', 'linguaflow.external-result.v1']
const STORAGE_PROBE_KEY = `${EXTERNAL_FLOW_KEY}.probe`
const invalid = () => new Error(t('authentication.flowInvalid'))
let runtime: RuntimeFlow | null = null
let callbackCandidate: RedirectFlow | null = null
let handoff: ProofHandoff | null = null
let version = 0
let expectedRedirect = false
let flowExpiry: ReturnType<typeof setTimeout> | undefined
let proofExpiry: ReturnType<typeof setTimeout> | undefined
let stopLifecycle: (() => void) | undefined
const resetListeners = new Set<(reason: 'expired' | 'invalidated') => void>()

function remove(key: string): void {
  try {
    sessionStorage.removeItem(key)
  } catch {
    /* A failed removal must never make a stored transaction eligible for exchange. */
  }
}
export function clearExternalBrowserStorage(storage: Pick<Storage, 'removeItem'>): void {
  for (const key of [EXTERNAL_FLOW_KEY, STORAGE_PROBE_KEY, ...LEGACY_KEYS]) {
    try {
      storage.removeItem(key)
    } catch {
      /* Cleanup cannot confer permission to resume a flow. */
    }
  }
}
function clearBrowserStorage(): void {
  try {
    clearExternalBrowserStorage(sessionStorage)
  } catch {
    /* Storage may be unavailable even when the application itself can start. */
  }
}
function verifyStorage(): void {
  try {
    sessionStorage.setItem(STORAGE_PROBE_KEY, '1')
    const available = sessionStorage.getItem(STORAGE_PROBE_KEY) === '1'
    sessionStorage.removeItem(STORAGE_PROBE_KEY)
    if (!available) throw invalid()
  } catch {
    remove(STORAGE_PROBE_KEY)
    throw new Error(t('authentication.storageUnavailable'))
  }
}
const deadlineIsFresh = (value: string): boolean =>
  Number.isFinite(Date.parse(value)) && Date.parse(value) > Date.now()
const object = (value: unknown): value is Record<string, unknown> =>
  !!value && typeof value === 'object' && !Array.isArray(value)
const nonempty = (value: unknown): value is string => typeof value === 'string' && !!value
const redirectFields = new Set([
  'baseUrl',
  'epoch',
  'userId',
  'purpose',
  'connectionId',
  'action',
  'targetId',
  'verifier',
  'transactionId',
  'expiresAt',
  'returnTo',
  'stage',
])
function isRedirectFlow(value: unknown): value is RedirectFlow {
  if (
    !object(value) ||
    Object.keys(value).some((key) => !redirectFields.has(key)) ||
    value.stage !== 'started' ||
    !nonempty(value.baseUrl) ||
    !nonempty(value.epoch) ||
    !(value.userId === null || (Number.isSafeInteger(value.userId) && Number(value.userId) > 0)) ||
    typeof value.verifier !== 'string' ||
    !/^[A-Za-z0-9_-]{43}$/.test(value.verifier) ||
    !nonempty(value.transactionId) ||
    typeof value.expiresAt !== 'string' ||
    !deadlineIsFresh(value.expiresAt) ||
    typeof value.returnTo !== 'string' ||
    safeReturnPath(value.returnTo) !== value.returnTo
  )
    return false
  if (value.purpose === 'login' || value.purpose === 'link')
    return (
      nonempty(value.connectionId) &&
      value.action === undefined &&
      value.targetId === undefined &&
      (value.purpose === 'login' ? value.userId === null : value.userId !== null)
    )
  return (
    value.purpose === 'reauthenticate' &&
    value.userId !== null &&
    value.connectionId === undefined &&
    nonempty(value.targetId) &&
    ['link', 'unlink', 'password_set', 'password_delete'].includes(value.action as string)
  )
}
function matches(value: Pick<ExternalFlow, 'baseUrl' | 'epoch' | 'userId' | 'expiresAt'>): boolean {
  const context = captureSession()
  return (
    value.baseUrl === context.baseUrl &&
    value.epoch === getAuthEpoch() &&
    value.userId === context.userId &&
    deadlineIsFresh(value.expiresAt)
  )
}
function clearRuntime(): void {
  clearTimeout(flowExpiry)
  flowExpiry = undefined
  if (runtime) {
    runtime.verifier = ''
    delete runtime.confirmation
    delete runtime.returnedProof
  }
  runtime = null
  expectedRedirect = false
  version++
}
export function clearExternalFlow(): void {
  clearRuntime()
  if (callbackCandidate) callbackCandidate.verifier = ''
  callbackCandidate = null
  clearBrowserStorage()
}
export function clearExternalProof(): void {
  clearTimeout(proofExpiry)
  proofExpiry = undefined
  if (handoff) handoff.proof.proof = ''
  handoff = null
}
export function onExternalAuthReset(
  callback: (reason: 'expired' | 'invalidated') => void,
): () => void {
  resetListeners.add(callback)
  return () => resetListeners.delete(callback)
}
function notifyReset(reason: 'expired' | 'invalidated'): void {
  for (const listener of Array.from(resetListeners))
    if (resetListeners.has(listener)) listener(reason)
}
function armFlowExpiry(flow: RuntimeFlow): void {
  clearTimeout(flowExpiry)
  flowExpiry = setTimeout(
    () => {
      if (runtime !== flow) return
      clearExternalFlow()
      notifyReset('expired')
    },
    Math.min(2_147_483_647, Math.max(0, Date.parse(flow.expiresAt) - Date.now())),
  )
}
function currentFlow(): RuntimeFlow | null {
  if (runtime && (!matches(runtime) || !isSessionCurrent(runtime.context))) {
    clearExternalFlow()
    notifyReset('invalidated')
  }
  return runtime
}
export function readExternalFlow(): ExternalFlow | null {
  const flow = currentFlow()
  if (!flow) return null
  return flowMetadata(flow)
}
function flowMetadata(flow: ExternalFlow): ExternalFlow {
  return {
    baseUrl: flow.baseUrl,
    epoch: flow.epoch,
    userId: flow.userId,
    purpose: flow.purpose,
    connectionId: flow.connectionId,
    action: flow.action,
    targetId: flow.targetId,
    transactionId: flow.transactionId,
    expiresAt: flow.expiresAt,
    returnTo: flow.returnTo,
    stage: flow.stage,
  }
}
/** Bootstrap can inspect the claimed identity without gaining access to its verifier. */
export function readPreparedExternalCallback(): ExternalFlow | null {
  return callbackCandidate ? flowMetadata(callbackCandidate) : null
}
/** Claim and remove the redirect record before bootstrap can restore an identity. */
export function prepareExternalCallback(ticket?: string): void {
  clearRuntime()
  clearExternalProof()
  callbackCandidate = null
  let raw: string | null = null
  try {
    raw = sessionStorage.getItem(EXTERNAL_FLOW_KEY)
    sessionStorage.removeItem(EXTERNAL_FLOW_KEY)
  } catch {
    raw = null
  }
  clearBrowserStorage()
  if (!ticket || !raw) return
  try {
    const candidate: unknown = JSON.parse(raw)
    if (isRedirectFlow(candidate)) callbackCandidate = candidate
  } catch {
    /* Malformed or legacy records cannot be resumed. */
  }
}
/** Bootstrap changes are expected; validate against its final service and user. */
export function activateExternalCallback(): void {
  const candidate = callbackCandidate
  callbackCandidate = null
  if (!candidate) return
  if (!matches(candidate)) {
    candidate.verifier = ''
    return
  }
  runtime = { ...candidate, context: captureSession(), version: ++version }
  armFlowExpiry(runtime)
}
export function installExternalAuthLifecycle(): () => void {
  if (stopLifecycle) return stopLifecycle
  const stopSession = onSessionChange(() => {
    // Preserve only the claimed candidate while bootstrap establishes its final identity.
    clearRuntime()
    clearExternalProof()
    clearBrowserStorage()
    notifyReset('invalidated')
  })
  const hidden = () => {
    const preserveRedirect =
      expectedRedirect &&
      runtime?.stage === 'started' &&
      matches(runtime) &&
      isSessionCurrent(runtime.context)
    clearRuntime()
    clearExternalProof()
    if (callbackCandidate) callbackCandidate.verifier = ''
    callbackCandidate = null
    if (!preserveRedirect) clearBrowserStorage()
    notifyReset('invalidated')
  }
  const shown = (event: PageTransitionEvent) => {
    if (!event.persisted) return
    clearExternalFlow()
    clearExternalProof()
    notifyReset('invalidated')
    window.location.reload()
  }
  window.addEventListener('pagehide', hidden)
  window.addEventListener('pageshow', shown)
  stopLifecycle = () => {
    stopSession()
    window.removeEventListener('pagehide', hidden)
    window.removeEventListener('pageshow', shown)
    stopLifecycle = undefined
  }
  return stopLifecycle
}
function requireFlow(): RuntimeFlow {
  const flow = currentFlow()
  if (!flow) throw invalid()
  return flow
}
function ownsFlow(flow: RuntimeFlow): boolean {
  return runtime === flow && flow.version === version
}
function assertFlow(flow: RuntimeFlow): void {
  if (!ownsFlow(flow) || !matches(flow) || !isSessionCurrent(flow.context)) throw invalid()
}
function confirmationSnapshot(value: Confirmation): ExternalConfirmation {
  return {
    kind: value.kind,
    transaction_id: value.transaction_id,
    expires_at: value.expires_at,
    profile: { ...value.profile },
    signup_requirements: [...value.signup_requirements],
    username_candidate: value.username_candidate,
    email: value.email,
    signup: value.signup ? { ...value.signup } : undefined,
  }
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
  clearExternalFlow()
  clearExternalProof()
  const operation = version
  const context = captureSession()
  const epoch = getAuthEpoch()
  let verifier = ''
  try {
    // Check readability, writes and removal before a start can spend a one-use proof.
    verifyStorage()
    const challenge = await createBrowserChallenge()
    verifier = challenge.verifier
    assertSessionCurrent(context)
    if (operation !== version || getAuthEpoch() !== epoch) throw invalid()
    const result = await submit({
      browser_challenge: challenge.browser_challenge,
      completion_url: new URL('/auth/complete', location.origin).href,
      return_to: safeReturnPath(returnTo),
    })
    assertSessionCurrent(context)
    if (operation !== version || getAuthEpoch() !== epoch) throw invalid()
    const url = new URL(result.authorization_url)
    if (!['https:', 'http:'].includes(url.protocol) || url.username || url.password) throw invalid()
    const redirect: RedirectFlow = {
      ...details,
      baseUrl: context.baseUrl,
      epoch,
      userId: context.userId,
      purpose,
      verifier,
      transactionId: result.transaction_id,
      expiresAt: result.expires_at,
      returnTo: safeReturnPath(returnTo),
      stage: 'started',
    }
    if (!isRedirectFlow(redirect)) throw invalid()
    try {
      sessionStorage.setItem(EXTERNAL_FLOW_KEY, JSON.stringify(redirect))
    } catch {
      throw new Error(t('authentication.storageUnavailable'))
    }
    runtime = { ...redirect, context, version: operation }
    armFlowExpiry(runtime)
    expectedRedirect = true
    location.assign(url.href)
  } catch (cause) {
    if (operation === version) clearExternalFlow()
    throw cause
  } finally {
    verifier = ''
  }
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
  signal?: AbortSignal,
): Promise<ExternalExchangeResult> {
  signal?.throwIfAborted()
  const flow = requireFlow()
  if (!ticket && flow.stage === 'confirming' && flow.confirmation)
    return confirmationSnapshot(flow.confirmation)
  if (!ticket || flow.stage !== 'started') throw invalid()
  flow.stage = 'exchanging'
  expectedRedirect = false
  remove(EXTERNAL_FLOW_KEY)
  try {
    const { data, error, response } = await flowClient(flow).POST('/auth/external/exchange', {
      body: { ticket, verifier: flow.verifier },
      signal,
    })
    signal?.throwIfAborted()
    assertFlow(flow)
    if (!data) throw buildRequestFailureError(t('authentication.failed'), error, response)
    const allowed = {
      login: ['session', 'signup_confirmation'],
      link: ['link_confirmation'],
      reauthenticate: ['reauthentication_proof'],
    }
    if (!allowed[flow.purpose].includes(data.kind)) throw invalid()
    if (data.kind === 'signup_confirmation' || data.kind === 'link_confirmation') {
      if (
        !isExternalConfirmation(data) ||
        data.transaction_id !== flow.transactionId ||
        data.profile.connection_id !== flow.connectionId
      )
        throw invalid()
      if (!deadlineIsFresh(data.expires_at)) throw new Error(t('authentication.flowExpired'))
      flow.stage = 'confirming'
      flow.confirmation = structuredClone(data)
      flow.expiresAt = new Date(
        Math.min(Date.parse(flow.expiresAt), Date.parse(data.expires_at)),
      ).toISOString()
      armFlowExpiry(flow)
      return confirmationSnapshot(flow.confirmation)
    }
    if (data.kind === 'reauthentication_proof') {
      if (!isReauthenticationProof(data) || !deadlineIsFresh(data.expires_at)) throw invalid()
      flow.verifier = ''
      flow.returnedProof = { ...data }
    } else clearExternalFlow()
    return data
  } catch (cause) {
    if (ownsFlow(flow)) clearExternalFlow()
    throw cause
  }
}
export async function completeExternalConfirmation(
  input: Pick<
    ApiSchemas['ExternalCompleteRequest'],
    'username' | 'display_name' | 'email' | 'initial_password'
  > = {},
): Promise<ApiSchemas['ExternalCompleteResponse']> {
  const flow = requireFlow()
  if (flow.stage !== 'confirming' || !flow.confirmation) throw invalid()
  flow.stage = 'completing'
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
    clearExternalFlow()
    return data
  } catch (error) {
    // Only explicit unconsumed input and username conflicts may resume this confirmation.
    if (ownsFlow(flow)) {
      if (canRetryExternalConfirmation(error) && matches(flow) && isSessionCurrent(flow.context))
        flow.stage = 'confirming'
      else clearExternalFlow()
    }
    throw error
  }
}
export async function cancelExternalFlow(): Promise<void> {
  const flow = requireFlow()
  if (flow.stage === 'completing' || flow.stage === 'exchanging') {
    clearExternalFlow()
    return
  }
  const transactionId = flow.transactionId
  const verifier = flow.verifier
  const client = flowClient(flow)
  clearExternalFlow()
  const { error, response } = await client.POST('/auth/external/cancel', {
    body: { transaction_id: transactionId, verifier },
  })
  if (!response.ok)
    throw buildRequestFailureError(t('authentication.cancelFailed'), error, response)
}
export function saveExternalResult(result: ApiSchemas['ExternalProofResponse']): void {
  const flow = requireFlow()
  if (
    flow.purpose !== 'reauthenticate' ||
    !flow.action ||
    !flow.targetId ||
    !isReauthenticationProof(result) ||
    flow.returnedProof?.reauthentication_proof !== result.reauthentication_proof ||
    flow.returnedProof.expires_at !== result.expires_at ||
    !deadlineIsFresh(result.expires_at)
  )
    throw invalid()
  clearExternalProof()
  const expiresAt = new Date(
    Math.min(Date.parse(flow.expiresAt), Date.parse(result.expires_at)),
  ).toISOString()
  handoff = {
    baseUrl: flow.baseUrl,
    epoch: flow.epoch,
    userId: flow.userId,
    expiresAt,
    context: flow.context,
    proof: {
      action: flow.action,
      targetId: flow.targetId,
      proof: result.reauthentication_proof,
      expiresAt,
    },
  }
  clearExternalFlow()
  proofExpiry = setTimeout(
    clearExternalProof,
    Math.min(2_147_483_647, Math.max(0, Date.parse(expiresAt) - Date.now())),
  )
}
export function takeExternalProof(): ProofResult | null {
  const result = handoff
  handoff = null
  clearTimeout(proofExpiry)
  proofExpiry = undefined
  if (!result) return null
  const valid = matches(result) && isSessionCurrent(result.context)
  const proof = valid ? { ...result.proof } : null
  result.proof.proof = ''
  return proof
}
