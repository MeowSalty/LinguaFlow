import { createApiClient, type ApiSchemas } from './client-core'
import { readStoredApiBaseUrl } from './token-storage'
import { captureSession, isSessionCurrent, normalizeApiBaseUrl } from './session-core'

export type ServiceMode = 'local' | 'server'
export type ServiceProbe = { health: ApiSchemas['HealthResponse']; mode: ServiceMode }

export const DEFAULT_SERVICE_BASE_URL = '/api/v1'
const PROBE_TIMEOUT_MS = 10_000

const isHealth = (value: unknown): value is ApiSchemas['HealthResponse'] =>
  !!value && typeof value === 'object' && typeof (value as { status?: unknown }).status === 'string'

const isMode = (value: unknown): value is { mode: ServiceMode } =>
  !!value &&
  typeof value === 'object' &&
  ((value as { mode?: unknown }).mode === 'local' ||
    (value as { mode?: unknown }).mode === 'server')

const probeSignal = (signal: AbortSignal | undefined, context: ReturnType<typeof captureSession>) =>
  AbortSignal.any([
    context.signal,
    AbortSignal.timeout(PROBE_TIMEOUT_MS),
    ...(signal ? [signal] : []),
  ])

const isCurrent = (context: ReturnType<typeof captureSession>, signal: AbortSignal): boolean =>
  !signal.aborted && isSessionCurrent(context)

const probeInSession = async (
  baseUrl: string,
  context: ReturnType<typeof captureSession>,
  signal?: AbortSignal,
  isSelectionCurrent: () => boolean = () => true,
): Promise<ServiceProbe | null> => {
  const combined = probeSignal(signal, context)
  const isProbeCurrent = () => isCurrent(context, combined) && isSelectionCurrent()
  if (!isProbeCurrent()) return null
  try {
    const client = createApiClient({ baseUrl, getAccessToken: () => null })
    const healthResponse = await client.GET('/ping', { signal: combined })
    if (!isProbeCurrent() || !healthResponse.response.ok || !isHealth(healthResponse.data))
      return null
    const modeResponse = await client.GET('/mode', { signal: combined })
    if (!isProbeCurrent() || !modeResponse.response.ok || !isMode(modeResponse.data)) return null
    return { health: healthResponse.data, mode: modeResponse.data.mode }
  } catch {
    return null
  }
}

/** Anonymous probe that rejects late results from its original session scope. */
export const probeService = (baseUrl: string, signal?: AbortSignal): Promise<ServiceProbe | null> =>
  probeInSession(baseUrl, captureSession(), signal)

/** Prefer local default service; otherwise probe the explicitly stored server once. */
export const resolveBootstrapService = async (
  signal?: AbortSignal,
): Promise<({ baseUrl: string } & ServiceProbe) | null> => {
  const context = captureSession()
  const stored = readStoredApiBaseUrl()
  const isResolutionCurrent = () =>
    !signal?.aborted && isSessionCurrent(context) && readStoredApiBaseUrl() === stored
  if (!isResolutionCurrent()) return null

  const defaultProbe = await probeInSession(
    DEFAULT_SERVICE_BASE_URL,
    context,
    signal,
    isResolutionCurrent,
  )
  if (!isResolutionCurrent()) return null
  if (defaultProbe?.mode === 'local') return { baseUrl: DEFAULT_SERVICE_BASE_URL, ...defaultProbe }

  if (!stored) return null
  if (normalizeApiBaseUrl(stored) === normalizeApiBaseUrl(DEFAULT_SERVICE_BASE_URL))
    return defaultProbe ? { baseUrl: DEFAULT_SERVICE_BASE_URL, ...defaultProbe } : null

  const storedProbe = await probeInSession(stored, context, signal, isResolutionCurrent)
  if (!isResolutionCurrent()) return null
  return storedProbe ? { baseUrl: stored, ...storedProbe } : null
}
