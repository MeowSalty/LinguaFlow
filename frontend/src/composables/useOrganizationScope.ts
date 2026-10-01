import { computed, onMounted, onUnmounted, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useOrganizationsStore } from '@/stores/organizations'
import { useServiceStore } from '@/stores/service'
import { parseOrganizationId } from '@/utils/organization-scope'

/** Scope is explicit in this page's URL; it never changes global discovery. */
export function useOrganizationScope(onChange?: (orgId: number | null) => void) {
  const route = useRoute()
  const router = useRouter()
  const organizations = useOrganizationsStore()
  const service = useServiceStore()
  // 0 is an invalid explicit scope; it must never silently widen into the all-resources view.
  const orgId = computed(() =>
    route.query.org_id === undefined ? null : (parseOrganizationId(route.query.org_id) ?? 0),
  )
  const invalid = computed(
    () =>
      route.query.org_id !== undefined &&
      (orgId.value === null ||
        (!organizations.loading &&
          !organizations.error &&
          !organizations.items.some((org) => org.id === orgId.value))),
  )
  const canWrite = computed(
    () => !invalid.value && (orgId.value === null || organizations.canWrite(orgId.value)),
  )
  const refresh = () => {
    if (!service.isLocal) void organizations.refresh()
  }
  const visibility = () => {
    if (document.visibilityState === 'visible') refresh()
  }
  onMounted(() => {
    refresh()
    document.addEventListener('visibilitychange', visibility)
  })
  onUnmounted(() => document.removeEventListener('visibilitychange', visibility))
  watch(orgId, (id) => onChange?.(id), { immediate: true })
  const setScope = (id: number | null) =>
    router.replace({ query: { ...route.query, org_id: id === null ? undefined : String(id) } })
  return { orgId, invalid, canWrite, organizations, setScope }
}
