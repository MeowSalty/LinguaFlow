import { type Ref, ref, watch } from 'vue'

import type { SSEEvent } from '@/composables/useJobSSE'

const STORAGE_PREFIX = 'linguaflow:sse:'
const MAX_EVENTS = 100

const getCacheKey = (jobId: number): string => `${STORAGE_PREFIX}${jobId}`

function readCache(jobId: number): SSEEvent[] {
  try {
    const raw = localStorage.getItem(getCacheKey(jobId))
    if (!raw) return []
    const parsed = JSON.parse(raw)
    return Array.isArray(parsed) ? parsed : []
  } catch {
    return []
  }
}

function writeCache(jobId: number, events: SSEEvent[]): void {
  try {
    const trimmed = events.length > MAX_EVENTS ? events.slice(-MAX_EVENTS) : events
    localStorage.setItem(getCacheKey(jobId), JSON.stringify(trimmed))
  } catch {
    // quota exceeded — silently ignore
  }
}

export function clearCache(jobId: number): void {
  try {
    localStorage.removeItem(getCacheKey(jobId))
  } catch {
    // ignore
  }
}

function eventKey(e: SSEEvent, idx: number): string {
  return `${e.type}-${e.created_at}-${idx}`
}

export function useSSEEventCache(
  jobId: Ref<number | null>,
  liveEvents: Ref<SSEEvent[]>,
): {
  cachedEvents: Ref<SSEEvent[]>
  restoreCache: () => void
  handleDrawerClose: () => void
} {
  const cachedEvents = ref<SSEEvent[]>([])
  let lastLen = -1
  let skipNext = false

  const restoreCache = (): void => {
    const id = jobId.value
    if (id == null) return
    cachedEvents.value = readCache(id)
    lastLen = -1
    skipNext = false
  }

  const handleDrawerClose = (): void => {
    skipNext = true
    cachedEvents.value = []
  }

  const makeKeySet = (events: SSEEvent[]): Set<string> => {
    const set = new Set<string>()
    events.forEach((e, i) => set.add(eventKey(e, i)))
    return set
  }

  watch(
    () => liveEvents.value.length,
    (newLen) => {
      if (skipNext) {
        skipNext = false
        lastLen = newLen
        return
      }
      if (lastLen >= 0 && newLen > lastLen) {
        const id = jobId.value
        if (id == null) return
        const added = liveEvents.value.slice(lastLen)
        const existing = makeKeySet(cachedEvents.value)
        const fresh = added.filter((e, i) => !existing.has(eventKey(e, lastLen + i)))
        if (fresh.length > 0) {
          cachedEvents.value = [...cachedEvents.value, ...fresh]
          writeCache(id, cachedEvents.value)
        }
      }
      lastLen = newLen
    },
  )

  return { cachedEvents, restoreCache, handleDrawerClose }
}
