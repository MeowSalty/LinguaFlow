import { shallowRef } from 'vue'

export const normalizeApiBaseUrl = (value = '/api/v1'): string =>
  new URL(
    value.trim() || '/api/v1',
    typeof location === 'undefined' ? 'http://localhost' : location.origin,
  ).href.replace(/\/+$/, '')

let baseUrl = normalizeApiBaseUrl()
let userId: number | null = null
let authGeneration = 0
let controller = new AbortController()
const listeners = new Set<() => void>()
export const sessionGeneration = shallowRef(0)
export const captureSession = () => ({
  generation: sessionGeneration.value,
  authGeneration,
  baseUrl,
  userId,
  signal: controller.signal,
})
export type SessionSnapshot = ReturnType<typeof captureSession>
export const isSessionCurrent = (snapshot: SessionSnapshot): boolean =>
  snapshot.generation === sessionGeneration.value && !snapshot.signal.aborted
export const isAuthSessionCurrent = (snapshot: SessionSnapshot): boolean =>
  snapshot.authGeneration === authGeneration
export class StaleSessionError extends Error {
  constructor() {
    super('Session changed')
    this.name = 'StaleSessionError'
  }
}
export const assertSessionCurrent = (snapshot: SessionSnapshot): void => {
  if (!isSessionCurrent(snapshot)) throw new StaleSessionError()
}
export const getSessionScope = (): string | null => (userId == null ? null : `${baseUrl}|${userId}`)
export const getSessionUserId = (): number | null => userId
export const onSessionChange = (callback: () => void): (() => void) => {
  listeners.add(callback)
  return () => listeners.delete(callback)
}
export const changeSessionContext = (
  url: string,
  id: number | null,
  force = false,
  preserveAuth = false,
): void => {
  const normalized = normalizeApiBaseUrl(url)
  if (!force && normalized === baseUrl && userId === id) return
  if (!preserveAuth || normalized !== baseUrl || userId !== id) authGeneration++
  controller.abort()
  controller = new AbortController()
  baseUrl = normalized
  userId = id
  sessionGeneration.value++
  const callbacks = Array.from(listeners)
  for (const callback of callbacks) if (listeners.has(callback)) callback()
}
export const invalidateSessionViews = (): void => changeSessionContext(baseUrl, userId, true, true)
