import { apiClient, type ApiSchemas } from '@/api/client-core'
import {
  AuthConnectionError,
  type AuthConnection,
  type AuthConnectionChanges,
} from '@/api/auth-connections'
import {
  captureSession,
  isSessionCurrent,
  onSessionChange,
  type SessionSnapshot,
} from '@/api/session-context'
import { getAuthEpoch } from '@/api/token-storage'
import { t } from '@/i18n'
import {
  clone,
  connectionTestCandidate,
  stableJson,
} from '@/components/admin/authentication/auth-connection-model'
import { createBrowserChallenge, EXTERNAL_FLOW_KEY } from './external-browser'

export const CONNECTION_TEST_RELAY_KEY = 'linguaflow.connection-test-relay.v1'
interface RelayMarker {
  channel: string
  transactionId: string
  expiresAt: string
}
export interface ConnectionTestEvidence {
  result: ApiSchemas['ExternalTestResponse']
  revision: number
  expiresAt: string
  verifier: string
  context: SessionSnapshot
  epoch: string
  candidate: ReturnType<typeof connectionTestCandidate>
  cancelled: boolean
  applied: boolean
}
export interface ConnectionTestHandle {
  result: Promise<ConnectionTestEvidence>
  cancel: () => Promise<void>
}
const failure = (key = 'testInvalid') => new Error(t(`authAdmin.${key}`))
const validDeadline = (value: string) =>
  Number.isFinite(Date.parse(value)) && Date.parse(value) > Date.now()
const contextMatches = (context: SessionSnapshot, epoch: string) =>
  isSessionCurrent(context) &&
  getAuthEpoch() === epoch &&
  captureSession().userId === context.userId

