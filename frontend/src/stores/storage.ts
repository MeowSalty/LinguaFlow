import { computed, onScopeDispose, ref, shallowRef, watch } from 'vue'
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
import { ApiError } from '@/api/utils'
import {
  safeStorageProblem,
  storageAccessDenied,
  storageErrorMessage,
  storageNeedsRefresh,
  storageResultUnknown,
  storageTaskErrorMessage,
} from '@/api/storage-errors'
import {
  hasConnectionManagementActions,
  hasSpaceManagementActions,
  hasStoragePolicyCapabilities,
  hasStoragePolicyQuota,
  hasStorageSpaceQuota,
  isStorageCapabilities,
  storageManagementAllowed,
  type StorageManagementAction,
  type StorageSnapshotStatus,
  type StorageSpaceAction,
} from '@/utils/storage-availability'
import { isStorageQuota } from '@/utils/storage-contract'
import { invalidateStorageSnapshots } from '@/utils/storage-snapshots'
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
type Policy = ApiSchemas['StoragePolicy']
const completePolicy = (value: Policy): boolean =>
  hasStoragePolicyCapabilities(value) &&
  hasStoragePolicyQuota(value) &&
  ['site_only', 'both', 'user_required'].includes(value.mode) &&
  ['site', 'user'].includes(value.default_choice)
