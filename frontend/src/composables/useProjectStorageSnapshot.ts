import { computed, onScopeDispose, shallowRef, watch } from 'vue'
import type { ApiSchemas } from '@/api/client-core'
import { getProjectStorage } from '@/api/storage'
import { captureSession, isSessionCurrent, sessionGeneration } from '@/api/session-context'
import { storageAccessDenied, storageErrorMessage } from '@/api/storage-errors'
import { hasStorageRuntime, type StorageSnapshotStatus } from '@/utils/storage-availability'
import { subscribeStorageRefresh } from '@/utils/storage-snapshots'
import { isSafeStorageId } from '@/utils/storage-contract'
import { t } from '@/i18n'

/** Safe project metadata, independently refreshed from drafts, tasks, and DB content. */
export function useProjectStorageSnapshot(
  project: () => ApiSchemas['Project'] | null | undefined,
  active: () => boolean = () => true,
) {
  const value = shallowRef<ApiSchemas['ProjectStorage'] | null>(null)
  const status = shallowRef<StorageSnapshotStatus>('idle')
  const error = shallowRef<string | null>(null)
  let sequence = 0
  let controller = new AbortController()
  const ready = computed(
    () =>
      status.value === 'ready' &&
      value.value?.project_id === project()?.id &&
      value.value?.storage_generation === project()?.storage_generation,
  )
  // Deployment is explanatory. Only the binding's explicit reason can block its new content;
  // deletion and dedicated repair/migration targets have their own admission rules.
  const contentWritable = computed(
    () =>
      ready.value &&
      !value.value?.runtime.maintenance &&
      !value.value?.reason_codes.some((code) =>
        ['storage_deployment_disabled', 'byos_disabled'].includes(code),
      ),
  )
  function invalidate() {
    ++sequence
    controller.abort()
    controller = new AbortController()
    status.value = value.value ? 'stale' : 'idle'
  }
  async function refresh() {
    invalidate()
    const id = project()?.id
    if (!active() || !isSafeStorageId(id)) return false
    const request = sequence,
      session = captureSession()
    const current = () => request === sequence && isSessionCurrent(session) && id === project()?.id
    status.value = 'loading'
    error.value = null
    try {
      const result = await getProjectStorage(id, { signal: controller.signal })
      if (!current()) return false
      if (result.project_id !== id || !hasStorageRuntime(result)) {
        status.value = 'unsupported'
        error.value = t('storageProject.contractIncomplete')
        return false
      }
      value.value = result
      status.value = 'ready'
      return true
    } catch (cause) {
      if (current()) {
        if (storageAccessDenied(cause)) value.value = null
        status.value = 'error'
        error.value = storageErrorMessage(cause)
      }
      return false
    }
  }
  watch(
    [
      () => project()?.id,
      () => project()?.owner_org_id,
      () => project()?.owner_user_id,
      () => sessionGeneration.value,
    ],
    () => {
      invalidate()
      value.value = null
      error.value = null
      void refresh()
    },
    { immediate: true, flush: 'sync' },
  )
  watch(
    [() => project()?.storage_generation, () => project()?.storage_state, active],
    () => void refresh(),
    { flush: 'sync' },
  )
  onScopeDispose(
    subscribeStorageRefresh({
      scope: () => ({ projectId: project()?.id, organizationId: project()?.owner_org_id }),
      invalidate,
      refresh,
    }),
  )
  onScopeDispose(invalidate)
  return { value, status, error, ready, contentWritable, invalidate, refresh }
}
