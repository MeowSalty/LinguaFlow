import { getAccessToken, readStoredApiBaseUrl } from '@/api/token-storage'

export interface SSEEvent {
  type: string
  job_id: number
  level: 'info' | 'warning' | 'error'
  stage?: string
  message: string
  metadata?: Record<string, unknown>
  created_at: string
}

export interface BatchEventMetadata {
  batch_index: number
  segment_count: number
  backend_name: string
  duration_ms: number
  input_tokens: number
  output_tokens: number
  sent_content: string
  received_content: string
  used_glossary: Array<{ source: string; target: string }>
  added_glossary: Array<{ source: string; target: string }>
  error_type: string
  error_message: string
  tried_backends: string[]
  shrink_attempted: boolean
}

export const KNOWN_EVENT_TYPES = [
  'stage_start',
  'stage_done',
  'batch_complete',
  'batch_error',
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
  try {
    const trimmed = events.length > 200 ? events.slice(-200) : events
    localStorage.setItem(`${STORAGE_PREFIX}${jobId}`, JSON.stringify(trimmed))
  } catch {
    // quota exceeded — silently ignore
  }
}

export const clearSSECacheFromStorage = (jobId: number): void => {
  try {
    localStorage.removeItem(`${STORAGE_PREFIX}${jobId}`)
  } catch {
    // ignore
  }
}
