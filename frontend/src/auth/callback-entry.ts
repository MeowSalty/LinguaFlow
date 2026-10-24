import { CONNECTION_TEST_RELAY_KEY, relayConnectionTestTicket } from './connection-test-relay'
import {
  clearEarlyCallbackStorage,
  showCallbackFailure,
  showCallbackStatus,
} from './callback-shell'

export const CALLBACK_TIMEOUT_MS = 20_000

export async function runAuthCallback(loadApplication: () => Promise<unknown>): Promise<void> {
  const ticketWindow = window as Window & { __linguaflowAuthTicket?: string }
  let ticket = ticketWindow.__linguaflowAuthTicket
  delete ticketWindow.__linguaflowAuthTicket
  let hasRelay = false
  try {
    hasRelay = !!sessionStorage.getItem(CONNECTION_TEST_RELAY_KEY)
  } catch {
    clearEarlyCallbackStorage()
    showCallbackFailure('flowInvalid')
    return
  }
  if (hasRelay) {
    showCallbackStatus('callbackRelay')
    const relayed = relayConnectionTestTicket(ticket, (completed) => {
      if (completed) showCallbackStatus('callbackRelayed', true)
      else showCallbackFailure('callbackRelayFailed')
    })
    ticket = undefined
    clearEarlyCallbackStorage()
    if (!relayed) showCallbackFailure('callbackRelayFailed')
    return
  }
  if (!ticket) {
    clearEarlyCallbackStorage()
    showCallbackFailure('flowInvalid')
    return
  }

  const controller = new AbortController()
  let failure: 'callbackTimeout' | 'flowInvalid' | 'flowExpired' | 'callbackInterfaceFailed' =
    'callbackTimeout'
  let exchanged = false
  let discard = () => {}
  let failureMessage: (
    error: unknown,
  ) => 'callbackUnavailable' | 'flowInvalid' | 'flowExpired' | 'resultUnknown' = () =>
    'callbackUnavailable'
  let deadline = setTimeout(() => controller.abort(), CALLBACK_TIMEOUT_MS)
  const aborted = () => {
    ticket = undefined
    clearEarlyCallbackStorage()
    discard()
    showCallbackFailure(failure)
  }
  controller.signal.addEventListener('abort', aborted, { once: true })
  const invalidate = () => {
    failure = 'flowInvalid'
    controller.abort()
  }
  const changed = (event: StorageEvent) => {
    if (
      event.key !== null &&
      !['linguaflow.auth_epoch', 'linguaflow.api_base_url'].includes(event.key)
    )
      return
    if (event.storageArea && event.storageArea !== window.localStorage) return
    if (event.key === 'linguaflow.api_base_url') {
      const normalize = (value: string | null) =>
        new URL(value || '/api/v1', location.origin).href.replace(/\/+$/, '')
      try {
        if (normalize(event.newValue) === normalize(event.oldValue)) return
      } catch {}
    } else if (event.key !== null && event.newValue === event.oldValue) return
    invalidate()
  }
  window.addEventListener('storage', changed)
  window.addEventListener('pagehide', invalidate)
  try {
    const transport = await import('./callback-exchange')
    discard = transport.discardAuthCallback
    failureMessage = transport.callbackFailureMessage
    controller.signal.throwIfAborted()
    const expiresAt = await transport.completeAuthCallback(ticket, controller.signal)
    exchanged = true
    ticket = undefined
    clearTimeout(deadline)
    controller.signal.throwIfAborted()
    showCallbackStatus('callbackVerified')
    const remaining = expiresAt ? Date.parse(expiresAt) - Date.now() : 60_000
    failure = expiresAt && remaining <= 60_000 ? 'flowExpired' : 'callbackInterfaceFailed'
    deadline = setTimeout(() => controller.abort(), Math.max(0, Math.min(60_000, remaining)))
    await loadApplication()
  } catch (error) {
    discard()
    clearEarlyCallbackStorage()
    if (!controller.signal.aborted)
      showCallbackFailure(exchanged ? 'callbackInterfaceFailed' : failureMessage(error))
  } finally {
    ticket = undefined
    clearTimeout(deadline)
    window.removeEventListener('storage', changed)
    window.removeEventListener('pagehide', invalidate)
    controller.signal.removeEventListener('abort', aborted)
  }
}
