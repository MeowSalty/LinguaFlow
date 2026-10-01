import { computed, onScopeDispose, ref, shallowRef } from 'vue'
import { defineStore } from 'pinia'
import * as api from '@/api/credentials'
import type { ApiSchemas } from '@/api/client-core'
import {
  captureSession,
  isSessionCurrent,
  onSessionChange,
  getSessionUserId,
} from '@/api/session-context'
import {
  canManageOrganization,
  onOrganizationInvalidated,
  organizationRoles,
} from '@/utils/organization-scope'
import {
  isCredentialAccessDenied,
  isUnknownCredentialWrite,
  safeCredentialError,
} from '@/api/credential-errors'
import { t } from '@/i18n'

type VersionState = {
  items: api.CredentialVersion[]
  loading: boolean
  loaded: boolean
  stale: boolean
  error: string | null
}
type WriteState = { busy: boolean; unknown: boolean; error: string | null }
export type CredentialWriteResult<T> =
  | { status: 'success'; value: T }
  | { status: 'error' | 'unknown' | 'stale' }
const emptyWrite = (): WriteState => ({ busy: false, unknown: false, error: null })
const emptyVersions = (): VersionState => ({
  items: [],
  loading: false,
  loaded: false,
  stale: false,
  error: null,
})

/** Metadata only. Submitted secrets remain local arguments and are never assigned to state. */
export function createCredentialState(transport = api) {
  const items = shallowRef<api.Credential[]>([])
  const orgId = ref<number | null>(null)
  const loading = ref(false)
  const loaded = ref(false)
  const stale = ref(false)
  const accessDenied = ref(false)
  const error = ref<string | null>(null)
  const versions = ref<Record<number, VersionState>>({})
  const writes = ref<Record<number, WriteState>>({})
  const creation = ref<WriteState>(emptyWrite())
  let generation = 0
  let listSequence = 0
  const versionSequences = new Map<number, number>()
  let controller = new AbortController()
  let listFlight: Promise<boolean> | null = null
  const versionFlights = new Map<number, Promise<boolean>>()
  const canManage = computed(
    () => orgId.value === null || canManageOrganization(organizationRoles.value[orgId.value]),
  )
  const ready = computed(() => canManage.value && loaded.value && !stale.value && !loading.value)
  const invalidate = () => {
    ++generation
    controller.abort()
    controller = new AbortController()
    listFlight = null
    versionFlights.clear()
    versionSequences.clear()
    items.value = []
    versions.value = {}
    writes.value = {}
    creation.value = emptyWrite()
    loading.value = loaded.value = stale.value = false
    error.value = null
    accessDenied.value = false
  }
  const setOrganization = (scope: number | null) => {
    if (scope === orgId.value) return
    invalidate()
    orgId.value = scope
  }
  const context = () => {
    const session = captureSession(),
      currentGeneration = generation
    return () => generation === currentGeneration && isSessionCurrent(session) && canManage.value
  }
  const denied = (id?: number) => {
    invalidate()
    accessDenied.value = true
    error.value = t('configurationCredentials.denied')
    if (id !== undefined) writes.value[id] = { ...emptyWrite(), error: error.value }
  }
  const belongs = (item: api.Credential) =>
    orgId.value === null
      ? item.scope === 'user' && item.owner_id === getSessionUserId()
      : item.scope === 'org' && item.owner_id === orgId.value
  const load = (scope: number | null = orgId.value): Promise<boolean> => {
    setOrganization(scope)
    if (!canManage.value || (scope !== null && (!Number.isSafeInteger(scope) || scope < 1))) {
      denied()
      return Promise.resolve(false)
    }
    if (listFlight) return listFlight
    accessDenied.value = false
    const currentContext = context(),
      sequence = ++listSequence
    const current = () => currentContext() && sequence === listSequence
    loading.value = true
    error.value = null
    const work = async () => {
      try {
        const response = await transport.fetchCredentials(scope, controller.signal)
        if (!current()) return false
        items.value = response.items.filter(belongs).map(api.credentialMetadata)
        loaded.value = true
        stale.value = false
        for (const id of Object.keys(versions.value))
          if (!items.value.some((item) => item.id === Number(id))) delete versions.value[Number(id)]
        return true
      } catch (cause) {
        if (current()) {
          if (isCredentialAccessDenied(cause)) denied()
          else {
            stale.value = loaded.value
            error.value = t('configurationCredentials.readFailure')
          }
        }
        return false
      } finally {
        if (current()) {
          loading.value = false
          listFlight = null
        }
      }
    }
    listFlight = work()
    return listFlight
  }
  const loadVersions = (id: number): Promise<boolean> => {
    if (!ready.value || !items.value.some((item) => item.id === id)) return Promise.resolve(false)
    const flight = versionFlights.get(id)
    if (flight) return flight
    const currentContext = context(),
      sequence = (versionSequences.get(id) ?? 0) + 1
    versionSequences.set(id, sequence)
    const current = () => currentContext() && versionSequences.get(id) === sequence
    versions.value[id] ??= emptyVersions()
    const state = versions.value[id]!
    state.loading = true
    state.error = null
    const work = async () => {
      try {
        const response = await transport.fetchCredentialVersions(id, controller.signal)
        if (!current()) return false
        state.items = response.items.map(api.versionMetadata)
        state.loaded = true
        state.stale = false
        return true
      } catch (cause) {
        if (current()) {
          if (isCredentialAccessDenied(cause)) denied(id)
          else {
            state.stale = state.loaded
            state.error = t('configurationCredentials.readFailure')
          }
        }
        return false
      } finally {
        if (current()) {
          state.loading = false
          versionFlights.delete(id)
        }
      }
    }
    const result = work()
    versionFlights.set(id, result)
    return result
  }
  const canMutate = (id: number) =>
    ready.value &&
    items.value.some((item) => item.id === id) &&
    !writes.value[id]?.busy &&
    !!versions.value[id]?.loaded &&
    !versions.value[id]?.error &&
    !versions.value[id]?.stale &&
    !versions.value[id]?.loading
  const write = async <T>(
    id: number | undefined,
    action: () => Promise<T>,
    retryUnknown = false,
  ): Promise<CredentialWriteResult<T>> => {
    if (id !== undefined) writes.value[id] ??= emptyWrite()
    const state = id === undefined ? creation.value : writes.value[id]!
    if (
      !canManage.value ||
      accessDenied.value ||
      state.busy ||
      (id !== undefined && !canMutate(id)) ||
      (state.unknown && !retryUnknown)
    )
      return { status: 'error' }
    const current = context()
    state.busy = true
    state.error = null
    state.unknown = false
    try {
      const value = await action()
      if (!current()) return { status: 'stale' }
      // Invalidate reads accepted before this write, so an old response cannot restore old versions.
      ++listSequence
      listFlight = null
      loading.value = false
      if (loaded.value) stale.value = true
      if (id !== undefined) {
        versionSequences.set(id, (versionSequences.get(id) ?? 0) + 1)
        versionFlights.delete(id)
        const versionState = versions.value[id]
        if (versionState) {
          versionState.stale = true
          versionState.loading = false
        }
      }
      return { status: 'success', value }
    } catch (cause) {
      if (!current()) return { status: 'stale' }
      if (isCredentialAccessDenied(cause)) {
        denied(id)
        return { status: 'error' }
      }
      state.unknown = isUnknownCredentialWrite(cause)
      state.error = state.unknown
        ? t('configurationCredentials.unknown')
        : safeCredentialError(cause).message
      return { status: state.unknown ? 'unknown' : 'error' }
    } finally {
      if (current()) state.busy = false
    }
  }
  const create = (body: ApiSchemas['CreateCredentialRequest'], retryUnknown = false) => {
    const scope = orgId.value
    return write(undefined, () => transport.createCredential(body, scope), retryUnknown)
  }
  const rotate = (id: number, body: ApiSchemas['RotateCredentialRequest'], retryUnknown = false) =>
    write(id, () => transport.rotateCredential(id, body), retryUnknown)
  const revoke = (id: number, version: number, retryUnknown = false) =>
    write(id, () => transport.revokeCredentialVersion(id, version), retryUnknown)
  const collect = (id: number, retryUnknown = false) =>
    write(id, () => transport.collectCredentialVersions(id), retryUnknown)
  onScopeDispose(
    onSessionChange(() => {
      invalidate()
      orgId.value = null
    }),
  )
  onOrganizationInvalidated((id) => {
    if (orgId.value === id) invalidate()
  })
  onScopeDispose(() => controller.abort())
  return {
    items,
    orgId,
    loading,
    loaded,
    stale,
    accessDenied,
    error,
    versions,
    writes,
    creation,
    canManage,
    ready,
    load,
    loadVersions,
    setOrganization,
    invalidate,
    create,
    rotate,
    revoke,
    collect,
    canMutate,
  }
}

export const useCredentialsStore = defineStore('credentials', () => createCredentialState())
