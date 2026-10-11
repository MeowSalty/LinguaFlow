import { defineStore } from 'pinia'
import { computed, onScopeDispose, ref, shallowRef } from 'vue'
import { listJobEvents, type ApiSchemas } from '@/api/client'
import { ApiError, isAccessDenied } from '@/api/utils'
import { taskHistoryErrorMessage } from '@/api/task-history'
import { captureSession, isSessionCurrent } from '@/api/session-context'
import { KNOWN_EVENT_TYPES, resolveStreamUrl, type SSEEvent } from '@/composables/sseShared'
import { useOperationsStore } from './operations'
import { t } from '@/i18n'
import { jobObservationTime, latestJobStages, mergeJobObservation } from '@/utils/jobStages'

type Job = ApiSchemas['Job']

const isValidEvent = (value: unknown): value is SSEEvent => {
  if (!value || typeof value !== 'object' || Array.isArray(value)) return false
  const event = value as Record<string, unknown>
  return (
    Number.isSafeInteger(event.job_id) &&
    Number.isSafeInteger(event.seq) &&
    Number(event.seq) > 0 &&
    typeof event.type === 'string' &&
    typeof event.level === 'string' &&
    typeof event.message === 'string' &&
    jobObservationTime(event.created_at) !== null &&
    (event.stage === undefined || typeof event.stage === 'string')
  )
}

