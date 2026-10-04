import { computed, onScopeDispose, ref, shallowRef } from 'vue'
import { defineStore } from 'pinia'
import * as api from '@/api/storage'
import type { ApiSchemas } from '@/api/client-core'
import {
  captureSession,
  getSessionUserId,
  isSessionCurrent,
  onSessionChange,
  sessionGeneration,
} from '@/api/session-context'
import { isAccessDenied } from '@/api/utils'
import { safeStorageProblem, storageErrorMessage, storageResultUnknown } from '@/api/storage-errors'
import {
  canManageOrganization,
  onOrganizationInvalidated,
  organizationRoles,
} from '@/utils/organization-scope'
import { useAuthStore } from '@/stores/auth'
import { t } from '@/i18n'

export type StorageScope = { kind: 'user' } | { kind: 'org'; id: number } | { kind: 'site' }
type Connection = ApiSchemas['StorageConnection']
type Space = ApiSchemas['StorageSpace']
type ReadState<T> = {
  items: T[]
  loading: boolean
  loaded: boolean
  stale: boolean
  error: string | null
}
export type StorageWriteResult<T> =
  | { status: 'success'; value: T }
  | { status: 'error' | 'unknown' | 'stale' }
const freshRead = <T>(): ReadState<T> => ({
  items: [],
  loading: false,
  loaded: false,
  stale: false,
  error: null,
})
export const storageScopeKey = (scope: StorageScope): string =>
  scope.kind === 'org' ? `org:${scope.id}` : scope.kind

