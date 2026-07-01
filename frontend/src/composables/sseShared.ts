import { getAccessToken, readStoredApiBaseUrl } from '@/api/token-storage'

export type SSELevel = 'info' | 'warn' | 'warning' | 'error'

export interface SSEEvent {
  type: string
  job_id: number
  level: SSELevel
  stage?: string
  message: string
  metadata?: Record<string, unknown>
  created_at: string
}

export interface BatchEventMetadata {
  segment_ids?: string[]
  segment_count: number
  backend_name: string
  status: 'success' | 'partial' | 'failed'
  duration_ms: number
  input_tokens: number
  output_tokens: number
  sent_content: string
  received_content: string
  sent_length?: number
  received_length?: number
  sent_truncated?: boolean
  received_truncated?: boolean
  used_glossary: Array<{ source: string; target: string }>
  added_glossary: Array<{ source: string; target: string }>
  error_type: string
  error_message: string
  http_status?: number
  tried_backends: string[]
  shrink_attempted: boolean
}

/** Normalize backend `warn` and legacy levels for UI components. */
export const normalizeSSELevel = (level: string): 'info' | 'warning' | 'error' => {
  switch (level) {
    case 'warning':
    case 'warn':
      return 'warning'
    case 'error':
      return 'error'
    default:
      return 'info'
  }
}

export const KNOWN_EVENT_TYPES = [
  'stage_start',
  'stage_done',
  'batch',
  'resource_started',
  'resource_completed',
  'resource_failed',
  'resource_cancelled',
  'job_started',
  'job_completed',
  'job_failed',
  'job_cancelled',
] as const

export const resolveStreamUrl = (jobId: number): string | null => {
  const token = getAccessToken()
  if (!token) return null

  const storedBase = readStoredApiBaseUrl()
  const base = (storedBase || '/api/v1').replace(/\/+$/, '')

  return `${base}/translation-jobs/${jobId}/stream?access_token=${encodeURIComponent(token)}`
}

const STORAGE_PREFIX = 'linguaflow:sse:'
const MAX_CACHED_EVENTS = 1000

export const readCachedSSEEvents = (jobId: number): SSEEvent[] => {
  try {
    const raw = localStorage.getItem(`${STORAGE_PREFIX}${jobId}`)
    if (!raw) return []
    const parsed = JSON.parse(raw)
    return Array.isArray(parsed) ? parsed : []
  } catch {
    return []
  }
}

export const persistSSEEvents = (jobId: number, events: SSEEvent[]): void => {
  const key = `${STORAGE_PREFIX}${jobId}`
  const trimmed = events.length > MAX_CACHED_EVENTS ? events.slice(-MAX_CACHED_EVENTS) : events
  try {
    localStorage.setItem(key, JSON.stringify(trimmed))
  } catch {
    let count = Math.floor(trimmed.length * 0.9)
    while (count > 0) {
      try {
        localStorage.removeItem(key)
        localStorage.setItem(key, JSON.stringify(events.slice(-count)))
        return
      } catch {
        count = Math.floor(count * 0.9)
      }
    }
  }
}

export const clearSSECacheFromStorage = (jobId: number): void => {
  try {
    localStorage.removeItem(`${STORAGE_PREFIX}${jobId}`)
  } catch {
    // ignore
  }
}
