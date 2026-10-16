import { captureSession, normalizeApiBaseUrl, suspendSessionContext } from '@/api/session-context'
import { authTokenStorageKeys, getAuthEpoch } from '@/api/token-storage'

/** Token rotation keeps the page; a different login/service context resets all private views. */
export function installCrossTabSessionSync(isLocal: () => boolean): () => void {
  let epoch = getAuthEpoch()
  let scheduled = false
  function changed(event: StorageEvent) {
    if (event.storageArea && event.storageArea !== window.localStorage) return
    const serviceChanged = event.key === authTokenStorageKeys.apiBaseUrl
    if (serviceChanged) {
      try {
        if (normalizeApiBaseUrl(event.newValue ?? undefined) === captureSession().baseUrl) return
      } catch {
        /* Reload through bootstrap's invalid-address recovery. */
      }
    }
    if (!serviceChanged && event.key !== null && event.key !== authTokenStorageKeys.authEpoch)
      return
    if (!serviceChanged && isLocal()) return
    if (!serviceChanged && event.key !== null && event.newValue === epoch) return
    epoch = getAuthEpoch()
    if (scheduled) return
    scheduled = true
    // Cancel requests immediately, before the next document bootstraps its own identity.
    suspendSessionContext()
    window.location.reload()
  }
  window.addEventListener('storage', changed)
  return () => window.removeEventListener('storage', changed)
}
