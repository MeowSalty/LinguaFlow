import { computed, onScopeDispose, ref, shallowRef } from 'vue'
import { captureSession, isSessionCurrent, onSessionChange } from '@/api/session-context'
import { isAccessDenied } from '@/api/utils'
import {
  canManageOrganization,
  onOrganizationInvalidated,
  organizationRoles,
} from '@/utils/organization-scope'
import { t } from '@/i18n'

export interface ScopedEntity {
  id: number
  name: string
  scope?: string
  owner_org_id?: number
}
export function createScopedEntityState<T extends ScopedEntity, Create, Update>(api: {
  list: (orgId: number | null, signal: AbortSignal) => Promise<{ items: T[] }>
  create: (body: Create, orgId: number | null) => Promise<T>
  update: (id: number, body: Update, orgId: number | null) => Promise<T>
  remove: (id: number, orgId: number | null) => Promise<void>
}) {
  const items = shallowRef<T[]>([])
  const orgId = ref<number | null>(null)
  const loading = ref(false)
  const creating = ref(false)
  const updating = ref(false)
  const deletingIds = ref<number[]>([])
  const error = ref<string | null>(null)
  const searchQuery = ref('')
  const scopeFilter = ref('all')
  const cache = new Map<number | null, T[]>()
  let generation = 0
  let loadSequence = 0
  let controller = new AbortController()
  const abort = () => {
    controller.abort()
    controller = new AbortController()
  }
  onScopeDispose(abort)
  let inFlight: Promise<void> | null = null
  const clear = () => {
    abort()
    ++generation
    cache.clear()
    inFlight = null
    items.value = []
    orgId.value = null
    loading.value = false
    creating.value = false
    updating.value = false
    deletingIds.value = []
    error.value = null
    searchQuery.value = ''
    scopeFilter.value = 'all'
  }
  onScopeDispose(onSessionChange(clear))
  onOrganizationInvalidated((id) => {
    cache.delete(id)
    for (const [key, values] of cache)
      cache.set(
        key,
        values.filter((item) => item.owner_org_id !== id),
      )
    items.value = items.value.filter((item) => item.owner_org_id !== id)
    if (orgId.value === id || orgId.value === null) {
      abort()
      ++generation
      inFlight = null
      loading.value = false
      creating.value = false
      updating.value = false
      deletingIds.value = []
      if (orgId.value === id && !organizationRoles.value[id]) error.value = t('team.unavailable')
      if (organizationRoles.value[id])
        queueMicrotask(() => {
          void load(orgId.value)
        })
    }
  })
  const setOrganization = (next: number | null) => {
    if (next === orgId.value) return
    abort()
    ++generation
    orgId.value = next
    inFlight = null
    items.value = cache.get(next) ?? []
    error.value = null
    loading.value = false
    creating.value = false
    updating.value = false
    deletingIds.value = []
    searchQuery.value = ''
    scopeFilter.value = 'all'
  }
  // Old no-argument callers (dashboard, workspace, quick translation) retain the unfiltered scope.
  const load = (scope: number | null = null): Promise<void> => {
    setOrganization(scope)
    if (scope !== null && (!Number.isSafeInteger(scope) || scope < 1)) {
      items.value = []
      error.value = t('team.unavailable')
      return Promise.resolve()
    }
    if (inFlight) return inFlight
    const session = captureSession()
    const request = generation
    const sequence = ++loadSequence
    const current = () =>
      request === generation && sequence === loadSequence && isSessionCurrent(session)
    loading.value = true
    error.value = null
    inFlight = (async () => {
      try {
        const response = await api.list(scope, controller.signal)
        if (current()) {
          items.value = response.items
          cache.set(scope, response.items)
        }
      } catch (cause) {
        if (current()) {
          if (isAccessDenied(cause)) {
            items.value = []
            cache.delete(scope)
          }
          error.value = cause instanceof Error ? cause.message : t('team.errors.loadResource')
        }
      } finally {
        if (current()) {
          loading.value = false
          inFlight = null
        }
      }
    })()
    return inFlight
  }
  const canEdit = (item?: T): boolean => {
    if (item?.scope === 'system') return false
    const id = item?.owner_org_id ?? orgId.value
    return id == null || canManageOrganization(organizationRoles.value[id])
  }
  const mutate = async <R>(
    kind: 'create' | 'update' | 'delete',
    id: number | undefined,
    action: (scope: number | null) => Promise<R>,
  ): Promise<R> => {
    const entity = items.value.find((item) => item.id === id)
    if (!canEdit(entity)) throw new Error(t('team.errors.readOnly'))
    const scope = entity?.owner_org_id ?? orgId.value
    const session = captureSession()
    const request = generation
    const current = () => request === generation && isSessionCurrent(session)
    ++loadSequence
    abort()
    inFlight = null
    loading.value = false
    error.value = null
    if (kind === 'create') creating.value = true
    if (kind === 'update') updating.value = true
    if (kind === 'delete' && id !== undefined) deletingIds.value = [...deletingIds.value, id]
    try {
      const value = await action(scope)
      if (!current()) throw new DOMException('Stale resource context', 'AbortError')
      cache.clear()
      if (kind === 'delete') items.value = items.value.filter((item) => item.id !== id)
      else {
        const item = value as unknown as T
        items.value = [item, ...items.value.filter((old) => old.id !== item.id)]
      }
      return value
    } catch (cause) {
      if (current()) {
        if (isAccessDenied(cause)) {
          items.value = []
          cache.clear()
        }
        if (cause && typeof cause === 'object' && 'status' in cause && cause.status === 409)
          await load(orgId.value)
        if (!current()) throw cause
        error.value = cause instanceof Error ? cause.message : t('team.errors.saveResource')
      }
      throw cause
    } finally {
      if (current()) {
        if (kind === 'create') creating.value = false
        if (kind === 'update') updating.value = false
        deletingIds.value = deletingIds.value.filter((value) => value !== id)
      }
    }
  }
  const create = (body: Create) => mutate('create', undefined, (scope) => api.create(body, scope))
  const update = (id: number, body: Update) =>
    mutate('update', id, (scope) => api.update(id, body, scope))
  const remove = (id: number) => mutate('delete', id, (scope) => api.remove(id, scope))
  const filteredItems = computed(() =>
    items.value.filter(
      (item) =>
        (scopeFilter.value === 'all' || scopeFilter.value === item.scope) &&
        JSON.stringify(item).toLowerCase().includes(searchQuery.value.trim().toLowerCase()),
    ),
  )
  return {
    items,
    orgId,
    loading,
    creating,
    updating,
    deletingIds,
    error,
    searchQuery,
    scopeFilter,
    filteredItems,
    sortedItems: computed(() => items.value),
    totalCount: computed(() => items.value.length),
    systemCount: computed(() => items.value.filter((item) => item.scope === 'system').length),
    userCount: computed(() => items.value.filter((item) => item.scope === 'user').length),
    orgCount: computed(() => items.value.filter((item) => item.scope === 'org').length),
    canEdit,
    load,
    create,
    update,
    remove,
    setOrganization,
    reset: clear,
    resetFilters: () => {
      searchQuery.value = ''
      scopeFilter.value = 'all'
    },
    setSearchQuery: (value: string) => {
      searchQuery.value = value
    },
    setScopeFilter: (value: string) => {
      scopeFilter.value = value
    },
    clearError: () => {
      error.value = null
    },
  }
}
