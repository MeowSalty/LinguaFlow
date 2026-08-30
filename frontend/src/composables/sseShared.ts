import { getAccessToken, readStoredApiBaseUrl } from '@/api/token-storage'

import type { ApiSchemas } from '@/api/client'

export type SSELevel = 'info' | 'warn' | 'warning' | 'error'

export interface SSEEvent {
  type: string
  job_id: number
  level: SSELevel
  stage?: string
  message: string
  metadata?: Record<string, unknown>
  created_at: string
  seq: number
}

/** REST 历史事件类型；结构上与 SSEEvent 同构，可直接作为 SSEEvent 使用。 */
export type JobEvent = ApiSchemas['JobEvent']

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
  truncated?: boolean
  repaired?: string[]
}

/** 池级事件元数据，对应后端 progress.PoolEvent。 */
export interface PoolEventMetadata {
  mode: string
  pool_index: number
  max_pools: number
  batches: number
  pending: number
  shrink_rate: number
  /** "pool_start" | "pool_advance" */
  phase: 'pool_start' | 'pool_advance'
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
  'pool',
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

  return `${base}/jobs/${jobId}/stream?access_token=${encodeURIComponent(token)}`
}
