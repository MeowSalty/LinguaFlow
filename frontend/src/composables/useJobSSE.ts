import { ref, onUnmounted, type Ref } from 'vue'

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

const KNOWN_EVENT_TYPES = [
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

const resolveStreamUrl = (jobId: number): string | null => {
  const token = getAccessToken()
  if (!token) return null

  const storedBase = readStoredApiBaseUrl()
  const base = (storedBase || '/api/v1').replace(/\/+$/, '')

  return `${base}/translation-jobs/${jobId}/stream?access_token=${encodeURIComponent(token)}`
}

export interface UseJobSSEReturn {
  events: Ref<SSEEvent[]>
  connected: Ref<boolean>
  error: Ref<string | null>
  connect: () => void
  disconnect: () => void
  clearEvents: () => void
}

export function useJobSSE(jobId: Ref<number | null>): UseJobSSEReturn {
  const events = ref<SSEEvent[]>([])
  const connected = ref(false)
  const error = ref<string | null>(null)

  let eventSource: EventSource | null = null

  const handleEvent = (e: MessageEvent): void => {
    try {
      const data = JSON.parse(e.data) as SSEEvent
      events.value.push(data)
    } catch {
      // ignore malformed events
    }
  }

  const connect = (): void => {
    disconnect()

    const id = jobId.value
    if (id == null) return

    const url = resolveStreamUrl(id)
    if (!url) {
      error.value = 'no_token'
      return
    }

    error.value = null

    const es = new EventSource(url)

    es.onopen = () => {
      connected.value = true
      error.value = null
    }

    es.onerror = () => {
      connected.value = false
      if (es.readyState === EventSource.CLOSED) {
        error.value = 'connection_closed'
        eventSource = null
      }
    }

    for (const eventType of KNOWN_EVENT_TYPES) {
      es.addEventListener(eventType, handleEvent)
    }

    eventSource = es
  }

  const disconnect = (): void => {
    if (eventSource) {
      for (const eventType of KNOWN_EVENT_TYPES) {
        eventSource.removeEventListener(eventType, handleEvent)
      }
      eventSource.close()
      eventSource = null
    }
    connected.value = false
  }

  const clearEvents = (): void => {
    events.value = []
    error.value = null
  }

  onUnmounted(() => {
    disconnect()
  })

  return { events, connected, error, connect, disconnect, clearEvents }
}