type Recovery<T, Request> = {
  attemptId: number
  baseline: T
  submitted: Request
  latest: T | null
  reviewRevision: number
  state: 'needs_read' | 'loading' | 'ready' | 'error'
}
export type StorageQuotaRecovery = Recovery<Space, ApiSchemas['StorageSpaceQuotaRequest']>
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
  // Fresh runtime discovery remains useful when a server lacks the newer quota fields.
  const policyRuntime = shallowRef<ApiSchemas['StorageRuntime'] | null>(null)
  const policyError = ref<string | null>(null)
  const policyLoading = ref(false)
  const policyStale = ref(false)
  const capabilities = shallowRef<ApiSchemas['StorageCapabilities'] | null>(null)
  const capabilityStatus = ref<StorageSnapshotStatus>('idle')
  const capabilityError = ref<string | null>(null)
  const policySavedPendingRefresh = ref(false)
  const updatedAt = ref<number | null>(null)
  const denied = ref(false)
  const busy = ref<Record<string, boolean>>({})
  const writeErrors = ref<Record<string, string>>({})
  const unknownWrites = ref<Record<string, boolean>>({})
  const quotaRecoveries = ref<Record<number, StorageQuotaRecovery>>({})
  const policyRecovery = shallowRef<Recovery<Policy, ApiSchemas['StoragePolicyRequest']> | null>(
    null,
  )
  let attemptSequence = 0
  let epoch = 0
  let listSequence = 0
  let policySequence = 0
  let capabilitySequence = 0
  let controller = new AbortController()
  const spaceSequences = new Map<number, number>()
  const spaceReads = new Map<number, { promise: Promise<boolean>; current: () => boolean }>()
  let summaryRead: { promise: Promise<void>; current: () => boolean } | undefined
  let spaceReadQueue = { active: 0, pending: [] as Array<() => void> }
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
  const runtime = computed(() =>
    scope.value.kind === 'site' ? policyRuntime.value : capabilities.value?.runtime,
  )
  const runtimeReady = computed(() =>
    scope.value.kind === 'site'
      ? !!policyRuntime.value && !policyLoading.value
      : capabilityStatus.value === 'ready',
  )
  const canCreateConnection = computed(
    () =>
      scope.value.kind !== 'site' &&
      canManage.value &&
      !denied.value &&
      storageManagementAllowed(
        capabilities.value?.management_actions.create_connection,
        capabilityStatus.value === 'ready',
      ) &&
      !busy.value.create &&
      !unknownWrites.value.create,
  )
  const createConnectionReason = computed(
    () =>
      capabilityError.value ||
      (capabilityStatus.value !== 'ready'
        ? t('storage.notAvailable')
        : (capabilities.value?.management_actions.create_connection.reason_codes
            .map(storageTaskErrorMessage)
            .join(' · ') ?? '')),
  )
  function markStale() {
    ++listSequence
    ++capabilitySequence
    connections.value.loading = false
    connections.value.stale = connections.value.loaded
    invalidateSpaceReads()
    invalidateCheckReads()
    if (capabilityStatus.value !== 'idle') capabilityStatus.value = 'stale'
    markPolicyStale()
  }
  function invalidateSpaceReads() {
    summaryRead = undefined
    spaceReads.clear()
    for (const [id, state] of Object.entries(spaces.value)) {
      spaceSequences.set(Number(id), (spaceSequences.get(Number(id)) ?? 0) + 1)
      state.loading = false
      state.stale = state.loaded
    }
    for (const recovery of Object.values(quotaRecoveries.value)) {
      recovery.latest = null
      recovery.state = 'needs_read'
      recovery.reviewRevision++
    }
  }
  function invalidateCheckReads() {
    for (const [id, state] of Object.entries(checks.value)) {
      checkSequences.set(Number(id), (checkSequences.get(Number(id)) ?? 0) + 1)
      state.loading = false
      state.stale = state.loaded
    }
  }
  function markPolicyStale() {
    policyRuntime.value = null
    ++policySequence
    policyLoading.value = false
    policyStale.value = !!policy.value
    if (policyRecovery.value)
      policyRecovery.value = {
        ...policyRecovery.value,
        latest: null,
        state: 'needs_read',
        reviewRevision: policyRecovery.value.reviewRevision + 1,
      }
  }
  function invalidate() {
    ++epoch
    controller.abort()
    controller = new AbortController()
    connections.value = freshRead()
    spaces.value = {}
    checks.value = {}
    policy.value = null
    policyRuntime.value = null
    policyError.value = null
    policyLoading.value = policyStale.value = false
    capabilities.value = null
    capabilityStatus.value = 'idle'
    capabilityError.value = null
    policySavedPendingRefresh.value = false
    updatedAt.value = null
    busy.value = {}
    writeErrors.value = {}
    unknownWrites.value = {}
    quotaRecoveries.value = {}
    policyRecovery.value = null
    denied.value = false
    spaceSequences.clear()
    spaceReads.clear()
    summaryRead = undefined
    spaceReadQueue = { active: 0, pending: [] }
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
    spaceReads.delete(id)
    delete checks.value[id]
    checkSequences.set(id, (checkSequences.get(id) ?? 0) + 1)
    spaceSequences.set(id, (spaceSequences.get(id) ?? 0) + 1)
    for (const [spaceId, recovery] of Object.entries(quotaRecoveries.value))
      if (recovery.baseline.connection_id === id) delete quotaRecoveries.value[Number(spaceId)]
    delete unknownWrites.value[`connection:${id}`]
    delete writeErrors.value[`connection:${id}`]
  }
  async function load(value: StorageScope = scope.value): Promise<boolean> {
    setScope(value)
    if (!canManage.value) {
      loseAccess()
      return false
    }
    // A list refresh invalidates cached ledgers and history without eagerly reloading either.
    invalidateSpaceReads()
    invalidateCheckReads()
    // Reads remain available during slow probes. A completed mutation invalidates their sequence.
    const capabilityRead = scope.value.kind === 'site' ? Promise.resolve(true) : loadCapabilities()
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
        if (!state.items.some((item) => item.id === Number(id))) forgetConnection(Number(id))
      // Present authorized metadata independently of optional-version discovery latency.
      state.loading = false
      await capabilityRead
      return current()
    } catch (error) {
      if (current()) {
        if (storageAccessDenied(error)) loseAccess()
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
  async function loadCapabilities(): Promise<boolean> {
    if (scope.value.kind === 'site' || !canManage.value) return false
    const owner = scope.value,
      context = currentContext(),
      sequence = ++capabilitySequence
    const current = () => context() && sequence === capabilitySequence
    capabilityStatus.value = 'loading'
    capabilityError.value = null
    try {
      const value = await transport.getStorageCapabilities(owner, { signal: controller.signal })
      if (!current()) return false
      if (
        !isStorageCapabilities(value) ||
        value.scope !== owner.kind ||
        value.owner_id !== (owner.kind === 'org' ? owner.id : getSessionUserId())
      ) {
        capabilityStatus.value = 'unsupported'
        capabilityError.value = t('storage.notAvailable')
        return false
      }
      capabilities.value = value
      capabilityStatus.value = 'ready'
      return true
    } catch (error) {
      if (current()) {
        // An org 404 is ambiguous: preserve reads but never fall back to personal capability.
        if (error instanceof ApiError && error.status === 404) {
          capabilityStatus.value = owner.kind === 'user' ? 'unsupported' : 'error'
          capabilityError.value = t(
            owner.kind === 'user'
              ? 'storageErrors.capabilityUnsupportedVersion'
              : 'storageErrors.capabilityScopeUnavailable',
          )
          return false
        } else if (storageAccessDenied(error)) {
          loseAccess()
          return false
        } else capabilityStatus.value = 'error'
        capabilityError.value = storageErrorMessage(error)
      }
      return false
    }
  }
  function loadSpaces(connectionId: number, force = false): Promise<boolean> {
    if (
      busy.value[`connection:${connectionId}`] ||
      !ready.value ||
      !connections.value.items.some((item) => item.id === connectionId)
    )
      return Promise.resolve(false)
    const pending = spaceReads.get(connectionId)
    if (pending?.current()) return pending.promise
    const cached = spaces.value[connectionId]
    if (!force && cached?.loaded && !cached.stale && !cached.error) return Promise.resolve(true)
    const context = currentContext(),
      sequence = (spaceSequences.get(connectionId) ?? 0) + 1
    spaceSequences.set(connectionId, sequence)
    const current = () => context() && spaceSequences.get(connectionId) === sequence
    spaces.value[connectionId] ??= freshRead()
    const state = spaces.value[connectionId]!
    state.loading = true
    state.error = null
    for (const recovery of Object.values(quotaRecoveries.value)) {
      if (recovery.baseline.connection_id !== connectionId) continue
      recovery.state = 'loading'
      recovery.latest = null
      recovery.reviewRevision++
    }
    const promise = scheduleSpaceRead(
      () => readSpaces(connectionId, state, current),
      current,
    ).finally(() => {
      if (spaceReads.get(connectionId)?.promise === promise) spaceReads.delete(connectionId)
    })
    spaceReads.set(connectionId, { promise, current })
    return promise
  }
  function scheduleSpaceRead(
    read: () => Promise<boolean>,
    current: () => boolean,
  ): Promise<boolean> {
    const queue = spaceReadQueue
    function drain() {
      while (queue.active < 4 && queue.pending.length) queue.pending.shift()!()
    }
    return new Promise((resolve) => {
      queue.pending.push(() => {
        if (!current()) {
          resolve(false)
          return
        }
        queue.active++
        void read()
          .then(resolve, () => resolve(false))
          .finally(() => {
            queue.active--
            drain()
          })
      })
      drain()
    })
  }
  async function readSpaces(
    connectionId: number,
    state: ReadState<Space>,
    current: () => boolean,
  ): Promise<boolean> {
    try {
      const response = await transport.listStorageSpaces(connectionId, {
        signal: controller.signal,
      })
      if (!current()) return false
      const items = response.items.filter((item) => item.connection_id === connectionId)
      const regressed = items.some((item) => {
        const previous = state.items.find((prior) => prior.id === item.id)
        return (
          previous &&
          validGeneration(previous.management_generation) &&
          (!validGeneration(item.management_generation) ||
            item.management_generation < previous.management_generation)
        )
      })
      if (regressed) {
        state.stale = true
        state.error = t('storage.stale')
        for (const recovery of Object.values(quotaRecoveries.value))
          if (recovery.baseline.connection_id === connectionId) recovery.state = 'error'
        return false
      }
      state.items = items
      state.loaded = true
      state.stale = false
      for (const recovery of Object.values(quotaRecoveries.value)) {
        if (recovery.baseline.connection_id !== connectionId) continue
        const latest = items.find((item) => item.id === recovery.baseline.id)
        const complete =
          latest &&
          hasStorageSpaceQuota(latest) &&
          hasSpaceManagementActions(latest, 'set_quota') &&
          validGeneration(latest.management_generation) &&
          latest.management_generation >= recovery.baseline.management_generation
        recovery.latest = complete ? latest : null
        recovery.state = complete ? 'ready' : 'error'
        recovery.reviewRevision++
      }
      return true
    } catch (error) {
      if (current()) {
        if (storageAccessDenied(error)) {
          forgetConnection(connectionId)
        } else {
          state.stale = state.loaded
          state.error = t('storage.readFailed')
          for (const recovery of Object.values(quotaRecoveries.value))
            if (recovery.baseline.connection_id === connectionId) recovery.state = 'error'
        }
      }
      return false
    } finally {
      if (current()) state.loading = false
    }
  }
  function loadSpaceSummaries(): Promise<void> {
    if (!ready.value) return Promise.resolve()
    if (summaryRead?.current()) return summaryRead.promise
    const context = currentContext(),
      sequence = listSequence
    const current = () => context() && listSequence === sequence && ready.value
    const ids = connections.value.items.map((item) => item.id)
    let next = 0
    async function worker() {
      while (current() && next < ids.length) {
        const id = ids[next++]!
        await loadSpaces(id)
      }
    }
    const promise = Promise.all(Array.from({ length: Math.min(4, ids.length) }, worker))
      .then(() => undefined)
      .finally(() => {
        if (summaryRead?.promise === promise) summaryRead = undefined
      })
    summaryRead = { promise, current }
    return promise
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
        if (storageAccessDenied(error)) {
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
    policyRuntime.value = null
    policyError.value = null
    if (policyRecovery.value)
      policyRecovery.value = {
        ...policyRecovery.value,
        latest: null,
        state: 'loading',
        reviewRevision: policyRecovery.value.reviewRevision + 1,
      }
    try {
      const value = await transport.getStoragePolicy({ signal: controller.signal })
      if (!current()) return false
      if (
        hasStoragePolicyCapabilities(value) &&
        validGeneration(value.generation) &&
        (!policy.value || value.generation >= policy.value.generation)
      )
        policyRuntime.value = value.runtime
      const complete =
        completePolicy(value) &&
        validGeneration(value.generation) &&
        (!policy.value || value.generation >= policy.value.generation)
      if (policyRecovery.value) {
        policyRecovery.value = {
          ...policyRecovery.value,
          latest: complete ? value : null,
          state: complete ? 'ready' : 'error',
          reviewRevision: policyRecovery.value.reviewRevision + 1,
        }
      } else if (complete) policy.value = value
      policyStale.value = !complete
      policyError.value = policyStale.value ? t('storage.notAvailable') : null
      policySavedPendingRefresh.value = false
      return complete
    } catch (error) {
      if (current()) {
        if (storageAccessDenied(error)) {
          loseAccess()
          return false
        }
        if (policyRecovery.value)
          policyRecovery.value = { ...policyRecovery.value, latest: null, state: 'error' }
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
      ((!ready.value &&
        !(
          key === `revoke:${connectionId}` &&
          (busy.value[`connection:${connectionId}`] ||
            unknownWrites.value[`connection:${connectionId}`])
        )) ||
        !connections.value.items.some((item) => item.id === connectionId))
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
      policyRuntime.value = null
      ++policySequence
      policyStale.value = true
      policyLoading.value = false
    }
    // Reads started before a mutation must never restore the old authorization or health.
    ++listSequence
    connections.value.loading = false
    if (connectionId !== undefined) {
      spaceSequences.set(connectionId, (spaceSequences.get(connectionId) ?? 0) + 1)
      spaceReads.delete(connectionId)
      const state = spaces.value[connectionId]
      if (state) {
        state.loading = false
        state.stale = true
      }
      for (const recovery of Object.values(quotaRecoveries.value)) {
        if (recovery.baseline.connection_id !== connectionId) continue
        recovery.latest = null
        recovery.state = 'needs_read'
        recovery.reviewRevision++
      }
    }
    try {
      const value = await action(controller.signal)
      if (!current()) return { status: 'stale' }
      ++listSequence
      connections.value.loading = false
      connections.value.stale = connections.value.loaded
      return { status: 'success', value }
    } catch (error) {
      if (!current()) return { status: 'stale' }
      if (storageAccessDenied(error)) {
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
      if (
        storageNeedsRefresh(error) ||
        (key === 'policy' && error instanceof ApiError && error.status === 409)
      )
        invalidateStorageSnapshots(
          scope.value.kind === 'org' ? { organizationId: scope.value.id } : {},
        )
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
  function connectionAllowed(id: number, action: StorageManagementAction) {
    const item = connection(id)
    const emergency =
      action === 'revoke_auth' &&
      (!!busy.value[`connection:${id}`] || !!unknownWrites.value[`connection:${id}`])
    return (
      canManage.value &&
      !denied.value &&
      !!item &&
      hasConnectionManagementActions(item) &&
      validGeneration(item.management_generation) &&
      storageManagementAllowed(
        item.management_actions[action],
        emergency || (ready.value && runtimeReady.value),
      ) &&
      !busy.value[`revoke:${id}`] &&
      (action === 'revoke_auth' ||
        (!busy.value[`connection:${id}`] && !unknownWrites.value[`connection:${id}`])) &&
      (action !== 'revoke_auth' || !unknownWrites.value[`revoke:${id}`])
    )
  }
  function connectionReason(id: number, action: StorageManagementAction) {
    const item = connection(id)
    if (!item || !hasConnectionManagementActions(item)) return t('storage.notAvailable')
    if (item.management_actions[action].reason_codes.length)
      return item.management_actions[action].reason_codes.map(storageTaskErrorMessage).join(' · ')
    return connectionAllowed(id, action) ? '' : t('storage.stale')
  }
  function spaceAllowed(id: number, spaceId: number, action: StorageSpaceAction = 'set_status') {
    const state = spaces.value[id],
      item = state?.items.find((value) => value.id === spaceId)
    return (
      ready.value &&
      runtimeReady.value &&
      !!state?.loaded &&
      !state.stale &&
      !state.loading &&
      !!item &&
      hasSpaceManagementActions(item, action) &&
      (action !== 'set_quota' || hasStorageSpaceQuota(item)) &&
      validGeneration(item.management_generation) &&
      storageManagementAllowed(item.management_actions[action], true) &&
      !busy.value[`connection:${id}`] &&
      !busy.value[`revoke:${id}`] &&
      !unknownWrites.value[`connection:${id}`]
    )
  }
  function spaceReason(id: number, spaceId: number, action: StorageSpaceAction = 'set_status') {
    const item = spaces.value[id]?.items.find((value) => value.id === spaceId)
    if (
      !item ||
      !hasSpaceManagementActions(item, action) ||
      (action === 'set_quota' && !hasStorageSpaceQuota(item))
    )
      return t('storage.notAvailable')
    return (
      item.management_actions[action].reason_codes.map(storageTaskErrorMessage).join(' · ') ||
      (spaceAllowed(id, spaceId, action) ? '' : t('storage.stale'))
    )
  }
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
        if (current() && storageAccessDenied(cause)) {
          forgetConnection(id)
          return
        }
      }
    }
    if (current()) await loadChecks(id)
  }
  async function check(id: number, writeCheck = false) {
    const generation = connection(id)?.management_generation
    if (
      !validGeneration(generation) ||
      !connectionAllowed(id, writeCheck ? 'check_write' : 'check_read')
    )
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
    if (
      !validGeneration(generation) ||
      !connectionAllowed(id, secrets.write_check ? 'authorize_write' : 'authorize_read')
    )
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
    if (!validGeneration(generation) || !connectionAllowed(id, 'revoke_auth'))
      return { status: 'error' } as const
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
      if (!Object.values(quotaRecoveries.value).some((item) => item.baseline.connection_id === id))
        delete unknownWrites.value[`connection:${id}`]
      delete writeErrors.value[`connection:${id}`]
    }
    return result
  }
  function setConnectionState(id: number, status: 'enabled' | 'disabled') {
    const generation = connection(id)?.management_generation
    if (!validGeneration(generation) || !connectionAllowed(id, 'set_status'))
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
  async function setSpaceState(
    id: number,
    spaceId: number,
    status: 'active' | 'read_only' | 'disabled',
  ) {
    const state = spaces.value[id],
      item = state?.items.find((value) => value.id === spaceId)
    if (!spaceAllowed(id, spaceId) || !validGeneration(item?.management_generation))
      return Promise.resolve<StorageWriteResult<Space>>({ status: 'error' })
    const result = await write(
      `connection:${id}`,
      (signal) =>
        transport.setStorageSpaceState(
          spaceId,
          { status, expected_generation: item.management_generation },
          { signal },
        ),
      id,
    )
    if (result.status === 'success') acceptSpace(id, result.value)
    return result
  }
  function acceptSpace(id: number, value: Space) {
    const state = spaces.value[id]
    if (!state || value.connection_id !== id) return
    const index = state.items.findIndex((item) => item.id === value.id)
    if (index >= 0 && value.management_generation >= state.items[index]!.management_generation)
      state.items[index] = value
    state.stale = true
  }
  async function setSpaceQuota(
    id: number,
    spaceId: number,
    body: ApiSchemas['StorageSpaceQuotaRequest'],
  ) {
    const baseline = spaces.value[id]?.items.find((item) => item.id === spaceId)
    if (
      !baseline ||
      !spaceAllowed(id, spaceId, 'set_quota') ||
      body.expected_generation !== baseline.management_generation ||
      !isStorageQuota(body.capacity_bytes)
    )
      return { status: 'error' } as const
    const current = currentContext(),
      attemptId = ++attemptSequence
    const submitted = {
      capacity_bytes: body.capacity_bytes,
      expected_generation: body.expected_generation,
    }
    let result: StorageWriteResult<Space> = await write(
      `connection:${id}`,
      (signal) => transport.setStorageSpaceQuota(spaceId, submitted, { signal }),
      id,
    )
    if (!current()) return { status: 'stale' } as const
    if (result.status === 'success') {
      const value = result.value
      if (
        value.id !== spaceId ||
        value.connection_id !== id ||
        !hasStorageSpaceQuota(value) ||
        !hasSpaceManagementActions(value, 'set_quota') ||
        !validGeneration(value.management_generation) ||
        value.management_generation < baseline.management_generation
      )
        result = { status: 'unknown' }
      else acceptSpace(id, value)
    }
    // A simultaneous emergency revocation may supersede this response without proving the quota outcome.
    if (result.status === 'unknown' || result.status === 'stale') {
      quotaRecoveries.value[spaceId] = {
        attemptId,
        baseline: { ...baseline },
        submitted,
        latest: null,
        reviewRevision: 0,
        state: 'needs_read',
      }
      unknownWrites.value[`connection:${id}`] = true
      writeErrors.value[`connection:${id}`] = t('storageQuota.unknown')
      return { status: 'unknown' } as const
    }
    return result
  }
  function acknowledgeSpaceQuotaUnknown(
    spaceId: number,
    attemptId: number,
    reviewRevision: number,
  ): Space | null {
    const recovery = quotaRecoveries.value[spaceId],
      latest = recovery?.latest
    if (
      !recovery ||
      !latest ||
      recovery.attemptId !== attemptId ||
      recovery.reviewRevision !== reviewRevision ||
      recovery.state !== 'ready'
    )
      return null
    const id = recovery.baseline.connection_id,
      state = spaces.value[id]
    if (
      !canManage.value ||
      denied.value ||
      !ready.value ||
      !runtimeReady.value ||
      !state?.loaded ||
      state.stale ||
      state.loading ||
      busy.value[`connection:${id}`] ||
      busy.value[`revoke:${id}`] ||
      !connection(id) ||
      !hasStorageSpaceQuota(latest) ||
      !hasSpaceManagementActions(latest, 'set_quota') ||
      !storageManagementAllowed(latest.management_actions.set_quota, true) ||
      state.items.find((item) => item.id === spaceId)?.management_generation !==
        latest.management_generation
    )
      return null
    delete quotaRecoveries.value[spaceId]
    delete unknownWrites.value[`connection:${id}`]
    delete writeErrors.value[`connection:${id}`]
    return latest
  }
  function acknowledgePolicyUnknown(attemptId: number, reviewRevision: number): Policy | null {
    const recovery = policyRecovery.value,
      latest = recovery?.latest
    if (
      !recovery ||
      !latest ||
      recovery.attemptId !== attemptId ||
      recovery.reviewRevision !== reviewRevision ||
      recovery.state !== 'ready' ||
      scope.value.kind !== 'site' ||
      !canManage.value ||
      denied.value ||
      busy.value.policy ||
      policyLoading.value ||
      policyStale.value ||
      !completePolicy(latest)
    )
      return null
    policy.value = latest
    policyRecovery.value = null
    delete unknownWrites.value.policy
    delete writeErrors.value.policy
    return latest
  }
  function createConnection(body: ApiSchemas['StorageConnectionRequest']) {
    const owner = scope.value
    if (owner.kind === 'site' || !canCreateConnection.value)
      return Promise.resolve<StorageWriteResult<Connection>>({ status: 'error' })
    return write('create', (signal) => transport.createStorageConnection(owner, body, { signal }))
  }
  const createSpace = (id: number, body: ApiSchemas['StorageSpaceRequest']) =>
    connectionAllowed(id, 'create_space') && isStorageQuota(body.capacity_bytes)
      ? write(
          `connection:${id}`,
          (signal) => transport.createStorageSpace(id, body, { signal }),
          id,
        )
      : Promise.resolve<StorageWriteResult<Space>>({ status: 'error' })
  async function savePolicy(body: ApiSchemas['StoragePolicyRequest']) {
    if (
      scope.value.kind !== 'site' ||
      !policy.value ||
      policyStale.value ||
      policyLoading.value ||
      !hasStoragePolicyCapabilities(policy.value) ||
      !hasStoragePolicyQuota(policy.value) ||
      !isStorageQuota(body.logical_limit_bytes) ||
      !isStorageQuota(body.default_space_capacity_bytes) ||
      !policy.value.allowed_policy_modes.includes(body.mode) ||
      (body.mode === 'site_only' && body.default_choice !== 'site') ||
      (body.mode === 'user_required' && body.default_choice !== 'user') ||
      body.generation !== policy.value.generation
    )
      return Promise.resolve<StorageWriteResult<ApiSchemas['StoragePolicy']>>({ status: 'error' })
    const snapshot: ApiSchemas['StoragePolicyRequest'] = {
      mode: body.mode,
      default_choice: body.default_choice,
      generation: body.generation,
      logical_limit_bytes: body.logical_limit_bytes,
      default_space_capacity_bytes: body.default_space_capacity_bytes,
    }
    const baseline = { ...policy.value },
      attemptId = ++attemptSequence,
      current = currentContext()
    let result: StorageWriteResult<Policy> = await write('policy', (signal) =>
      transport.setStoragePolicy(snapshot, { signal }),
    )
    if (!current()) return { status: 'stale' } as const
    if (
      result.status === 'success' &&
      (!completePolicy(result.value) ||
        !validGeneration(result.value.generation) ||
        result.value.generation < baseline.generation)
    )
      result = { status: 'unknown' }
    if (result.status === 'success') {
      policy.value = result.value
      policyStale.value = true
      policySavedPendingRefresh.value = true
    } else if (result.status === 'unknown') {
      policyRecovery.value = {
        attemptId,
        baseline,
        submitted: snapshot,
        latest: null,
        reviewRevision: 0,
        state: 'needs_read',
      }
      unknownWrites.value.policy = true
      writeErrors.value.policy = t('storageQuota.unknown')
    }
    return result
  }
  onScopeDispose(onSessionChange(invalidate))
  watch(
    canManage,
    (allowed) => {
      if (!allowed) loseAccess()
    },
    { flush: 'sync' },
  )
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
    policySavedPendingRefresh,
    capabilities,
    capabilityStatus,
    capabilityError,
    runtime,
    canCreateConnection,
    createConnectionReason,
    connectionAllowed,
    connectionReason,
    spaceAllowed,
    spaceReason,
    markStale,
    markPolicyStale,
    updatedAt,
    denied,
    busy,
    writeErrors,
    unknownWrites,
    quotaRecoveries,
    policyRecovery,
    acknowledgeSpaceQuotaUnknown,
    acknowledgePolicyUnknown,
    canManage,
    ready,
    invalidate,
    setScope,
    load,
    loadSpaces,
    loadSpaceSummaries,
    loadChecks,
    loadPolicy,
    loadCapabilities,
    check,
    authorize,
    revoke,
    setConnectionState,
    setSpaceState,
    setSpaceQuota,
    createConnection,
    createSpace,
    savePolicy,
  }
}

export const useStorageStore = defineStore('storage', () => {
  const auth = useAuthStore()
  return createStorageState(api, () => auth.user?.role === 'admin')
})