/** Opens synchronously within the click gesture. Candidate parameters never enter storage. */
export function startConnectionTest(
  record: AuthConnection,
  changes: AuthConnectionChanges = {},
): ConnectionTestHandle {
  record = clone(record)
  const context = captureSession()
  const epoch = getAuthEpoch()
  if (
    context.userId === null ||
    record.kind === 'password' ||
    record.archived ||
    !['configuring', 'enabled'].includes(record.state ?? '')
  )
    throw failure('testUnavailable')
  if (!globalThis.crypto?.subtle || typeof BroadcastChannel === 'undefined')
    throw failure('testUnavailable')
  const channelName = `linguaflow-auth-test-${crypto.randomUUID()}`
  const popup = window.open('about:blank', channelName, 'popup,width=560,height=760')
  if (!popup) throw failure('popupBlocked')
  let channel!: BroadcastChannel
  try {
    channel = new BroadcastChannel(channelName)
    popup.opener = null
    popup.sessionStorage.removeItem(EXTERNAL_FLOW_KEY)
    popup.sessionStorage.removeItem('linguaflow.external-result.v1')
  } catch {
    channel?.close()
    popup.close()
    throw failure('testUnavailable')
  }
  const candidateChanges = clone(changes)
  let transactionId = ''
  let verifier = ''
  let expiresAt = new Date(Date.now() + 600_000).toISOString()
  let settled = false
  let cancelled = false
  let claimed = false
  let startInFlight = false
  let evidence: ConnectionTestEvidence | null = null
  let resolveResult!: (result: ConnectionTestEvidence) => void
  let rejectResult!: (error: unknown) => void
  const result = new Promise<ConnectionTestEvidence>((resolve, reject) => {
    resolveResult = resolve
    rejectResult = reject
  })
  let stopSession = () => {}
  const monitor = setInterval(() => {
    if (!contextMatches(context, epoch)) void cancel('testInvalid')
    else if (!validDeadline(expiresAt)) void cancel('testExpired')
    else if (!settled && popup.closed) void cancel('testCancelled')
  }, 500)
  const closeRelay = () => {
    try {
      channel.close()
    } catch {
      /* Cleanup is idempotent. */
    }
    try {
      popup.close()
    } catch {
      /* The provider may already have closed its window. */
    }
  }
  const cleanup = () => {
    clearInterval(monitor)
    stopSession()
    closeRelay()
  }
  const cancelTransaction = async () => {
    if (!transactionId || !verifier || !contextMatches(context, epoch)) return
    try {
      await apiClient.POST('/auth/external/cancel', {
        body: { transaction_id: transactionId, verifier },
      })
    } catch {
      /* Expiry remains the server's upper bound when cancellation cannot reach it. */
    }
  }
  async function cancel(key = 'testCancelled'): Promise<void> {
    if (cancelled) return
    cancelled = true
    if (evidence) {
      evidence.cancelled = true
      evidence.verifier = ''
      evidence.candidate.secret = null
    }
    if (!settled) {
      settled = true
      rejectResult(failure(key))
    }
    cleanup()
    await cancelTransaction()
    if (!startInFlight) verifier = ''
    delete candidateChanges.client_secret
  }
  stopSession = onSessionChange(() => {
    void cancel('testInvalid')
  })
  channel.onmessage = async (event: MessageEvent) => {
    const value: unknown = event.data
    if (
      settled ||
      claimed ||
      !transactionId ||
      event.origin !== location.origin ||
      !value ||
      typeof value !== 'object' ||
      !('transactionId' in value) ||
      value.transactionId !== transactionId ||
      !('type' in value)
    )
      return
    if (value.type === 'cancel') {
      await cancel()
      return
    }
    if (
      value.type !== 'ticket' ||
      !('ticket' in value) ||
      typeof value.ticket !== 'string' ||
      !value.ticket
    ) {
      await cancel('testInvalid')
      return
    }
    if (!contextMatches(context, epoch) || !validDeadline(expiresAt)) {
      await cancel('testExpired')
      return
    }
    claimed = true
    try {
      const { data, error, response } = await apiClient.POST('/auth/external/exchange', {
        body: { ticket: value.ticket, verifier },
      })
      if (cancelled || settled) return
      if (!contextMatches(context, epoch) || !validDeadline(expiresAt)) throw failure('testExpired')
      if (!data || error || !response.ok) {
        throw new AuthConnectionError(
          error && typeof error === 'object' ? error : undefined,
          response.status,
        )
      }
      if (
        data.kind !== 'test_result' ||
        data.connection_id !== record.id ||
        data.revision !== record.aggregate_revision ||
        data.test_id !== transactionId ||
        !data.expires_at ||
        !validDeadline(data.expires_at)
      )
        throw failure()
      expiresAt = new Date(
        Math.min(Date.parse(expiresAt), Date.parse(data.expires_at)),
      ).toISOString()
      evidence = {
        result: { ...data, expires_at: expiresAt },
        revision: record.aggregate_revision,
        expiresAt,
        verifier,
        context,
        epoch,
        candidate: connectionTestCandidate(record, candidateChanges),
        cancelled: false,
        applied: false,
      }
      settled = true
      try {
        channel.postMessage({ type: 'complete', transactionId })
      } catch {
        /* The child may have closed. */
      }
      closeRelay()
      delete candidateChanges.client_secret
      resolveResult(evidence)
    } catch (cause) {
      if (settled) return
      settled = true
      rejectResult(cause)
      cleanup()
      await cancelTransaction()
      verifier = ''
      delete candidateChanges.client_secret
    }
  }
  void (async () => {
    try {
      const challenge = await createBrowserChallenge()
      if (cancelled || !contextMatches(context, epoch)) return
      verifier = challenge.verifier
      startInFlight = true
      const { data, error, response } = await apiClient.POST(
        '/admin/auth-connections/{connectionId}/test',
        {
          params: { path: { connectionId: record.id } },
          body: {
            expected_revision: record.aggregate_revision,
            browser_challenge: challenge.browser_challenge,
            completion_url: new URL('/auth/complete', location.origin).href,
            return_to: '/admin/authentication',
            changes: candidateChanges,
          },
        },
      )
      if (!data || error || !response.ok) {
        throw new AuthConnectionError(
          error && typeof error === 'object' ? error : undefined,
          response.status,
        )
      }
      transactionId = data.transaction_id
      expiresAt = data.expires_at
      startInFlight = false
      if (cancelled) {
        await cancelTransaction()
        return
      }
      if (!contextMatches(context, epoch) || !validDeadline(expiresAt) || !transactionId)
        throw failure()
      const authorization = new URL(data.authorization_url)
      if (
        !['http:', 'https:'].includes(authorization.protocol) ||
        authorization.username ||
        authorization.password
      )
        throw failure()
      popup.sessionStorage.setItem(
        CONNECTION_TEST_RELAY_KEY,
        JSON.stringify({
          channel: channelName,
          transactionId,
          expiresAt,
        } satisfies RelayMarker),
      )
      popup.location.href = authorization.href
    } catch (cause) {
      if (settled) return
      settled = true
      rejectResult(cause)
      cleanup()
      await cancelTransaction()
      verifier = ''
      delete candidateChanges.client_secret
    } finally {
      startInFlight = false
      if (cancelled) {
        verifier = ''
        delete candidateChanges.client_secret
      }
    }
  })()
  return { result, cancel: () => cancel() }
}

