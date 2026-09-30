import { onScopeDispose, shallowRef } from 'vue'
import { onSessionChange } from '@/api/session-context'

export type OrganizationRole = 'owner' | 'admin' | 'member'
export const organizationRoles = shallowRef<Record<number, OrganizationRole>>({})
// Module state outlives disposed Pinia stores; clear it on every identity generation.
onSessionChange(() => {
  organizationRoles.value = {}
})
const listeners = new Set<(orgId: number) => void>()
export const canManageOrganization = (role?: OrganizationRole): boolean =>
  role === 'owner' || role === 'admin'
export const canRemoveMember = (
  role: OrganizationRole,
  targetRole: OrganizationRole,
  self: boolean,
): boolean => self || role === 'owner' || (role === 'admin' && targetRole === 'member')
export const invalidateOrganization = (orgId: number): void => {
  for (const listener of listeners) listener(orgId)
}
export const onOrganizationInvalidated = (listener: (orgId: number) => void): void => {
  listeners.add(listener)
  onScopeDispose(() => listeners.delete(listener))
}
export const parseOrganizationId = (value: unknown): number | null => {
  if (typeof value !== 'string' || !/^[1-9]\d*$/.test(value)) return null
  const id = Number(value)
  return Number.isSafeInteger(id) ? id : null
}
export const isOrganizationDependency = (
  item: { scope?: string; owner_org_id?: number },
  orgId: number | null,
): boolean => orgId === null || item.scope === 'system' || item.owner_org_id === orgId
