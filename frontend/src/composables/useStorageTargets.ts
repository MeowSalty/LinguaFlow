import { computed, ref, shallowRef, watch } from 'vue'
import type { ApiSchemas } from '@/api/client-core'
import * as api from '@/api/storage'
import { captureSession, isSessionCurrent, sessionGeneration } from '@/api/session-context'
import { storageAccessDenied, storageErrorMessage } from '@/api/storage-errors'
import { hasStorageRuntime, type StorageSnapshotStatus } from '@/utils/storage-availability'
import { t } from '@/i18n'
import { canManageOrganization, organizationRoles } from '@/utils/organization-scope'
import {
  getStorageContractGate,
  isSafeStorageId,
  storageActionAllowed,
  storageProjectWritable,
} from '@/utils/storage-contract'

export type StorageTargetContext =
  | { kind: 'create'; organizationId: number | null }
  | { kind: 'project'; project: ApiSchemas['Project']; purpose: 'bind' | 'migrate' }
  | { kind: 'project'; project: ApiSchemas['Project']; purpose: 'repair'; sourceRevisionId: number }

export function storageTargetContextAllowed(context: StorageTargetContext | null): boolean {
  if (!context || !getStorageContractGate('targetDiscovery').available) return false
  if (context.kind === 'create')
    return (
      context.organizationId === null ||
      (isSafeStorageId(context.organizationId) &&
        canManageOrganization(organizationRoles.value[context.organizationId]))
    )
  if (context.purpose === 'repair' && !isSafeStorageId(context.sourceRevisionId)) return false
  return storageActionAllowed(
    context.project,
    context.purpose === 'migrate' ? 'migration' : context.purpose,
  )
}

export function storageTargetContextKey(context: StorageTargetContext | null): string {
  if (!context) return ''
  if (context.kind === 'create') return `create:${context.organizationId ?? 'user'}`
  return [
    context.project.id,
    context.project.owner_user_id,
    context.project.owner_org_id,
    context.purpose,
    context.purpose === 'repair' ? context.sourceRevisionId : '',
  ].join(':')
}

export function storageTargetSnapshotKey(context: StorageTargetContext | null): string {
  return [
    storageTargetContextKey(context),
    context?.kind === 'project'
      ? `${context.project.storage_generation}:${context.project.storage_state}`
      : '',
  ].join(':')
}

export function watchStorageTargetContext(
  context: () => StorageTargetContext | null,
  allowed: () => boolean,
  targets: Pick<ReturnType<typeof createStorageTargets>, 'clear' | 'refresh'>,
) {
  return watch(
    [
      () => storageTargetContextKey(context()),
      () => sessionGeneration.value,
      () => storageTargetSnapshotKey(context()),
      allowed,
    ],
    (next, previous) => {
      if (next[0] !== previous[0] || next[1] !== previous[1]) targets.clear()
      void targets.refresh()
    },
    { immediate: true, flush: 'sync' },
  )
}

/** Discovery contains safe metadata only. A selection is never a promise that a write will succeed. */
export function createStorageTargets(
  options: {
    context: () => StorageTargetContext | null
    available?: () => boolean
  },
  transport: Pick<typeof api, 'getStorageOptions' | 'getProjectStorageOptions'> = api,
) {
  const response = shallowRef<ApiSchemas['StorageOptions'] | null>(null)
  const selectedId = ref<number | null>(null)
  const loading = ref(false)
  const error = ref<string | null>(null)
  const status = ref<StorageSnapshotStatus>('idle')
  const retainedSelection = shallowRef<ApiSchemas['StorageOption'] | null>(null)
  let sequence = 0
  const responseKey = ref('')
  let identity = ''
  let owner = captureSession()
  let discovered = false
  let chosen = false
  let controller = new AbortController()
  const allowed = () => options.available?.() ?? storageTargetContextAllowed(options.context())
  const selected = computed(
    () =>
      response.value?.items.find(
        (item) =>
          item.space_id === selectedId.value && item.selectable && isSafeStorageId(item.space_id),
      ) ?? null,
  )
  const valid = computed(
    () =>
      allowed() &&
      isSessionCurrent(owner) &&
      responseKey.value === storageTargetSnapshotKey(options.context()) &&
      status.value === 'ready' &&
      selected.value !== null,
  )
  const selectionUnavailable = computed(() => selectedId.value !== null && !valid.value)

  function select(value: number | null) {
    chosen = true
    selectedId.value =
      allowed() &&
      response.value?.items.some(
        (item) => item.space_id === value && item.selectable && isSafeStorageId(value),
      )
        ? value
        : null
    retainedSelection.value =
      response.value?.items.find((item) => item.space_id === selectedId.value) ?? null
  }
  function invalidate() {
    ++sequence
    controller.abort()
    controller = new AbortController()
    status.value = response.value ? 'stale' : 'idle'
    loading.value = false
  }
  function clear() {
    invalidate()
    response.value = null
    responseKey.value = ''
    selectedId.value = null
    loading.value = false
    error.value = null
    status.value = 'idle'
    retainedSelection.value = null
    discovered = false
    chosen = false
    identity = ''
  }
  async function refresh(): Promise<boolean> {
    const context = options.context()
    const nextIdentity = storageTargetContextKey(context)
    if (!isSessionCurrent(owner) || identity !== nextIdentity) clear()
    else invalidate()
    owner = captureSession()
    identity = nextIdentity
    if (!context || !allowed()) {
      if (
        context &&
        (context.kind === 'project'
          ? !storageProjectWritable(context.project)
          : context.organizationId !== null &&
            !canManageOrganization(organizationRoles.value[context.organizationId]))
      )
        clear()
      return false
    }
    const session = captureSession()
    const key = storageTargetSnapshotKey(context)
    const request = sequence
    const current = () =>
      request === sequence &&
      isSessionCurrent(session) &&
      allowed() &&
      key === storageTargetSnapshotKey(options.context())
    loading.value = true
    status.value = 'loading'
    error.value = null
    try {
      const result =
        context.kind === 'create'
          ? await transport.getStorageOptions(
              context.organizationId === null
                ? { kind: 'user' }
                : { kind: 'org', id: context.organizationId },
              { signal: controller.signal },
            )
          : await transport.getProjectStorageOptions(
              context.project.id,
              context.purpose === 'repair'
                ? { purpose: 'repair', source_revision_id: context.sourceRevisionId }
                : { purpose: context.purpose },
              { signal: controller.signal },
            )
      if (!current()) return false
      responseKey.value = key
      response.value = result
      if (!hasStorageRuntime(result)) {
        status.value = 'unsupported'
        error.value = t('storageProject.contractIncomplete')
        return false
      }
      status.value = 'ready'
      // A changed response cannot replace an existing choice, including a deliberate clear.
      if (
        !discovered &&
        !chosen &&
        result.default_unavailable_reason === null &&
        result.policy.default_choice !== 'user'
      )
        select(result.default_space_id)
      discovered = true
      retainedSelection.value =
        result.items.find((item) => item.space_id === selectedId.value) ?? retainedSelection.value
      return true
    } catch (cause) {
      if (current()) {
        if (storageAccessDenied(cause)) clear()
        status.value = 'error'
        error.value = storageErrorMessage(cause)
      }
      return false
    } finally {
      if (request === sequence) loading.value = false
    }
  }
  return {
    response,
    selectedId,
    selected,
    valid,
    loading,
    error,
    status,
    retainedSelection,
    selectionUnavailable,
    select,
    clear,
    invalidate,
    refresh,
  }
}