/** The completion window relays a ticket, never a proof or a formal session. */
export function relayConnectionTestTicket(ticket?: string): boolean {
  let marker: RelayMarker | null = null
  try {
    const raw = sessionStorage.getItem(CONNECTION_TEST_RELAY_KEY)
    if (!raw) return false
    sessionStorage.removeItem(CONNECTION_TEST_RELAY_KEY)
    marker = JSON.parse(raw) as RelayMarker
  } catch {
    return false
  }
  if (
    !marker ||
    typeof marker.channel !== 'string' ||
    !marker.channel.startsWith('linguaflow-auth-test-') ||
    typeof marker.transactionId !== 'string' ||
    !marker.transactionId ||
    typeof marker.expiresAt !== 'string' ||
    !validDeadline(marker.expiresAt)
  )
    return false
  let channel: BroadcastChannel
  try {
    channel = new BroadcastChannel(marker.channel)
  } catch {
    return false
  }
  const transactionId = marker.transactionId
  const timeout = setTimeout(
    () => {
      channel.close()
    },
    Math.min(30_000, Date.parse(marker.expiresAt) - Date.now()),
  )
  channel.onmessage = (event: MessageEvent) => {
    if (
      event.origin !== location.origin ||
      event.data?.type !== 'complete' ||
      event.data?.transactionId !== transactionId
    )
      return
    clearTimeout(timeout)
    channel.close()
    window.close()
  }
  channel.postMessage({ type: 'ticket', transactionId, ticket: ticket ?? '' })
  return true
}

export function matchConnectionTest(
  evidence: ConnectionTestEvidence | null | undefined,
  record: AuthConnection,
  changes: AuthConnectionChanges = {},
  purpose: 'save' | 'enable' = 'save',
): boolean {
  return Boolean(
    evidence &&
    !evidence.cancelled &&
    (!evidence.applied || purpose === 'enable') &&
    contextMatches(evidence.context, evidence.epoch) &&
    validDeadline(evidence.expiresAt) &&
    evidence.revision === record.aggregate_revision &&
    evidence.result.connection_id === record.id &&
    !record.archived &&
    record.state !== 'disabled' &&
    stableJson(evidence.candidate) === stableJson(connectionTestCandidate(record, changes)),
  )
}

export function applyConnectionTestEvidence(
  evidence: ConnectionTestEvidence | null | undefined,
  record: AuthConnection,
): ConnectionTestEvidence | null {
  if (
    !evidence ||
    evidence.cancelled ||
    !contextMatches(evidence.context, evidence.epoch) ||
    !validDeadline(evidence.expiresAt) ||
    evidence.result.connection_id !== record.id ||
    !evidence.result.test_id ||
    record.validation.applied_test_id !== evidence.result.test_id ||
    record.state === 'disabled' ||
    record.archived
  )
    return null
  const candidate = connectionTestCandidate(record, {})
  const diagnostic = (value: ConnectionTestEvidence['candidate']) => {
    const copy = { ...clone(value), secret: undefined }
    if (copy.protocol?.scopes) copy.protocol.scopes.sort()
    return stableJson(copy)
  }
  if (
    diagnostic(evidence.candidate) !== diagnostic(candidate) ||
    evidence.applied ||
    record.aggregate_revision !== evidence.revision + 1
  )
    return null
  evidence.revision = record.aggregate_revision
  evidence.candidate = candidate
  evidence.applied = true
  return evidence
}

export function testEvidenceFields(evidence: ConnectionTestEvidence): {
  test_id: string
  verifier: string
} {
  if (
    !evidence.result.test_id ||
    evidence.cancelled ||
    !validDeadline(evidence.expiresAt) ||
    !contextMatches(evidence.context, evidence.epoch)
  )
    throw failure('testExpired')
  return { test_id: evidence.result.test_id, verifier: evidence.verifier }
}
