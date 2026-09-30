import { computed, onScopeDispose, ref, shallowRef } from 'vue'
import { defineStore } from 'pinia'
import type { ApiSchemas } from '@/api/client'
import * as api from '@/api/organizations'
import { captureSession, isSessionCurrent, onSessionChange } from '@/api/session-context'
import {
  canManageOrganization,
  invalidateOrganization,
  organizationRoles,
} from '@/utils/organization-scope'
import { t } from '@/i18n'
import { isAccessDenied } from '@/api/utils'

export const useOrganizationsStore = defineStore('organizations', () => {
  const items = shallowRef<ApiSchemas['Organization'][]>([])
  const loading = ref(false)
  const error = ref<string | null>(null)
  let generation = 0
  let inFlight: Promise<void> | null = null
  const writable = computed(() =>
    items.value.filter((org) => canManageOrganization(org.current_user_role)),
  )
  const replace = (next: ApiSchemas['Organization'][]) => {
    const version = generation
    organizationRoles.value = Object.fromEntries(next.map((org) => [org.id, org.current_user_role]))
    for (const org of items.value) {
      const fresh = next.find((value) => value.id === org.id)
      if (!fresh || fresh.current_user_role !== org.current_user_role)
        invalidateOrganization(org.id)
      if (version !== generation) return
    }
    items.value = next
  }
  const clear = () => {
    ++generation
    inFlight = null
    items.value = []
    organizationRoles.value = {}
    error.value = null
    loading.value = false
  }
  onScopeDispose(onSessionChange(clear))
  const refresh = (): Promise<void> => {
    if (inFlight) return inFlight
    const session = captureSession()
    const request = ++generation
    loading.value = true
    error.value = null
    const current = () => request === generation && isSessionCurrent(session)
    const work = async () => {
      try {
        const response = await api.fetchOrganizations()
        if (current()) replace(response.items)
      } catch (cause) {
        if (current()) {
          if (isAccessDenied(cause)) replace([])
          error.value = cause instanceof Error ? cause.message : t('team.errors.load')
        }
      } finally {
        if (current()) {
          loading.value = false
          inFlight = null
        }
      }
    }
    inFlight = work()
    return inFlight
  }
  const remove = (id: number) => replace(items.value.filter((org) => org.id !== id))
  const accept = (org: ApiSchemas['Organization']) =>
    replace([...items.value.filter((value) => value.id !== org.id), org])
  const canWrite = (id: number) => canManageOrganization(organizationRoles.value[id])
  return { items, writable, loading, error, refresh, remove, accept, canWrite, clear }
})
