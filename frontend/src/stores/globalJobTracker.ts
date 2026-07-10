import { defineStore } from 'pinia'
import { computed, ref, watch, onScopeDispose } from 'vue'

import { type ApiSchemas, fetchTranslationJob } from '@/api/client'
import {
  type SSEEvent,
  KNOWN_EVENT_TYPES,
  resolveStreamUrl,
} from '@/composables/sseShared'

type TranslationJob = ApiSchemas['TranslationJob']

export interface TrackedJob extends TranslationJob {
  project_name?: string
}

const STORAGE_KEY = 'linguaflow:globalTracker:jobIds'
const MAX_TRACKED_JOBS = 20
const TERMINAL_STATUSES = new Set(['completed', 'failed', 'cancelled'])

const RUNNING_POLL_INTERVAL = 3_000
const PENDING_POLL_INTERVAL = 8_000
const MAX_SSE_CONNECTIONS = 5

export const useGlobalJobTrackerStore = defineStore('globalJobTracker', () => {
  // ── 状态 ──
  const trackedJobs = ref<TrackedJob[]>([])
  const drawerJobId = ref<number | null>(null)
  const loadingJobIds = ref<Set<number>>(new Set())
  const initialized = ref(false)

  // ── 详情抽屉独立状态 ──
  const detailJob = ref<TranslationJob | null>(null)
  const loadingDetail = ref(false)

  // ── SSE 后台连接管理 ──
  const eventBuffers = ref<Map<number, SSEEvent[]>>(new Map())
  const connectionStatus = ref<Map<number, boolean>>(new Map())
  const eventSources = new Map<number, EventSource>()

  // ── Getters ──
  const activeJobs = computed(() =>
    trackedJobs.value.filter((j) => !TERMINAL_STATUSES.has(j.status)),
  )

  const hasActiveJobs = computed(() => activeJobs.value.length > 0)

  const displayJobs = computed(() => {
    const active: TrackedJob[] = []
    const terminal: TrackedJob[] = []
    for (const job of trackedJobs.value) {
      if (TERMINAL_STATUSES.has(job.status)) {
        terminal.push(job)
      } else {
        active.push(job)
      }
    }
    active.sort((a, b) => new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime())
    terminal.sort((a, b) => new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime())
    return [...active, ...terminal]
  })

  const hasTerminalJobs = computed(() =>
    trackedJobs.value.some((j) => TERMINAL_STATUSES.has(j.status)),
  )

  // ── 持久化 ──
  const persistIds = (): void => {
    const ids = trackedJobs.value.map((j) => j.id)
    try {
      localStorage.setItem(STORAGE_KEY, JSON.stringify(ids))
    } catch {
      // quota exceeded — silently ignore
    }
  }

  const loadPersistedIds = (): number[] => {
    try {
      const raw = localStorage.getItem(STORAGE_KEY)
      if (!raw) return []
      const parsed = JSON.parse(raw)
      if (!Array.isArray(parsed)) return []
      return parsed.filter((id: unknown): id is number => typeof id === 'number')
    } catch {
      return []
    }
  }

  // ── SSE 后台连接管理 ──

  const connectJobSSE = (jobId: number): void => {
    if (eventSources.has(jobId)) return

    const url = resolveStreamUrl(jobId)
    if (!url) return

    const es = new EventSource(url)

    es.onopen = () => {
      connectionStatus.value = new Map([...connectionStatus.value, [jobId, true]])
    }

    const handleEvent = (e: MessageEvent): void => {
      try {
        const data = JSON.parse(e.data) as SSEEvent
        const current = eventBuffers.value.get(jobId) ?? []
        const updated = [...current, data]
        const next = new Map(eventBuffers.value)
        next.set(jobId, updated)
        eventBuffers.value = next
      } catch {
        // ignore malformed events
      }
    }

    for (const eventType of KNOWN_EVENT_TYPES) {
      es.addEventListener(eventType, handleEvent)
    }

    es.onerror = () => {
      connectionStatus.value = new Map([...connectionStatus.value, [jobId, false]])
      if (es.readyState === EventSource.CLOSED) {
        eventSources.delete(jobId)
      }
    }

    eventSources.set(jobId, es)
    enforceConnectionLimit()
  }

  const disconnectJobSSE = (jobId: number): void => {
    const es = eventSources.get(jobId)
    if (es) {
      es.close()
      eventSources.delete(jobId)
    }
    const next = new Map(connectionStatus.value)
    next.delete(jobId)
    connectionStatus.value = next
  }

  const enforceConnectionLimit = (): void => {
    if (eventSources.size <= MAX_SSE_CONNECTIONS) return

    const activeWithSSE = trackedJobs.value
      .filter((j) => !TERMINAL_STATUSES.has(j.status) && eventSources.has(j.id))
      .sort((a, b) => {
        if (a.status === 'running' && b.status !== 'running') return -1
        if (b.status === 'running' && a.status !== 'running') return 1
        return new Date(b.updated_at).getTime() - new Date(a.updated_at).getTime()
      })

    while (eventSources.size > MAX_SSE_CONNECTIONS) {
      const lowest = activeWithSSE.pop()
      if (lowest) disconnectJobSSE(lowest.id)
      else break
    }
  }

  const clearJobEvents = (jobId: number): void => {
    const next = new Map(eventBuffers.value)
    next.delete(jobId)
    eventBuffers.value = next
  }

  // ── 单个任务刷新 ──
  const refreshJob = async (jobId: number): Promise<void> => {
    if (loadingJobIds.value.has(jobId)) return
    loadingJobIds.value = new Set([...loadingJobIds.value, jobId])

    try {
      const job = await fetchTranslationJob(jobId)
      const idx = trackedJobs.value.findIndex((j) => j.id === jobId)
      if (idx !== -1) {
        const existing = trackedJobs.value[idx]!
        trackedJobs.value[idx] = { ...job, project_name: existing.project_name }
        if (TERMINAL_STATUSES.has(job.status)) {
          disconnectJobSSE(jobId)
        }
      }
    } catch {
      // task may have been deleted server-side — remove from tracking
      trackedJobs.value = trackedJobs.value.filter((j) => j.id !== jobId)
      persistIds()
    } finally {
      const next = new Set(loadingJobIds.value)
      next.delete(jobId)
      loadingJobIds.value = next
    }
  }

  const refreshAll = async (): Promise<void> => {
    const ids = trackedJobs.value.map((j) => j.id)
    await Promise.allSettled(ids.map((id) => refreshJob(id)))
  }

  // ── Actions ──
  const trackJob = (job: TranslationJob, projectName?: string): void => {
    const existing = trackedJobs.value.find((j) => j.id === job.id)
    if (existing) {
      Object.assign(existing, job)
      if (projectName) existing.project_name = projectName
      return
    }

    if (trackedJobs.value.length >= MAX_TRACKED_JOBS) {
      // Remove the oldest terminal job to make room
      const terminalIdx = trackedJobs.value.findIndex((j) => TERMINAL_STATUSES.has(j.status))
      if (terminalIdx !== -1) {
        trackedJobs.value.splice(terminalIdx, 1)
      } else {
        // All slots occupied by active jobs — reject
        return
      }
    }

    const tracked: TrackedJob = { ...job, project_name: projectName }
    trackedJobs.value = [tracked, ...trackedJobs.value]
    persistIds()

    if (!TERMINAL_STATUSES.has(job.status)) {
      connectJobSSE(job.id)
    }
  }

  const untrackJob = (jobId: number): void => {
    trackedJobs.value = trackedJobs.value.filter((j) => j.id !== jobId)
    if (drawerJobId.value === jobId) {
      drawerJobId.value = null
    }
    disconnectJobSSE(jobId)
    clearJobEvents(jobId)
    persistIds()
  }

  const clearCompleted = (): void => {
    const removed = trackedJobs.value.filter((j) => TERMINAL_STATUSES.has(j.status))
    trackedJobs.value = trackedJobs.value.filter((j) => !TERMINAL_STATUSES.has(j.status))
    for (const job of removed) {
      disconnectJobSSE(job.id)
      clearJobEvents(job.id)
    }
    persistIds()
  }

  const openDetail = async (jobId: number): Promise<void> => {
    drawerJobId.value = jobId
    const job = trackedJobs.value.find((j) => j.id === jobId)
    if (job) {
      detailJob.value = job
    }
    // Always fetch latest detail
    void loadDetailJob(jobId)
  }

  const closeDetail = (): void => {
    drawerJobId.value = null
    detailJob.value = null
  }

  // ── 详情抽屉数据加载 ──
  const loadDetailJob = async (jobId: number): Promise<void> => {
    loadingDetail.value = true
    try {
      const job = await fetchTranslationJob(jobId)
      detailJob.value = job
      // Also update the tracked job
      const idx = trackedJobs.value.findIndex((j) => j.id === jobId)
      if (idx !== -1) {
        const existing = trackedJobs.value[idx]!
        trackedJobs.value[idx] = { ...job, project_name: existing.project_name }
      }
    } catch {
      // ignore
    } finally {
      loadingDetail.value = false
    }
  }

  const refreshDetail = async (): Promise<void> => {
    if (!drawerJobId.value) return
    await loadDetailJob(drawerJobId.value)
  }

  // ── 轮询 ──
  let pollTimer: ReturnType<typeof setInterval> | null = null
  let detailPollTimer: ReturnType<typeof setInterval> | null = null

  const clearPollTimer = (): void => {
    if (pollTimer) {
      clearInterval(pollTimer)
      pollTimer = null
    }
  }

  const clearDetailPollTimer = (): void => {
    if (detailPollTimer) {
      clearInterval(detailPollTimer)
      detailPollTimer = null
    }
  }

  const resolvePollInterval = (): number | null => {
    const jobs = trackedJobs.value
    if (jobs.length === 0) return null

    const hasRunning = jobs.some((j) => j.status === 'running')
    if (hasRunning) return RUNNING_POLL_INTERVAL

    const hasPending = jobs.some((j) => j.status === 'pending')
    if (hasPending) return PENDING_POLL_INTERVAL

    return null
  }

  const startPolling = (): void => {
    clearPollTimer()
    const interval = resolvePollInterval()
    if (interval == null) return

    pollTimer = setInterval(() => {
      if (document.hidden) return

      const newInterval = resolvePollInterval()
      if (newInterval == null) {
        clearPollTimer()
        return
      }

      // Poll active jobs
      const active = trackedJobs.value.filter((j) => !TERMINAL_STATUSES.has(j.status))
      for (const job of active) {
        void refreshJob(job.id)
      }
    }, interval)
  }

  const startDetailPolling = (): void => {
    clearDetailPollTimer()
    if (!drawerJobId.value) return

    const job = trackedJobs.value.find((j) => j.id === drawerJobId.value)
    if (!job || TERMINAL_STATUSES.has(job.status)) return

    detailPollTimer = setInterval(() => {
      if (document.hidden) return
      if (!drawerJobId.value) {
        clearDetailPollTimer()
        return
      }
      const current = trackedJobs.value.find((j) => j.id === drawerJobId.value)
      if (!current || TERMINAL_STATUSES.has(current.status)) {
        clearDetailPollTimer()
        return
      }
      void loadDetailJob(drawerJobId.value)
    }, 10_000)
  }

  // ── 监听活跃任务变化，自动启停轮询 ──
  watch(hasActiveJobs, (active) => {
    if (active) {
      startPolling()
    } else {
      clearPollTimer()
    }
  })

  // ── 监听 drawerJobId 变化，启停详情轮询 ──
  watch(drawerJobId, (id) => {
    if (id != null) {
      startDetailPolling()
    } else {
      clearDetailPollTimer()
    }
  })

  // ── 页面可见性 ──
  const handleVisibility = (): void => {
    if (document.hidden) return

    // Tab became visible: refresh all active jobs immediately
    const active = trackedJobs.value.filter((j) => !TERMINAL_STATUSES.has(j.status))
    for (const job of active) {
      void refreshJob(job.id)
    }

    // Refresh detail if open
    if (drawerJobId.value) {
      void loadDetailJob(drawerJobId.value)
    }

    // Restart polling if needed
    if (hasActiveJobs.value && !pollTimer) {
      startPolling()
    }
  }

  // ── 初始化：恢复持久化的任务 ──
  const initialize = async (): Promise<void> => {
    if (initialized.value) return
    initialized.value = true

    const ids = loadPersistedIds()
    if (ids.length === 0) return

    // Fetch all persisted jobs
    const results = await Promise.allSettled(ids.map((id) => fetchTranslationJob(id)))
    const jobs: TrackedJob[] = []
    for (const result of results) {
      if (result.status === 'fulfilled') {
        jobs.push(result.value)
      }
    }

    trackedJobs.value = jobs
    persistIds()

    // Reconnect for active jobs (backend will replay all historical events)
    for (const job of jobs) {
      if (!TERMINAL_STATUSES.has(job.status)) {
        connectJobSSE(job.id)
      }
    }

    if (hasActiveJobs.value) {
      startPolling()
    }
  }

  // ── 页面可见性监听（非组件生命周期，直接挂载） ──
  if (typeof document !== 'undefined') {
    document.addEventListener('visibilitychange', handleVisibility)
  }

  // ── 清理 ──
  onScopeDispose(() => {
    clearPollTimer()
    clearDetailPollTimer()
    for (const [, es] of eventSources) {
      es.close()
    }
    eventSources.clear()
    if (typeof document !== 'undefined') {
      document.removeEventListener('visibilitychange', handleVisibility)
    }
  })

  return {
    // 状态
    trackedJobs,
    drawerJobId,
    detailJob,
    loadingDetail,
    initialized,
    eventBuffers,
    connectionStatus,
    // Getters
    activeJobs,
    hasActiveJobs,
    displayJobs,
    hasTerminalJobs,
    // Actions
    trackJob,
    untrackJob,
    clearCompleted,
    refreshJob,
    refreshAll,
    openDetail,
    closeDetail,
    refreshDetail,
    initialize,
    getJobEvents: (jobId: number): SSEEvent[] => eventBuffers.value.get(jobId) ?? [],
    isJobSSEConnected: (jobId: number): boolean => connectionStatus.value.get(jobId) ?? false,
    clearJobEvents,
  }
})