/** Secrets are passed directly to the transport; this state contains metadata only. */
export function createStorageState(transport = api, isAdmin: () => boolean = () => false) {
  const scope = shallowRef<StorageScope>({ kind: 'user' })
  const connections = ref<ReadState<Connection>>(freshRead())
  const spaces = ref<Record<number, ReadState<Space>>>({})
  const checks = ref<
    Record<number, ReadState<ApiSchemas['StorageCheck']> & { nextCursor?: number }>
  >({})
  const policy = shallowRef<ApiSchemas['StoragePolicy'] | null>(null)
  const policyError = ref<string | null>(null)
  const policyLoading = ref(false)
  const policyStale = ref(false)
  const updatedAt = ref<number | null>(null)
  const denied = ref(false)
  const busy = ref<Record<string, boolean>>({})
  const writeErrors = ref<Record<string, string>>({})
  const unknownWrites = ref<Record<string, boolean>>({})
  let epoch = 0
  let listSequence = 0
  let policySequence = 0
  let controller = new AbortController()
  const spaceSequences = new Map<number, number>()
  const checkSequences = new Map<number, number>()
  const writeSequences = new Map<number, number>()
  const canManage = computed(() => {
    void sessionGeneration.value
    return scope.value.kind === 'site'
      ? isAdmin()
      : scope.value.kind === 'org'
        ? Number.isSafeInteger(scope.value.id) &&
          scope.value.id > 0 &&
          canManageOrganization(organizationRoles.value[scope.value.id])
        : getSessionUserId() !== null
  })
  const ready = computed(
    () =>
      canManage.value &&
      connections.value.loaded &&
      !connections.value.stale &&
      !connections.value.loading &&
      !denied.value,
  )
  function invalidate() {
    ++epoch
    controller.abort()
    controller = new AbortController()
    connections.value = freshRead()
    spaces.value = {}
    checks.value = {}
    policy.value = null
    policyError.value = null
    policyLoading.value = policyStale.value = false
    updatedAt.value = null
    busy.value = {}
    writeErrors.value = {}
    unknownWrites.value = {}
    denied.value = false
    spaceSequences.clear()
    checkSequences.clear()
    writeSequences.clear()
  }
  function setScope(value: StorageScope) {
    if (storageScopeKey(value) === storageScopeKey(scope.value)) return
    invalidate()
    scope.value = value
  }
  function currentContext() {
    const session = captureSession(),
      version = epoch
    return () => version === epoch && isSessionCurrent(session) && canManage.value
  }
  function loseAccess() {
    invalidate()
    denied.value = true
    connections.value.error = t('storage.denied')
  }
  const belongs = (item: Connection) =>
    scope.value.kind === 'site'
      ? item.scope === 'site'
      : scope.value.kind === 'org'
        ? item.scope === 'org' && item.owner_id === scope.value.id
        : item.scope === 'user' && item.owner_id === getSessionUserId()
  function forgetConnection(id: number) {
    connections.value.items = connections.value.items.filter((item) => item.id !== id)
    delete spaces.value[id]
    delete checks.value[id]
    checkSequences.set(id, (checkSequences.get(id) ?? 0) + 1)
    spaceSequences.set(id, (spaceSequences.get(id) ?? 0) + 1)
  }
  async function load(value: StorageScope = scope.value): Promise<boolean> {
    setScope(value)
    if (!canManage.value) {
      loseAccess()
      return false
    }
    // A snapshot taken during a write can still contain the previous generation.
    if (Object.values(busy.value).some(Boolean)) return false
    const context = currentContext(),
      sequence = ++listSequence
    const current = () => context() && sequence === listSequence
    const state = connections.value
    state.loading = true
    state.error = null
    try {
      const response = await transport.listStorageConnections(scope.value, {
        signal: controller.signal,
      })
      if (!current()) return false
      state.items = response.items.filter(belongs)
      state.loaded = true
      state.stale = denied.value = false
      updatedAt.value = Date.now()
      for (const id of Object.keys(spaces.value))
        if (!state.items.some((item) => item.id === Number(id))) delete spaces.value[Number(id)]
      return true
    } catch (error) {
      if (current()) {
        if (isAccessDenied(error)) loseAccess()
        else {
          state.stale = state.loaded
          state.error = t('storage.readFailed')
        }
      }
      return false
    } finally {
      if (current()) state.loading = false
    }
  }
  async function loadSpaces(connectionId: number): Promise<boolean> {
    if (
      busy.value[`connection:${connectionId}`] ||
      !ready.value ||
      !connections.value.items.some((item) => item.id === connectionId)
    )
      return false
    const context = currentContext(),
      sequence = (spaceSequences.get(connectionId) ?? 0) + 1
    spaceSequences.set(connectionId, sequence)
    const current = () => context() && spaceSequences.get(connectionId) === sequence
    spaces.value[connectionId] ??= freshRead()
    const state = spaces.value[connectionId]!
    state.loading = true
    state.error = null
    try {
      const response = await transport.listStorageSpaces(connectionId, {
        signal: controller.signal,
      })
      if (!current()) return false
      state.items = response.items.filter((item) => item.connection_id === connectionId)
      state.loaded = true
      state.stale = false
      return true
    } catch (error) {
      if (current()) {
        if (isAccessDenied(error)) {
          forgetConnection(connectionId)
        } else {
          state.stale = state.loaded
          state.error = t('storage.readFailed')
        }
      }
      return false
    } finally {
      if (current()) state.loading = false
    }
  }
  async function loadChecks(connectionId: number, append = false): Promise<boolean> {
    if (
      !canManage.value ||
      denied.value ||
      !connections.value.items.some((item) => item.id === connectionId)
    )
      return false
    if (
      append &&
      (checks.value[connectionId]?.loading || checks.value[connectionId]?.nextCursor === undefined)
    )
      return false
    const context = currentContext()
    const sequence = (checkSequences.get(connectionId) ?? 0) + 1
    checkSequences.set(connectionId, sequence)
    const current = () => context() && checkSequences.get(connectionId) === sequence
    checks.value[connectionId] ??= freshRead()
    const state = checks.value[connectionId]!
    state.loading = true
    state.error = null
    try {
      const response = await transport.listStorageChecks(
        connectionId,
        { limit: 50, ...(append ? { cursor: state.nextCursor } : {}) },
        { signal: controller.signal },
      )
      if (!current()) return false
      const rows = response.items.filter((item) => item.connection_id === connectionId)
      state.items = append
        ? [...new Map([...state.items, ...rows].map((item) => [item.check_id, item])).values()]
        : rows
      state.nextCursor = rows.length === 50 ? rows.at(-1)?.check_id : undefined
      state.loaded = true
      state.stale = false
      return true
    } catch (error) {
      if (current()) {
        if (isAccessDenied(error)) {
          forgetConnection(connectionId)
          return false
        }
        state.stale = state.loaded
        state.error = storageErrorMessage(error)
      }
      return false
    } finally {
      if (current()) state.loading = false
    }
  }
  async function loadPolicy(): Promise<boolean> {
    if (scope.value.kind !== 'site' || !isAdmin() || busy.value.policy) return false
    const context = currentContext(),
      sequence = ++policySequence
    const current = () => context() && sequence === policySequence
    policyLoading.value = true
    policyError.value = null
    try {
      const value = await transport.getStoragePolicy({ signal: controller.signal })
      if (!current()) return false
      policy.value = value
      policyStale.value = false
      return true
    } catch (error) {
      if (current()) {
        if (isAccessDenied(error)) policy.value = null
        policyError.value = t('storage.readFailed')
        policyStale.value = true
      }
      return false
    } finally {
      if (current()) policyLoading.value = false
    }
  }
  async function write<T>(
    key: string,
    action: (signal: AbortSignal) => Promise<T>,
    connectionId?: number,
  ): Promise<StorageWriteResult<T>> {
    if (
      !canManage.value ||
      denied.value ||
      busy.value[key] ||
      unknownWrites.value[key] ||
      (connectionId !== undefined &&
        key !== `revoke:${connectionId}` &&
        busy.value[`revoke:${connectionId}`])
    )
      return { status: 'error' }
    if (
      connectionId !== undefined &&
      (!ready.value || !connections.value.items.some((item) => item.id === connectionId))
    )
      return { status: 'error' }
    const context = currentContext()
    const writeSequence =
      connectionId === undefined ? 0 : (writeSequences.get(connectionId) ?? 0) + 1
    if (connectionId !== undefined) writeSequences.set(connectionId, writeSequence)
    const current = () =>
      context() &&
      (connectionId === undefined || writeSequences.get(connectionId) === writeSequence)
    busy.value[key] = true
    delete writeErrors.value[key]
    if (key === 'policy') {
      ++policySequence
      policyStale.value = true
      policyLoading.value = false
    }
    // Reads started before a mutation must never restore the old authorization or health.
    ++listSequence
    connections.value.loading = false
    if (connectionId !== undefined) {
      spaceSequences.set(connectionId, (spaceSequences.get(connectionId) ?? 0) + 1)
      const state = spaces.value[connectionId]
      if (state) {
        state.loading = false
        state.stale = true
      }
    }
    try {
      const value = await action(controller.signal)
      if (!current()) return { status: 'stale' }
      connections.value.stale = connections.value.loaded
      return { status: 'success', value }
    } catch (error) {
      if (!current()) return { status: 'stale' }
      if (isAccessDenied(error)) {
        if (connectionId === undefined) loseAccess()
        else {
          forgetConnection(connectionId)
          writeErrors.value[key] = t('storage.denied')
        }
        return { status: 'error' }
      }
      const unknown = storageResultUnknown(error)
      unknownWrites.value[key] = unknown
      writeErrors.value[key] = storageErrorMessage(error)
      const checkId = safeStorageProblem(error).check_id
      if (connectionId !== undefined && checkId !== undefined) {
        try {
          const recovered = await transport.getStorageCheck(connectionId, checkId, {
            signal: controller.signal,
          })
          if (current() && recovered.connection_id === connectionId) {
            checks.value[connectionId] ??= freshRead()
            checks.value[connectionId]!.items = [
              recovered,
              ...checks.value[connectionId]!.items.filter((item) => item.check_id !== checkId),
            ]
            checks.value[connectionId]!.loaded = true
          }
        } catch {
          /* The explicit history read remains available after a transport failure. */
        }
      }
      connections.value.stale = connections.value.loaded
      return { status: unknown ? 'unknown' : 'error' }
    } finally {
      if (current()) busy.value[key] = false
    }
  }
  const connection = (id: number) => connections.value.items.find((item) => item.id === id)
  const validGeneration = (value: number | undefined): value is number =>
    value !== undefined && Number.isSafeInteger(value) && value >= 0
  async function recoverCheck(id: number, checkId: number | null | undefined) {
    const current = currentContext()
    if (typeof checkId === 'number' && Number.isSafeInteger(checkId) && checkId > 0) {
      try {
        const fact = await transport.getStorageCheck(id, checkId, { signal: controller.signal })
        if (!current()) return
        if (fact.connection_id === id && connections.value.items.some((item) => item.id === id)) {
          checks.value[id] ??= freshRead()
          checks.value[id]!.items = [
            fact,
            ...checks.value[id]!.items.filter((item) => item.check_id !== checkId),
          ]
          checks.value[id]!.loaded = true
        }
      } catch (cause) {
        if (current() && isAccessDenied(cause)) {
          forgetConnection(id)
          return
        }
      }
    }
    if (current()) await loadChecks(id)
  }
  async function check(id: number, writeCheck = false) {
    const generation = connection(id)?.management_generation
    if (!validGeneration(generation))
      return Promise.resolve<StorageWriteResult<Connection>>({ status: 'error' })
    const current = currentContext()
    const result = await write(
      `connection:${id}`,
      (signal) =>
        transport.checkStorageConnection(
          id,
          { write_check: writeCheck, expected_generation: generation },
          { signal },
        ),
      id,
    )
    if (current() && result.status !== 'stale')
      await recoverCheck(id, result.status === 'success' ? result.value.check_id : undefined)
    return result
  }
  async function authorize(
    id: number,
    secrets: Omit<ApiSchemas['StorageAuthorizationRequest'], 'expected_management_generation'>,
  ) {
    const generation = connection(id)?.management_generation
    if (!validGeneration(generation))
      return Promise.resolve<StorageWriteResult<Connection>>({ status: 'error' })
    const current = currentContext()
    const result = await write(
      `connection:${id}`,
      (signal) =>
        transport.authorizeStorage(
          id,
          { ...secrets, expected_management_generation: generation },
          { signal },
        ),
      id,
    )
    if (current() && result.status !== 'stale')
      await recoverCheck(id, result.status === 'success' ? result.value.check_id : undefined)
    return result
  }
  async function revoke(id: number) {
    const generation = connection(id)?.management_generation
    if (!validGeneration(generation)) return { status: 'error' } as const
    const pending = write(
      `revoke:${id}`,
      (signal) =>
        transport.revokeStorageAuthorization(id, { expected_generation: generation }, { signal }),
      id,
    )
    if (busy.value[`revoke:${id}`]) busy.value[`connection:${id}`] = false
    const result = await pending
    if (result.status === 'success') {
      const index = connections.value.items.findIndex((item) => item.id === id)
      if (index >= 0) connections.value.items[index] = result.value
      delete unknownWrites.value[`connection:${id}`]
      delete writeErrors.value[`connection:${id}`]
    }
    return result
  }
  function setConnectionState(id: number, status: 'enabled' | 'disabled') {
    const generation = connection(id)?.management_generation
    if (!validGeneration(generation))
      return Promise.resolve<StorageWriteResult<Connection>>({ status: 'error' })
    return write(
      `connection:${id}`,
      (signal) =>
        transport.setStorageConnectionState(
          id,
          { status, expected_generation: generation },
          { signal },
        ),
      id,
    )
  }
  function setSpaceState(id: number, spaceId: number, status: 'active' | 'read_only' | 'disabled') {
    const state = spaces.value[id],
      item = state?.items.find((value) => value.id === spaceId)
    if (!state?.loaded || state.stale || !validGeneration(item?.management_generation))
      return Promise.resolve<StorageWriteResult<Space>>({ status: 'error' })
    return write(
      `connection:${id}`,
      (signal) =>
        transport.setStorageSpaceState(
          spaceId,
          { status, expected_generation: item.management_generation },
          { signal },
        ),
      id,
    )
  }
  function createConnection(body: ApiSchemas['StorageConnectionRequest']) {
    const owner = scope.value
    if (owner.kind === 'site')
      return Promise.resolve<StorageWriteResult<Connection>>({ status: 'error' })
    return write('create', (signal) => transport.createStorageConnection(owner, body, { signal }))
  }
  const createSpace = (id: number, body: ApiSchemas['StorageSpaceRequest']) =>
    write(`connection:${id}`, (signal) => transport.createStorageSpace(id, body, { signal }), id)
  function savePolicy(body: ApiSchemas['StoragePolicy']) {
    if (
      scope.value.kind !== 'site' ||
      !policy.value ||
      policyStale.value ||
      policyLoading.value ||
      body.generation !== policy.value.generation
    )
      return Promise.resolve<StorageWriteResult<ApiSchemas['StoragePolicy']>>({ status: 'error' })
    return write('policy', (signal) => transport.setStoragePolicy(body, { signal }))
  }
  onScopeDispose(onSessionChange(invalidate))
  onOrganizationInvalidated((id) => {
    if (scope.value.kind === 'org' && scope.value.id === id) invalidate()
  })
  onScopeDispose(() => controller.abort())
  return {
    scope,
    connections,
    spaces,
    checks,
    policy,
    policyError,
    policyLoading,
    policyStale,
    updatedAt,
    denied,
    busy,
    writeErrors,
    unknownWrites,
    canManage,
    ready,
    invalidate,
    setScope,
    load,
    loadSpaces,
    loadChecks,
    loadPolicy,
    check,
    authorize,
    revoke,
    setConnectionState,
    setSpaceState,
    createConnection,
    createSpace,
    savePolicy,
  }
}

export const useStorageStore = defineStore('storage', () => {
  const auth = useAuthStore()
  return createStorageState(api, () => auth.user?.role === 'admin')
})
