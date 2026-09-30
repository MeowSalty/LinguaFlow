import { computed, onScopeDispose, ref, shallowRef } from 'vue'
import { defineStore } from 'pinia'
import { fetchJob, getGlossarySyncTaskStatus, type ApiSchemas } from '@/api/client'
import {
  fetchOperationsSummary,
  listOperations,
  type Operation,
  type OperationsQuery,
  type OperationsSummary,
  type SummaryQuery,
} from '@/api/operations'
import {
  captureSession,
  isSessionCurrent,
  getSessionScope,
  StaleSessionError,
} from '@/api/session-context'
import { isAccessDenied } from '@/api/utils'
import {
  isTerminalOperation,
  operationKey,
  queryKey,
  safeTaskNumber,
  summaryQuery,
  type OperationLocator,
} from '@/utils/operationQuery'
import { t } from '@/i18n'

type TaskDetail = ApiSchemas['Job'] | ApiSchemas['GlossarySyncTaskStatusResponse']
type Subscription = {
  locator: OperationLocator
  receive: (detail: TaskDetail) => void
  fail: (error: unknown) => void
  active: boolean
  controller: AbortController
}
type TaskFlight = {
  promise: Promise<TaskDetail>
  controller: AbortController
  consumers: number
  settled: boolean
}

