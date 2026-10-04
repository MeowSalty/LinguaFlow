import { computed, ref, shallowRef } from 'vue'
import type { ApiSchemas } from '@/api/client-core'
import * as api from '@/api/storage'
import { captureSession, isSessionCurrent } from '@/api/session-context'
import { storageErrorMessage } from '@/api/storage-errors'
import { canManageOrganization, organizationRoles } from '@/utils/organization-scope'
import {
  getStorageContractGate,
  isSafeStorageId,
  storageActionAllowed,
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
    context.project.storage_generation,
    context.project.storage_state,
    context.project.owner_user_id,
    context.project.owner_org_id,
    context.purpose,
    context.purpose === 'repair' ? context.sourceRevisionId : '',
  ].join(':')
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
  let sequence = 0
  let responseKey = ''
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
      responseKey === storageTargetContextKey(options.context()) &&
      !loading.value &&
      !error.value &&
      selected.value !== null,
  )

  function select(value: number | null) {
    selectedId.value =
      allowed() &&
      response.value?.items.some(
        (item) => item.space_id === value && item.selectable && isSafeStorageId(value),
      )
        ? value
        : null
  }
  function clear() {
    ++sequence
    controller.abort()
    controller = new AbortController()
    response.value = null
    responseKey = ''
    selectedId.value = null
    loading.value = false
    error.value = null
  }
  async function refresh(): Promise<boolean> {
    const context = options.context()
    const previousSelection = selectedId.value
    clear()
    if (!context || !allowed()) return false
    const session = captureSession()
    const key = storageTargetContextKey(context)
    const request = sequence
    const current = () =>
      request === sequence &&
      isSessionCurrent(session) &&
      allowed() &&
      key === storageTargetContextKey(options.context())
    loading.value = true
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
      responseKey = key
      response.value = result
      // Preserve a deliberate valid choice, but never invent a replacement for an unavailable default.
      if (
        previousSelection !== null &&
        result.items.some((item) => item.space_id === previousSelection && item.selectable)
      )
        select(previousSelection)
      else if (
        result.default_unavailable_reason === null &&
        result.policy.default_choice !== 'user'
      )
        select(result.default_space_id)
      return true
    } catch (cause) {
      if (current()) error.value = storageErrorMessage(cause)
      return false
    } finally {
      if (request === sequence) loading.value = false
    }
  }
  return { response, selectedId, selected, valid, loading, error, select, clear, refresh }
}
