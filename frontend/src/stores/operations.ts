import { computed, onScopeDispose, ref, shallowRef } from 'vue'
import { defineStore } from 'pinia'
import { fetchJob, getGlossarySyncTaskStatus, type ApiSchemas } from '@/api/client'
import { getStorageTask } from '@/api/storage'
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
import { jobObservationTime } from '@/utils/jobStages'
import {
  taskHistoryKey,
  toTaskHistoryItem,
  type TaskHistoryItem,
  type TaskHistoryTarget,
} from '@/api/task-history'

export type TaskDetailMap = {
  translation: ApiSchemas['Job']
  glossary_sync: ApiSchemas['GlossarySyncTaskStatusResponse']
  storage: ApiSchemas['StorageTask']
}
type TaskType = keyof TaskDetailMap
type TaskDetail = TaskDetailMap[TaskType]
type TranslationOperation = Extract<Operation, { task_type: 'translation' }>
type TranslationObservation = Pick<
  TranslationOperation,
  'project_id' | 'status' | 'updated_at' | 'can_delete' | 'finished_at'
> & { progress?: TranslationOperation['progress'] }
const withDetailStatus = (operation: Operation, detail: TaskDetail): Operation => {
  if (operation.task_type === 'storage' && 'cleanup_status' in detail) {
    return {
      ...operation,
      status: detail.status,
      phase: detail.phase,
      cleanup_status: detail.cleanup_status,
      error_code: detail.error_code ?? '',
      next_retry_at: detail.next_retry_at ?? null,
    }
  }
  return {
    ...operation,
    status: detail.status,
    ...('updated_at' in detail ? { updated_at: detail.updated_at } : {}),
    ...('can_delete' in detail
      ? { can_delete: detail.can_delete, finished_at: detail.finished_at }
      : {}),
  } as Operation
}
type Subscription = {
  locator: OperationLocator
  receive: (detail: TaskDetail) => void
  fail: (error: unknown) => void
  active: boolean
  status?: TaskDetail['status']
  nextPollAt: number
  controller: AbortController
  terminalRecheckMs?: number
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
  const capabilityRevision = ref(0)
  const capabilities = new Map<string, { item: TaskHistoryItem; read: number }>()
  let capabilityRead = 0
  const beginCapabilityRead = () => ++capabilityRead
  const observeCapabilities = (items: TaskHistoryItem[], read = beginCapabilityRead()): void => {
    let changed = false
    for (const item of items) {
      const key = taskHistoryKey(item),
        previous = capabilities.get(key)
      if (previous && previous.read > read) continue
      capabilities.set(key, { item: { ...item }, read })
      changed = true
    }
    if (changed) capabilityRevision.value++
  }
  const getTaskCapability = (target: TaskHistoryTarget): TaskHistoryItem | undefined =>
    capabilities.get(taskHistoryKey(target))?.item
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
  const detailReads = new WeakMap<TaskDetail, number>()
  const subscriptions = new Set<Subscription>()
  // Session-scoped observations arbitrate list/detail/action races without inventing transitions.
  const translationObservations = new Map<string, { value: TranslationObservation; read: number }>()
  const observeTranslation = (
    id: string,
    incoming: TranslationObservation,
    read: number,
  ): TranslationObservation => {
    const previous = translationObservations.get(id)
    const before = jobObservationTime(previous?.value.updated_at)
    const after = jobObservationTime(incoming.updated_at)
    if (
      previous &&
      before !== null &&
      after !== null &&
      (before > after || (before === after && previous.read > read))
    )
      return previous.value
    translationObservations.set(id, { value: incoming, read })
    return incoming
  }
  const projectOperation = (item: Operation): Operation => {
    if (item.task_type !== 'translation') return item
    const latest = translationObservations.get(item.task_id)?.value
    const capability = getTaskCapability({
      kind: 'translation',
      id: item.task_id,
      project_id: item.project_id,
    })
    return { ...item, ...latest, ...(capability ? { can_delete: capability.can_delete } : {}) }
  }
  const observeOperation = (item: Operation, read: number): Operation => {
    if (item.task_type !== 'translation') return item
    const { project_id, status, updated_at, can_delete, finished_at, progress } = item
    observeTranslation(
      item.task_id,
      {
        project_id,
        status,
        updated_at,
        can_delete,
        finished_at,
        progress,
      },
      read,
    )
    return projectOperation(item)
  }
  const observeDetail = (
    locator: OperationLocator,
    detail: TaskDetail,
    read = beginCapabilityRead(),
  ): TaskDetail => {
    if (locator.task_type !== 'translation' || !('project_id' in detail)) return detail
    const job = detail as ApiSchemas['Job']
    const { project_id, status, updated_at, can_delete, finished_at } = job
    const progress = job.progress && {
      total_resources: job.progress.total_resources,
      completed_resources: job.progress.completed_resources,
      failed_resources: job.progress.failed_resources,
      progress_total: job.progress.progress_total,
      progress_completed: job.progress.progress_completed,
      queue_position: job.progress.queue_position ?? null,
      queue_size: job.progress.queue_size ?? null,
    }
    observeTranslation(
      locator.task_id,
      {
        project_id,
        status,
        updated_at,
        can_delete,
        finished_at,
        ...(progress ? { progress } : {}),
      },
      read,
    )
    return projectJobObservation(job)
  }
  // A projection is never recorded as another read: only raw REST/action responses advance observations.
  const projectJobObservation = (job: ApiSchemas['Job']): ApiSchemas['Job'] => {
    const latest = translationObservations.get(String(job.id))?.value
    const capability = getTaskCapability({
      kind: 'translation',
      id: String(job.id),
      project_id: job.project_id,
    })
    return {
      ...job,
      ...latest,
      ...(job.progress ? { progress: { ...job.progress, ...latest?.progress } } : {}),
      ...(capability ? { can_delete: capability.can_delete } : {}),
    } as ApiSchemas['Job']
  }
  const matchesState = (item: Operation, query: OperationsQuery): boolean =>
    query.status
      ? item.status === query.status
      : query.state === 'all'
        ? true
        : query.state === 'terminal'
          ? isTerminalOperation(item.status)
          : !isTerminalOperation(item.status)
  const projectRows = (locator: OperationLocator, detail: TaskDetail): void => {
    const key = operationKey(locator)
    const update = (item: Operation) =>
      operationKey(item) === key
        ? projectOperation(withDetailStatus(item, detail))
        : projectOperation(item)
    const discovered = new Map(
      [...active.value, ...terminal.value].map((item) => {
        const updated = update(item)
        return [operationKey(updated), updated] as const
      }),
    )
    active.value = [...discovered.values()].filter((item) => !isTerminalOperation(item.status))
    terminal.value = [...discovered.values()]
      .filter((item) => isTerminalOperation(item.status))
      .slice(0, 20)
    items.value = items.value.map(update).filter((item) => matchesState(item, filters.value))
  }
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
    if (!force && cached && Date.now() - cached.at < 10000)
      return Promise.resolve({
        ...cached.data,
        items: cached.data.items.map(projectOperation).filter((item) => matchesState(item, query)),
      })
    const capabilityStamp = beginCapabilityRead()
    const flight = listOperations(query, { signal: requestController.signal })
      .then((data) => {
        assertCurrent(context)
        observeCapabilities(
          data.items.flatMap((item) => {
            const target = toTaskHistoryItem(item)
            return target ? [target] : []
          }),
          capabilityStamp,
        )
        data = {
          ...data,
          items: data.items
            .map((item) => observeOperation(item, capabilityStamp))
            .filter((item) => matchesState(item, query)),
        }
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
  const requestTask = (
    locator: OperationLocator,
    consumerSignal?: AbortSignal,
  ): Promise<TaskDetail> => {
    if (consumerSignal?.aborted) return Promise.reject(consumerSignal.reason)
    // Validate before allocating a flight; storage and sync are project-scoped APIs.
    try {
      if (locator.task_type !== 'translation') safeTaskNumber(String(locator.project_id))
      if (locator.task_type !== 'glossary_sync') safeTaskNumber(locator.task_id)
    } catch (error) {
      return Promise.reject(error)
    }
    const consumerContext = captureOperations()
    // Translation IDs are globally unique; project matching is a per-consumer constraint.
    const key =
      locator.task_type === 'translation'
        ? operationKey(locator)
        : `${operationKey(locator)}:${locator.project_id ?? ''}`
    let shared = taskFlights.get(key)
    if (!shared) {
      const context = captureOperations()
      const capabilityStamp = beginCapabilityRead()
      const controller = new AbortController()
      const signal = AbortSignal.any([controller.signal, requestController.signal])
      const flight: TaskFlight = {
        controller,
        consumers: 0,
        settled: false,
        promise: Promise.resolve()
          .then<TaskDetail>(() => {
            signal.throwIfAborted()
            switch (locator.task_type) {
              case 'translation':
                return fetchJob(safeTaskNumber(locator.task_id), undefined, { signal })
              case 'glossary_sync':
                return getGlossarySyncTaskStatus(locator.project_id!, locator.task_id, undefined, {
                  signal,
                })
              case 'storage':
                return getStorageTask(locator.project_id!, safeTaskNumber(locator.task_id), {
                  signal,
                })
            }
          })
          .then((data) => {
            assertCurrent(context)
            signal.throwIfAborted()
            detailReads.set(data, capabilityStamp)
            if (locator.task_type !== 'storage' && 'can_delete' in data) {
              const projectId = 'project_id' in data ? data.project_id : locator.project_id
              if (projectId != null)
                observeCapabilities(
                  [
                    {
                      kind: locator.task_type,
                      id: locator.task_id,
                      project_id: projectId,
                      can_delete: data.can_delete,
                      status: data.status,
                    },
                  ],
                  capabilityStamp,
                )
            }
            const observed = observeDetail(locator, data, capabilityStamp)
            projectRows(locator, observed)
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
  const queryTask = <T extends TaskType>(
    locator: OperationLocator & { task_type: T },
    signal?: AbortSignal,
  ): Promise<TaskDetailMap[T]> => requestTask(locator, signal) as Promise<TaskDetailMap[T]>
  const queryTranslation = (id: string, signal?: AbortSignal): Promise<ApiSchemas['Job']> =>
    queryTask({ task_type: 'translation', task_id: id }, signal)
  const querySync = (
    projectId: number,
    id: string,
    signal?: AbortSignal,
  ): Promise<ApiSchemas['GlossarySyncTaskStatusResponse']> =>
    queryTask({ task_type: 'glossary_sync', task_id: id, project_id: projectId }, signal)
  const queryStorage = (
    projectId: number,
    id: string,
    signal?: AbortSignal,
  ): Promise<ApiSchemas['StorageTask']> =>
    queryTask({ task_type: 'storage', task_id: id, project_id: projectId }, signal)
  const projectTask = (locator: OperationLocator, detail: TaskDetail): void => {
    // Preflight and history callers may project a raw query response again after it was superseded.
    const read = detailReads.get(detail) ?? beginCapabilityRead()
    if (locator.task_type === 'translation' && 'can_delete' in detail && 'project_id' in detail)
      observeCapabilities(
        [
          {
            kind: 'translation',
            id: locator.task_id,
            project_id: detail.project_id,
            can_delete: detail.can_delete,
            status: detail.status,
          },
        ],
        read,
      )
    const observed = observeDetail(locator, detail, read)
    const key = operationKey(locator)
    projectRows(locator, observed)
    for (const subscription of subscriptions)
      if (
        operationKey(subscription.locator) === key &&
        (subscription.locator.project_id == null ||
          subscription.locator.project_id === locator.project_id)
      )
        subscription.receive(detail)
  }
  const invalidateReads = (): void => {
    discardRequests()
    nextCursor.value = undefined
    listExpanded = false
    revision.value++
  }
  const removeProject = (projectId: number): void => {
    discardRequests()
    for (const [id, state] of translationObservations)
      if (state.value.project_id === projectId) translationObservations.delete(id)
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
    if (locator.task_type === 'translation') translationObservations.delete(locator.task_id)
    const key = operationKey(locator)
    active.value = active.value.filter((item) => operationKey(item) !== key)
    terminal.value = terminal.value.filter((item) => operationKey(item) !== key)
    items.value = items.value.filter((item) => operationKey(item) !== key)
    queryCache.clear()
    summaryCache.clear()
    summary.value = null
    filteredSummary.value = null
    nextCursor.value = undefined
    revision.value++
    schedule()
  }
  // Synchronous removal barrier. The caller clears dependent stores before one public refresh.
  const invalidateTasks = (targets: TaskHistoryTarget[], publish = true): void => {
    for (const target of targets) {
      capabilities.delete(taskHistoryKey(target))
      if (target.kind === 'translation') translationObservations.delete(target.id)
    }
    const matches = (locator: OperationLocator) =>
      targets.some(
        (target) =>
          target.kind === locator.task_type &&
          target.id === locator.task_id &&
          (locator.project_id == null || target.project_id === locator.project_id),
      )
    for (const subscription of subscriptions)
      if (matches(subscription.locator)) {
        subscription.controller.abort()
        subscriptions.delete(subscription)
      }
    discardRequests()
    const keep = (item: Operation) => !matches(item)
    active.value = active.value.filter(keep)
    terminal.value = terminal.value.filter(keep)
    items.value = items.value.filter(keep)
    summary.value = null
    filteredSummary.value = null
    summaryUpdatedAt.value = null
    nextCursor.value = undefined
    listExpanded = false
    if (publish) revision.value++
    schedule()
  }
  const publishHistoryChange = (): void => {
    revision.value++
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
            const updated = withDetailStatus(old, detail)
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
          for (const key of denied)
            if (key.startsWith('translation:'))
              translationObservations.delete(key.slice('translation:'.length))
          items.value = items.value.filter((item) => !denied.has(operationKey(item)))
          terminal.value = terminal.value.filter((item) => !denied.has(operationKey(item)))
          summary.value = null
          filteredSummary.value = null
          initialized.value = true
        }
        const observed = [...found.values()].map(projectOperation)
        active.value = observed.filter((item) => !isTerminalOperation(item.status))
        const activeKeys = new Set(active.value.map(operationKey))
        terminal.value = [
          ...new Map(
            [
              ...terminal.value.map(projectOperation),
              ...observed.filter((item) => isTerminalOperation(item.status)),
            ].map((item) => [operationKey(item), item]),
          ).values(),
        ]
          .filter((item) => !activeKeys.has(operationKey(item)))
          .slice(0, 20)
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
          translationObservations.clear()
          queryCache.clear()
        } else {
          for (const old of active.value)
            if (!found.has(operationKey(old))) found.set(operationKey(old), old)
          active.value = [...found.values()]
            .map(projectOperation)
            .filter((item) => !isTerminalOperation(item.status))
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
        .map(projectOperation)
        .filter((item) => matchesState(item, filters.value))
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
    // Keep cleanup retries at the low frequency even if a detail read fails.
    if (subscription.nextPollAt > 0) subscription.nextPollAt = Date.now() + 30_000
    try {
      const response = await queryTask(subscription.locator, subscription.controller.signal)
      if (!subscriptions.has(subscription) || !isCurrent(context)) return
      const data =
        subscription.locator.task_type === 'translation'
          ? projectJobObservation(response as ApiSchemas['Job'])
          : response
      const businessTerminal = isTerminalOperation(data.status)
      subscription.status = data.status
      const pendingCleanup =
        subscription.locator.task_type === 'storage' &&
        'cleanup_status' in data &&
        ['cleanup_pending', 'running', 'blocked'].includes(data.cleanup_status)
      subscription.active = !businessTerminal || pendingCleanup || !!subscription.terminalRecheckMs
      subscription.nextPollAt =
        businessTerminal && (pendingCleanup || subscription.terminalRecheckMs)
          ? Date.now() + (subscription.terminalRecheckMs ?? 30_000)
          : 0
      projectRows(subscription.locator, data)
      subscription.receive(response)
    } catch (error) {
      if (!subscriptions.has(subscription) || !isCurrent(context)) return
      if (isAccessDenied(error)) {
        subscription.active = false
        forget(subscription.locator)
      }
      subscription.fail(error)
    }
  }
  const subscribeTask = <T extends TaskType>(
    locator: OperationLocator & { task_type: T },
    receive: (detail: TaskDetailMap[T]) => void,
    fail: Subscription['fail'],
    options?: { terminalRecheckMs?: number },
  ): (() => void) => {
    const subscription: Subscription = {
      locator,
      receive: (detail) => receive(detail as TaskDetailMap[T]),
      fail,
      active: true,
      nextPollAt: 0,
      controller: new AbortController(),
      terminalRecheckMs: options?.terminalRecheckMs,
    }
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
      : active.value.some((item) => ['running', 'pausing'].includes(item.status)) ||
          [...subscriptions].some(
            (sub) => sub.active && ['running', 'pausing'].includes(sub.status ?? ''),
          )
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
      ...[...subscriptions]
        .filter((sub) => sub.active && sub.nextPollAt <= Date.now())
        .map(pollSubscription),
      ...(listUsers ? [...(!listExpanded ? [loadList()] : []), refreshFilteredSummary()] : []),
    ])
      .then(() => {
        if (!isCurrent(context) || !listUsers || !listExpanded) return
        // Preserve loaded historical pages/cursor while updating rows already present.
        const fresh = new Map(allDiscovered.value.map((item) => [operationKey(item), item]))
        items.value = items.value
          .map((item) => projectOperation(fresh.get(operationKey(item)) ?? item))
          .filter((item) => matchesState(item, filters.value))
      })
      .finally(() => {
        if (running === flight) running = null
        if (isCurrent(context)) schedule()
      })
    running = flight
    return flight
  }
  const refresh = async (): Promise<void> => {
    summaryCache.clear()
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
    } else if (started) {
      for (const subscription of subscriptions) subscription.nextPollAt = 0
      void refresh()
    }
  }
  const reconnect = (): void => {
    if (started && !disposed && !document.hidden && getSessionScope()) void refresh()
  }
  if (typeof document !== 'undefined') document.addEventListener('visibilitychange', visibility)
  if (typeof window !== 'undefined') window.addEventListener('online', reconnect)
  onScopeDispose(() => {
    disposed = true
    stop()
    requestController.abort()
    for (const subscription of subscriptions) subscription.controller.abort()
    subscriptions.clear()
    translationObservations.clear()
    if (typeof document !== 'undefined')
      document.removeEventListener('visibilitychange', visibility)
    if (typeof window !== 'undefined') window.removeEventListener('online', reconnect)
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
    capabilityRevision,
    beginCapabilityRead,
    observeCapabilities,
    getTaskCapability,
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
    queryStorage,
    subscribeTask,
    discover,
    loadList,
    setFilters,
    attachList,
    refresh,
    invalidate,
    removeProject,
    forget,
    invalidateTasks,
    publishHistoryChange,
    projectTask,
    projectJobObservation,
    invalidateReads,
    start,
    stop,
  }
})