export const useOperationsStore = defineStore('operations', () => {
  let dataGeneration = 0
  let requestController = new AbortController()
  const captureOperations = () => ({ ...captureSession(), dataGeneration })
  const isCurrent = (context: ReturnType<typeof captureOperations>) =>
    !disposed && isSessionCurrent(context) && context.dataGeneration === dataGeneration
  const assertCurrent = (context: ReturnType<typeof captureOperations>) => {
    if (!isCurrent(context)) throw new StaleSessionError()
  }
  const active = shallowRef<Operation[]>([])
  const terminal = shallowRef<Operation[]>([])
  const summary = shallowRef<OperationsSummary | null>(null)
  const summaryLoading = ref(false),
    summaryError = ref<string | null>(null),
    summaryUpdatedAt = ref<number | null>(null)
  const discoveryError = ref<string | null>(null),
    discoveryComplete = ref(false),
    initialized = ref(false)
  const revision = ref(0)
  const items = shallowRef<Operation[]>([]),
    nextCursor = ref<string | undefined>()
  const listLoading = ref(false),
    listError = ref<string | null>(null),
    listUpdatedAt = ref<number | null>(null)
  const filters = shallowRef<OperationsQuery>({ state: 'active' })
  const filteredSummary = shallowRef<OperationsSummary | null>(null)
  const filteredSummaryError = ref<string | null>(null)
  const queryFlights = new Map<string, Promise<ApiSchemas['OperationListResponse']>>()
  const queryCache = new Map<string, { at: number; data: ApiSchemas['OperationListResponse'] }>()
  const summaryFlights = new Map<string, Promise<OperationsSummary>>()
  const summaryCache = new Map<string, { at: number; data: OperationsSummary }>()
  const taskFlights = new Map<string, TaskFlight>()
  const subscriptions = new Set<Subscription>()
  let timer: ReturnType<typeof setTimeout> | null = null
  let running: Promise<void> | null = null
  let discoveryFlight: Promise<void> | null = null
  let started = false,
    disposed = false,
    failures = 0,
    listVersion = 0,
    listUsers = 0
  let listExpanded = false
  const discardRequests = (): void => {
    dataGeneration++
    requestController.abort()
    requestController = new AbortController()
    queryFlights.clear()
    summaryFlights.clear()
    taskFlights.clear()
    queryCache.clear()
    summaryCache.clear()
    discoveryFlight = null
    running = null
    listVersion++
    listLoading.value = false
    summaryLoading.value = false
  }
  const allDiscovered = computed(() => [...active.value, ...terminal.value])

  const queryOperations = (
    query: OperationsQuery = {},
    force = false,
  ): Promise<ApiSchemas['OperationListResponse']> => {
    const key = queryKey(query)
    const context = captureOperations()
    const pending = queryFlights.get(key)
    if (pending) return pending
    const cached = queryCache.get(key)
    if (!force && cached && Date.now() - cached.at < 10000) return Promise.resolve(cached.data)
    const flight = listOperations(query, { signal: requestController.signal })
      .then((data) => {
        assertCurrent(context)
        queryCache.set(key, { at: Date.now(), data })
        if (queryCache.size > 100) queryCache.delete(queryCache.keys().next().value!)
        return data
      })
      .finally(() => {
        if (queryFlights.get(key) === flight) queryFlights.delete(key)
      })
    queryFlights.set(key, flight)
    return flight
  }
  const querySummary = (query: SummaryQuery = {}, force = false): Promise<OperationsSummary> => {
    const key = queryKey(query),
      context = captureOperations(),
      pending = summaryFlights.get(key)
    if (pending) return pending
    const cached = summaryCache.get(key)
    if (!force && cached && Date.now() - cached.at < 10000) return Promise.resolve(cached.data)
    const flight = fetchOperationsSummary(query, { signal: requestController.signal })
      .then((data) => {
        assertCurrent(context)
        summaryCache.set(key, { at: Date.now(), data })
        return data
      })
      .finally(() => {
        if (summaryFlights.get(key) === flight) summaryFlights.delete(key)
      })
    summaryFlights.set(key, flight)
    return flight
  }
  const ensureSummary = async (force = false): Promise<OperationsSummary | null> => {
    const context = captureOperations()
    summaryLoading.value = true
    try {
      const data = await querySummary({}, force)
      assertCurrent(context)
      summary.value = data
      summaryError.value = null
      summaryUpdatedAt.value = Date.now()
      return data
    } catch (error) {
      if (isCurrent(context)) {
        summaryError.value = error instanceof Error ? error.message : t('operations.loadFailed')
        if (isAccessDenied(error)) summary.value = null
      }
      return null
    } finally {
      if (isCurrent(context)) summaryLoading.value = false
    }
  }
  const queryTask = (
    locator: OperationLocator,
    consumerSignal?: AbortSignal,
  ): Promise<TaskDetail> => {
    if (consumerSignal?.aborted) return Promise.reject(consumerSignal.reason)
    const consumerContext = captureOperations()
    // Translation IDs are globally unique; project matching is a per-consumer constraint.
    const key =
      locator.task_type === 'translation'
        ? operationKey(locator)
        : `${operationKey(locator)}:${locator.project_id ?? ''}`
    let shared = taskFlights.get(key)
    if (!shared) {
      const context = captureOperations()
      const controller = new AbortController()
      const signal = AbortSignal.any([controller.signal, requestController.signal])
      const flight: TaskFlight = {
        controller,
        consumers: 0,
        settled: false,
        promise: Promise.resolve()
          .then<TaskDetail>(() => {
            signal.throwIfAborted()
            return locator.task_type === 'translation'
              ? fetchJob(safeTaskNumber(locator.task_id), undefined, { signal })
              : getGlossarySyncTaskStatus(locator.project_id!, locator.task_id, undefined, {
                  signal,
                })
          })
          .then((data) => {
            assertCurrent(context)
            signal.throwIfAborted()
            return data
          })
          .finally(() => {
            flight.settled = true
            if (taskFlights.get(key) === flight) taskFlights.delete(key)
          }),
      }
      taskFlights.set(key, flight)
      shared = flight
    }
    const flight = shared
    flight.consumers++
    const result = new Promise<TaskDetail>((resolve, reject) => {
      let released = false
      const release = () => {
        if (released) return
        released = true
        consumerSignal?.removeEventListener('abort', abort)
        flight.consumers--
        if (!flight.consumers && !flight.settled) {
          flight.controller.abort()
          if (taskFlights.get(key) === flight) taskFlights.delete(key)
        }
      }
      const abort = () => {
        release()
        reject(consumerSignal?.reason ?? new DOMException('Request cancelled', 'AbortError'))
      }
      consumerSignal?.addEventListener('abort', abort, { once: true })
      flight.promise.then(
        (data) => {
          release()
          resolve(data)
        },
        (error) => {
          release()
          reject(error)
        },
      )
    })
    return result.then((data) => {
      assertCurrent(consumerContext)
      if (
        locator.task_type === 'translation' &&
        locator.project_id != null &&
        'project_id' in data &&
        data.project_id !== locator.project_id
      )
        throw new Error(t('operations.inaccessible'))
      return data
    })
  }
  const queryTranslation = (id: string, signal?: AbortSignal): Promise<ApiSchemas['Job']> =>
    queryTask({ task_type: 'translation', task_id: id }, signal) as Promise<ApiSchemas['Job']>
  const querySync = (
    projectId: number,
    id: string,
    signal?: AbortSignal,
  ): Promise<ApiSchemas['GlossarySyncTaskStatusResponse']> =>
    queryTask(
      { task_type: 'glossary_sync', task_id: id, project_id: projectId },
      signal,
    ) as Promise<ApiSchemas['GlossarySyncTaskStatusResponse']>
  const removeProject = (projectId: number): void => {
    discardRequests()
    active.value = active.value.filter((item) => item.project_id !== projectId)
    terminal.value = terminal.value.filter((item) => item.project_id !== projectId)
    items.value = items.value.filter((item) => item.project_id !== projectId)
    queryCache.clear()
    summaryCache.clear()
    summary.value = null
    filteredSummary.value = null
    schedule()
  }
  const forget = (locator: OperationLocator): void => {
    discardRequests()
    const key = operationKey(locator)
    active.value = active.value.filter((item) => operationKey(item) !== key)
    terminal.value = terminal.value.filter((item) => operationKey(item) !== key)
    items.value = items.value.filter((item) => operationKey(item) !== key)
    queryCache.clear()
    summaryCache.clear()
    schedule()
  }
  const discover = (): Promise<void> => {
    if (discoveryFlight) return discoveryFlight
    const context = captureOperations()
    const flight = (async () => {
      const found = new Map<string, Operation>()
      const denied = new Set<string>()
      let cursor: string | undefined
      const cursors = new Set<string>()
      discoveryComplete.value = false
      try {
        do {
          const page = await queryOperations({ state: 'active', limit: 100, cursor }, true)
          assertCurrent(context)
          for (const item of page.items) found.set(operationKey(item), item)
          cursor = page.next_cursor
          if (cursor && cursors.has(cursor)) throw new Error(t('operations.incomplete'))
          if (cursor) cursors.add(cursor)
        } while (cursor)
        // Cursor pagination is not a snapshot. Confirm every disappearance explicitly.
        for (const old of active.value) {
          if (found.has(operationKey(old))) continue
          try {
            const detail = await queryTask(old)
            assertCurrent(context)
            const updated = { ...old, status: detail.status } as Operation
            if (isTerminalOperation(detail.status))
              terminal.value = [
                updated,
                ...terminal.value.filter((item) => operationKey(item) !== operationKey(old)),
              ].slice(0, 20)
            else found.set(operationKey(old), updated)
          } catch (error) {
            assertCurrent(context)
            if (isAccessDenied(error)) denied.add(operationKey(old))
            else found.set(operationKey(old), old)
          }
        }
        if (denied.size) {
          // Fence off older list/history flights before removing rows already known to be denied.
          discardRequests()
          items.value = items.value.filter((item) => !denied.has(operationKey(item)))
          terminal.value = terminal.value.filter((item) => !denied.has(operationKey(item)))
          summary.value = null
          filteredSummary.value = null
          initialized.value = true
        }
        active.value = [...found.values()]
        terminal.value = terminal.value.filter((item) => !found.has(operationKey(item)))
        discoveryComplete.value = true
        discoveryError.value = null
        failures = 0
        if (denied.size) schedule()
      } catch (error) {
        if (!isCurrent(context)) return
        failures++
        discoveryError.value = error instanceof Error ? error.message : t('operations.loadFailed')
        if (isAccessDenied(error)) {
          active.value = []
          terminal.value = []
          queryCache.clear()
        } else {
          for (const old of active.value)
            if (!found.has(operationKey(old))) found.set(operationKey(old), old)
          active.value = [...found.values()]
        }
      } finally {
        if (isCurrent(context)) initialized.value = true
      }
    })().finally(() => {
      if (discoveryFlight === flight) discoveryFlight = null
    })
    discoveryFlight = flight
    return flight
  }
  const loadTerminal = async (): Promise<void> => {
    const context = captureOperations()
    try {
      const data = await queryOperations({ state: 'terminal', limit: 20 }, true)
      assertCurrent(context)
      const activeKeys = new Set(active.value.map(operationKey))
      terminal.value = data.items.filter((item) => !activeKeys.has(operationKey(item)))
    } catch (error) {
      if (isCurrent(context) && isAccessDenied(error)) terminal.value = []
    }
  }
  const refreshFilteredSummary = async (): Promise<void> => {
    const version = listVersion,
      context = captureOperations()
    try {
      const data = await querySummary(summaryQuery(filters.value))
      if (version !== listVersion || !isCurrent(context)) return
      filteredSummary.value = data
      filteredSummaryError.value = null
    } catch (error) {
      if (version !== listVersion || !isCurrent(context)) return
      filteredSummaryError.value =
        error instanceof Error ? error.message : t('operations.loadFailed')
      if (isAccessDenied(error)) filteredSummary.value = null
    }
  }
  const loadList = async (more = false): Promise<void> => {
    if (more && (listLoading.value || !nextCursor.value)) return
    const version = more ? listVersion : ++listVersion,
      context = captureOperations()
    listLoading.value = true
    listError.value = null
    listExpanded = more
    if (!more) nextCursor.value = undefined
    try {
      const page = await queryOperations(
        { ...filters.value, limit: 50, cursor: more ? nextCursor.value : undefined },
        !more,
      )
      if (version !== listVersion || !isCurrent(context)) return
      const unique = new Map((more ? items.value : []).map((item) => [operationKey(item), item]))
      for (const item of page.items) unique.set(operationKey(item), item)
      items.value = [...unique.values()]
      nextCursor.value = page.next_cursor
      listUpdatedAt.value = Date.now()
    } catch (error) {
      if (version !== listVersion || !isCurrent(context)) return
      listError.value = error instanceof Error ? error.message : t('operations.loadFailed')
      if (isAccessDenied(error)) items.value = []
    } finally {
      if (version === listVersion && isCurrent(context)) listLoading.value = false
    }
  }
  const setFilters = async (value: OperationsQuery): Promise<void> => {
    if (queryKey(value) !== queryKey(filters.value)) {
      ++listVersion
      items.value = []
      filteredSummary.value = null
      nextCursor.value = undefined
      listUpdatedAt.value = null
    }
    filters.value = value
    await Promise.all([loadList(), refreshFilteredSummary()])
  }
  const pollSubscription = async (subscription: Subscription): Promise<void> => {
    const context = captureOperations()
    try {
      const data = await queryTask(subscription.locator, subscription.controller.signal)
      if (!subscriptions.has(subscription) || !isCurrent(context)) return
      subscription.active = !isTerminalOperation(data.status)
      subscription.receive(data)
    } catch (error) {
      if (!subscriptions.has(subscription) || !isCurrent(context)) return
      if (isAccessDenied(error)) {
        subscription.active = false
        forget(subscription.locator)
      }
      subscription.fail(error)
    }
  }
  const subscribeTask = (
    locator: OperationLocator,
    receive: Subscription['receive'],
    fail: Subscription['fail'],
  ): (() => void) => {
    const subscription = { locator, receive, fail, active: true, controller: new AbortController() }
    subscriptions.add(subscription)
    void pollSubscription(subscription)
    return () => {
      subscriptions.delete(subscription)
      subscription.controller.abort()
    }
  }
  const interval = (): number =>
    failures
      ? Math.min(10000, 3000 * 2 ** failures)
      : active.value.some((item) => item.status === 'running')
        ? 3000
        : active.value.some((item) => item.status === 'pending')
          ? 5000
          : 10000
  const schedule = (): void => {
    if (timer) clearTimeout(timer)
    timer = null
    if (started && !disposed && !document.hidden && getSessionScope())
      timer = setTimeout(() => {
        void cycle()
      }, interval())
  }
  const cycle = (): Promise<void> => {
    if (running) return running
    if (!getSessionScope() || document.hidden || disposed) return Promise.resolve()
    const context = captureOperations()
    const flight = Promise.allSettled([
      discover(),
      ensureSummary(),
      ...[...subscriptions].filter((sub) => sub.active).map(pollSubscription),
      ...(listUsers ? [...(!listExpanded ? [loadList()] : []), refreshFilteredSummary()] : []),
    ])
      .then(() => {
        if (!isCurrent(context) || !listUsers || !listExpanded) return
        // Preserve loaded historical pages/cursor while updating rows already present.
        const fresh = new Map(allDiscovered.value.map((item) => [operationKey(item), item]))
        items.value = items.value.map((item) => fresh.get(operationKey(item)) ?? item)
      })
      .finally(() => {
        if (running === flight) running = null
        if (isCurrent(context)) schedule()
      })
    running = flight
    return flight
  }
  const refresh = async (): Promise<void> => {
    await Promise.all([
      cycle(),
      ensureSummary(true),
      loadTerminal(),
      ...(listUsers ? [loadList(), refreshFilteredSummary()] : []),
    ])
  }
  const invalidate = async (): Promise<void> => {
    discardRequests()
    revision.value++
    await refresh()
    await Promise.all([...subscriptions].map(pollSubscription))
  }
  const start = (): void => {
    if (started || disposed) return
    started = true
    void refresh()
  }
  const stop = (): void => {
    started = false
    if (timer) clearTimeout(timer)
    timer = null
  }
  const attachList = (): (() => void) => {
    listUsers++
    return () => {
      listUsers--
    }
  }
  const visibility = (): void => {
    if (document.hidden) {
      if (timer) clearTimeout(timer)
      timer = null
    } else if (started) void refresh()
  }
  if (typeof document !== 'undefined') document.addEventListener('visibilitychange', visibility)
  onScopeDispose(() => {
    disposed = true
    stop()
    requestController.abort()
    for (const subscription of subscriptions) subscription.controller.abort()
    subscriptions.clear()
    if (typeof document !== 'undefined')
      document.removeEventListener('visibilitychange', visibility)
  })
  return {
    active,
    terminal,
    allDiscovered,
    summary,
    summaryLoading,
    summaryError,
    summaryUpdatedAt,
    discoveryError,
    discoveryComplete,
    initialized,
    revision,
    items,
    nextCursor,
    listLoading,
    listError,
    listUpdatedAt,
    filteredSummary,
    filteredSummaryError,
    filters,
    queryOperations,
    querySummary,
    ensureSummary,
    queryTask,
    queryTranslation,
    querySync,
    subscribeTask,
    discover,
    loadList,
    setFilters,
    attachList,
    refresh,
    invalidate,
    removeProject,
    forget,
    start,
    stop,
  }
})