// Compatibility adapter: discovery and polling belong to the operations coordinator.
export const useGlobalJobTrackerStore = defineStore('globalJobTracker', () => {
  const operations = useOperationsStore()
  const drawerJobId = ref<number | null>(null)
  const detailJob = shallowRef<Job | null>(null)
  const detailError = ref<string | null>(null)
  const loadingDetail = ref(false)
  const events = shallowRef<SSEEvent[]>([])
  const connected = ref(false)
  const loadingOlder = ref(false)
  const hasOlder = ref(true)
  const jobEnded = ref(false)
  const projectName = ref<string | undefined>()
  let source: EventSource | null = null
  let streamInterrupted = false
  let unsubscribe: (() => void) | null = null
  let finishOpening: (() => void) | null = null
  let historyFlight: Promise<void> | null = null
  let historyVersion = 0
  let lifecycleRefresh: ReturnType<typeof setTimeout> | null = null
  let expectedProjectId: number | undefined
  let taskError: string | null = null
  let historyError: string | null = null
  let version = 0
  let accessBlocked = false
  let disposed = false
  let historyLoaded = false
  let drawerController = new AbortController()
  const cancelDrawerRequests = (): void => {
    drawerController.abort()
    drawerController = new AbortController()
  }

  const syncError = () => {
    detailError.value = taskError ?? historyError
  }
  const finishInitialRequest = () => {
    finishOpening?.()
    finishOpening = null
  }
  const disconnect = (): void => {
    if (lifecycleRefresh !== null) clearTimeout(lifecycleRefresh)
    lifecycleRefresh = null
    source?.close()
    source = null
    connected.value = false
  }
  const insert = (incoming: SSEEvent[]): void => {
    const map = new Map(events.value.map((event) => [event.seq, event]))
    for (const event of incoming)
      if (isValidEvent(event) && event.job_id === drawerJobId.value) map.set(event.seq, event)
    const sorted = [...map.values()].sort((a, b) => a.seq - b.seq)
    if (sorted.length > 1000) hasOlder.value = true
    events.value = sorted.slice(-1000)
  }
  const clearJobEvents = (): void => {
    historyVersion++
    historyFlight = null
    loadingOlder.value = false
    events.value = []
    hasOlder.value = true
  }

  // Any denied endpoint invalidates the whole drawer, including in-flight successful reads.
  // Keep its ID so the user can explicitly retry after permissions are restored.
  const denyAccess = (forgetTask = true, cause?: unknown): void => {
    const missing = cause instanceof ApiError && cause.status === 404
    if (missing) expectedProjectId ??= detailJob.value?.project_id
    else expectedProjectId = undefined
    ++version
    cancelDrawerRequests()
    accessBlocked = true
    unsubscribe?.()
    unsubscribe = null
    disconnect()
    finishInitialRequest()
    detailJob.value = null
    events.value = []
    projectName.value = undefined
    loadingDetail.value = false
    loadingOlder.value = false
    hasOlder.value = false
    jobEnded.value = false
    historyLoaded = false
    historyFlight = null
    taskError = missing ? t('taskHistoryErrors.notFound') : t('operations.inaccessible')
    historyError = null
    syncError()
    if (forgetTask && drawerJobId.value != null)
      operations.forget({ task_type: 'translation', task_id: String(drawerJobId.value) })
  }

  const connect = (id: number): void => {
    const reconnecting = streamInterrupted
    disconnect()
    if (
      disposed ||
      accessBlocked ||
      typeof document === 'undefined' ||
      document.hidden ||
      jobEnded.value ||
      detailJob.value?.id !== id
    )
      return
    const url = resolveStreamUrl(id)
    if (!url) return
    const current = version,
      context = captureSession(),
      es = new EventSource(url)
    source = es
    const valid = () =>
      source === es && current === version && isSessionCurrent(context) && !disposed
    es.onopen = () => {
      if (!valid()) return
      connected.value = true
      streamInterrupted = false
      if (reconnecting) void operations.refresh()
    }
    const receive = (event: MessageEvent): void => {
      if (!valid()) return
      try {
        const incoming: unknown = JSON.parse(event.data)
        if (!isValidEvent(incoming) || incoming.job_id !== id) return
        insert([incoming])
        if (incoming.type === 'stage_counts') {
          const job = detailJob.value
          if (job?.id !== id || !job.progress) return
          const stages = latestJobStages(job.progress.stages, incoming.metadata?.stages)
          if (stages) detailJob.value = { ...job, progress: { ...job.progress, stages } }
        } else if (incoming.type.startsWith('job_') && lifecycleRefresh === null) {
          // Coalesce lifecycle bursts, then re-read through the shared request coordinator.
          lifecycleRefresh = setTimeout(() => {
            lifecycleRefresh = null
            if (valid()) void Promise.all([refreshDetail(), operations.refresh()])
          }, 100)
        }
      } catch {
        /* Ignore malformed events. */
      }
    }
    for (const type of KNOWN_EVENT_TYPES) es.addEventListener(type, receive)
    es.onerror = () => {
      if (valid()) {
        streamInterrupted = true
        disconnect()
      } else es.close()
      // A coordinator detail response must revalidate access before reconnecting.
    }
  }

  const loadLatestPage = (id: number): Promise<void> => {
    if (disposed || accessBlocked || id !== drawerJobId.value || detailJob.value?.id !== id)
      return Promise.resolve()
    if (historyFlight) return historyFlight
    const current = version,
      currentHistory = historyVersion,
      context = captureSession()
    const valid = () =>
      current === version &&
      currentHistory === historyVersion &&
      isSessionCurrent(context) &&
      !disposed
    const flight = (async () => {
      try {
        const page = await listJobEvents(id, { limit: 50, signal: drawerController.signal })
        if (!valid()) return
        insert(page.items as SSEEvent[])
        hasOlder.value = page.next_before_seq != null
        historyLoaded = true
        historyError = null
        syncError()
      } catch (error) {
        // Clearing visible logs must not discard evidence that this task is inaccessible.
        if (current !== version || !isSessionCurrent(context) || disposed) return
        if (isAccessDenied(error)) denyAccess(true, error)
        else if (currentHistory === historyVersion) {
          historyError = taskHistoryErrorMessage(error)
          syncError()
        }
      }
    })().finally(() => {
      if (historyFlight === flight) historyFlight = null
    })
    historyFlight = flight
    return flight
  }

  const accept = (job: Job): void => {
    if (disposed || accessBlocked) return
    if (
      job.id !== drawerJobId.value ||
      (expectedProjectId != null && job.project_id !== expectedProjectId)
    ) {
      // An inconsistent link is not evidence that the real task itself is inaccessible.
      denyAccess(false)
      return
    }
    const observed = operations.projectJobObservation(mergeJobObservation(detailJob.value, job))
    detailJob.value = observed
    taskError = null
    syncError()
    loadingDetail.value = false
    jobEnded.value = ['completed', 'failed', 'cancelled'].includes(observed.status)
    if (jobEnded.value) disconnect()
    else if (!source) connect(job.id)
    if (!historyLoaded) void loadLatestPage(job.id)
  }

  const closeDetail = (): void => {
    ++version
    cancelDrawerRequests()
    unsubscribe?.()
    unsubscribe = null
    disconnect()
    finishInitialRequest()
    drawerJobId.value = null
    detailJob.value = null
    detailError.value = null
    events.value = []
    projectName.value = undefined
    expectedProjectId = undefined
    jobEnded.value = false
    loadingDetail.value = false
    loadingOlder.value = false
    hasOlder.value = true
    taskError = null
    historyError = null
    historyLoaded = false
    historyFlight = null
    accessBlocked = false
    streamInterrupted = false
  }

  const openDetail = async (id: number, projectId?: number): Promise<void> => {
    if (disposed) return
    closeDetail()
    drawerJobId.value = id
    expectedProjectId = projectId
    loadingDetail.value = true
    const operation = operations.allDiscovered.find(
      (task) =>
        task.task_type === 'translation' &&
        task.task_id === String(id) &&
        (projectId == null || task.project_id === projectId),
    )
    projectName.value = operation?.project_name
    const current = version,
      context = captureSession()
    const valid = () => current === version && isSessionCurrent(context) && !disposed
    const initial = new Promise<void>((resolve) => {
      finishOpening = resolve
    })
    const release = operations.subscribeTask(
      { task_type: 'translation', task_id: String(id) },
      (data) => {
        if (!valid()) return
        accept(data as Job)
        finishInitialRequest()
      },
      (error) => {
        if (!valid()) return
        if (isAccessDenied(error)) denyAccess(true, error)
        else {
          loadingDetail.value = false
          taskError = error instanceof Error ? error.message : t('operations.loadFailed')
          syncError()
          finishInitialRequest()
        }
      },
      { terminalRecheckMs: 30_000 },
    )
    // A synchronous subscription callback may already have denied this drawer.
    if (valid()) unsubscribe = release
    else release()
    await initial
    if (valid() && historyFlight) await historyFlight
  }

  const refreshDetail = async (): Promise<void> => {
    const id = drawerJobId.value,
      current = version,
      context = captureSession()
    if (id == null || disposed) return
    if (accessBlocked || !unsubscribe) {
      await openDetail(id, expectedProjectId)
      return
    }
    try {
      const job = await operations.queryTranslation(String(id), drawerController.signal)
      if (current === version && isSessionCurrent(context) && !disposed) accept(job)
    } catch (error) {
      if (current !== version || !isSessionCurrent(context) || disposed) return
      if (isAccessDenied(error)) denyAccess(true, error)
      else {
        taskError = error instanceof Error ? error.message : t('operations.loadFailed')
        syncError()
      }
    } finally {
      if (current === version && isSessionCurrent(context)) loadingDetail.value = false
    }
  }

  const loadOlder = async (): Promise<void> => {
    const id = drawerJobId.value,
      current = version,
      currentHistory = historyVersion,
      context = captureSession()
    if (
      id == null ||
      !detailJob.value ||
      disposed ||
      accessBlocked ||
      loadingOlder.value ||
      !hasOlder.value
    )
      return
    loadingOlder.value = true
    try {
      const page = await listJobEvents(id, {
        beforeSeq: events.value[0]?.seq,
        limit: 50,
        signal: drawerController.signal,
      })
      if (
        current !== version ||
        currentHistory !== historyVersion ||
        !isSessionCurrent(context) ||
        disposed
      )
        return
      const map = new Map(
        [...page.items, ...events.value]
          .filter((event) => isValidEvent(event) && event.job_id === id)
          .map((event) => [event.seq, event as SSEEvent]),
      )
      events.value = [...map.values()].sort((a, b) => a.seq - b.seq)
      hasOlder.value = page.next_before_seq != null
      historyError = null
      syncError()
    } catch (error) {
      if (current !== version || !isSessionCurrent(context) || disposed) return
      if (isAccessDenied(error)) denyAccess(true, error)
      else if (currentHistory === historyVersion) {
        historyError = taskHistoryErrorMessage(error)
        syncError()
      }
    } finally {
      if (current === version && currentHistory === historyVersion && isSessionCurrent(context))
        loadingOlder.value = false
    }
  }

  const trackJob = (job: Job, name?: string): void => {
    if (disposed) return
    operations.projectTask(
      { task_type: 'translation', task_id: String(job.id), project_id: job.project_id },
      job,
    )
    if (!accessBlocked && drawerJobId.value === job.id) {
      accept(job)
      if (!accessBlocked) projectName.value = name
    }
  }
  const visibility = (): void => {
    if (document.hidden) disconnect()
    else if (drawerJobId.value != null && !accessBlocked) void refreshDetail()
  }
  if (typeof document !== 'undefined') document.addEventListener('visibilitychange', visibility)
  onScopeDispose(() => {
    disposed = true
    closeDetail()
    if (typeof document !== 'undefined')
      document.removeEventListener('visibilitychange', visibility)
  })
  return {
    drawerJobId,
    detailJob,
    detailError,
    detailProjectId: computed(() => detailJob.value?.project_id ?? expectedProjectId),
    loadingDetail,
    projectName,
    hasOlder,
    loadingOlder,
    jobEnded,
    initialized: computed(() => operations.initialized),
    trackJob,
    openDetail,
    closeDetail,
    refreshDetail,
    loadLatestPage,
    loadOlder,
    clearJobEvents,
    initialize: async () => {
      operations.start()
    },
    getJobEvents: () => events.value,
    isJobSSEConnected: () => connected.value,
    refreshJob: async () => {
      await operations.invalidate()
    },
    refreshAll: operations.refresh,
  }
})
