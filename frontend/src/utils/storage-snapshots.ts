import { captureSession, isSessionCurrent } from '@/api/session-context'

export interface StorageRefreshScope {
  organizationId?: number | null
  projectId?: number
  connectionId?: number
}
interface Consumer {
  scope?: () => StorageRefreshScope
  /** Synchronous: stop new submissions before any asynchronous read starts. */
  invalidate: () => void
  /** Reads only. Never resume uploads, commits, authorization, or task actions here. */
  refresh: () => void | Promise<unknown>
}
const consumers = new Set<Consumer>()
const pending = new Map<Consumer, ReturnType<typeof captureSession>>()
let scheduled = false
const matches = (left: StorageRefreshScope, right: StorageRefreshScope) =>
  (['organizationId', 'projectId', 'connectionId'] as const).every(
    (key) => left[key] === undefined || right[key] === undefined || left[key] === right[key],
  )

/** One event burst invalidates synchronously, then coalesces its reads into one round. */
export function invalidateStorageSnapshots(scope: StorageRefreshScope = {}) {
  const session = captureSession()
  for (const consumer of consumers) {
    if (!matches(scope, consumer.scope?.() ?? {})) continue
    if (!pending.has(consumer)) consumer.invalidate()
    pending.set(consumer, session)
  }
  if (scheduled || !pending.size) return
  scheduled = true
  setTimeout(() => {
    scheduled = false
    const batch = Array.from(pending)
    pending.clear()
    for (const [consumer, captured] of batch) {
      if (!consumers.has(consumer) || !isSessionCurrent(captured)) continue
      // Consumers expose read errors in their own state. No retry loop at this layer.
      void Promise.resolve()
        .then(() => {
          if (consumers.has(consumer) && isSessionCurrent(captured)) return consumer.refresh()
        })
        .catch(() => {})
    }
  }, 25)
}
const focus = () => invalidateStorageSnapshots()
const visible = () => {
  if (document.visibilityState === 'visible') focus()
}
export function subscribeStorageRefresh(consumer: Consumer): () => void {
  if (!consumers.size && typeof window !== 'undefined') {
    window.addEventListener('focus', focus)
    document.addEventListener('visibilitychange', visible)
  }
  consumers.add(consumer)
  return () => {
    consumers.delete(consumer)
    pending.delete(consumer)
    if (!consumers.size && typeof window !== 'undefined') {
      window.removeEventListener('focus', focus)
      document.removeEventListener('visibilitychange', visible)
    }
  }
}
