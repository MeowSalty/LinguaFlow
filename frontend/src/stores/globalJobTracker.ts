import { defineStore } from 'pinia'
import { computed, ref, watch, onScopeDispose } from 'vue'

import { type ApiSchemas, fetchTranslationJob } from '@/api/client'
import { clearCache as clearSSECache } from '@/composables/useSSEEventCache'

type TranslationJob = ApiSchemas['TranslationJob']

export interface TrackedJob extends TranslationJob {
  project_name?: string
}

const STORAGE_KEY = 'linguaflow:globalTracker:jobIds'
const MAX_TRACKED_JOBS = 20
const TERMINAL_STATUSES = new Set(['completed', 'failed', 'cancelled'])

const RUNNING_POLL_INTERVAL = 3_000
const PENDING_POLL_INTERVAL = 8_000

export const useGlobalJobTrackerStore = defineStore('globalJobTracker', () => {
  // ── 状态 ──
  const trackedJobs = ref<TrackedJob[]>([])
  const drawerJobId = ref<number | null>(null)
  const loadingJobIds = ref<Set<number>>(new Set())
  const initialized = ref(false)

  // ── 详情抽屉独立状态 ──
  const detailJob = ref<TranslationJob | null>(null)
  const loadingDetail = ref(false)

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
  }

  const untrackJob = (jobId: number): void => {
    trackedJobs.value = trackedJobs.value.filter((j) => j.id !== jobId)
    if (drawerJobId.value === jobId) {
      drawerJobId.value = null
    }
    clearSSECache(jobId)
    persistIds()
  }

  const clearCompleted = (): void => {
    const removed = trackedJobs.value.filter((j) => TERMINAL_STATUSES.has(j.status))
    trackedJobs.value = trackedJobs.value.filter((j) => !TERMINAL_STATUSES.has(j.status))
    for (const job of removed) {
      clearSSECache(job.id)
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
  }
})
