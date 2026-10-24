export const CONNECTION_TEST_RELAY_KEY = 'linguaflow.connection-test-relay.v1'
export interface RelayMarker {
  channel: string
  transactionId: string
  expiresAt: string
}

const validDeadline = (value: string) =>
  Number.isFinite(Date.parse(value)) && Date.parse(value) > Date.now()

/** The completion window needs only Web APIs; the originating window owns verification. */
export function relayConnectionTestTicket(
  ticket?: string,
  onSettled?: (completed: boolean) => void,
): boolean {
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
    Array.isArray(marker) ||
    Object.keys(marker).some((key) => !['channel', 'transactionId', 'expiresAt'].includes(key)) ||
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
  let settled = false
  const cleanup = () => {
    if (settled) return
    settled = true
    clearTimeout(timeout)
    channel.close()
    window.removeEventListener('pagehide', cleanup)
  }
  const timeout = setTimeout(
    () => {
      cleanup()
      onSettled?.(false)
    },
    Math.min(30_000, Date.parse(marker.expiresAt) - Date.now()),
  )
  window.addEventListener('pagehide', cleanup)
  channel.onmessage = (event: MessageEvent) => {
    if (
      settled ||
      !validDeadline(marker.expiresAt) ||
      event.origin !== location.origin ||
      event.data?.type !== 'complete' ||
      event.data?.transactionId !== transactionId
    )
      return
    cleanup()
    onSettled?.(true)
    window.close()
  }
  try {
    channel.postMessage({ type: 'ticket', transactionId, ticket: ticket ?? '' })
  } catch {
    cleanup()
    return false
  }
  return true
}
