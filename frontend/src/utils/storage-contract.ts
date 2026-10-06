import type { ApiSchemas } from '@/api/client-core'
import { isLocalMode } from '@/api/client-core'
import { getSessionUserId, sessionGeneration } from '@/api/session-context'
import { canManageOrganization, organizationRoles } from './organization-scope'
import { ApiError } from '@/api/utils'
import { t } from '@/i18n'

export const isSafeStorageId = (value: unknown): value is number =>
  typeof value === 'number' && Number.isSafeInteger(value) && value > 0

export const isStorageGeneration = (value: unknown): value is number =>
  typeof value === 'number' && Number.isSafeInteger(value) && value >= 0

export const requireStorageId = (value: unknown): number => {
  if (!isSafeStorageId(value)) throw new ApiError(t('storageErrors.invalidIdentity'))
  return value
}

export const requireStorageGeneration = (value: unknown): number => {
  if (!isStorageGeneration(value)) throw new ApiError(t('storageErrors.invalidGeneration'))
  return value
}

export const requireIdempotencyKey = (key: string): string => {
  if (typeof key !== 'string' || !key.trim() || key.length > 200 || /[\r\n]/.test(key))
    throw new ApiError(t('storageErrors.invalidIdentity'))
  return key
}

// Contract packages verified against the synchronized C01–C09 working-tree bundle.
// Runtime role, generation and maintenance checks remain mandatory below.
const blockers = {
  targetDiscovery: ['C01', 'C02'],
  sourceUpdate: ['C03', 'C04', 'C06'],
  repair: ['C01', 'C02', 'C04', 'C05', 'C06'],
  revoke: ['C04'],
  writeCheck: ['C07'],
  migration: ['C01', 'C02', 'C04', 'C06'],
  exportMutation: ['C04', 'C06'],
  manifestWrite: ['C04'],
} as const

export type StorageCapability = keyof typeof blockers
const implemented: Record<StorageCapability, boolean> = {
  targetDiscovery: true,
  sourceUpdate: true,
  repair: true,
  revoke: true,
  writeCheck: true,
  migration: true,
  exportMutation: true,
  manifestWrite: true,
}
export const getStorageContractGate = (capability: StorageCapability) => ({
  available: implemented[capability],
  reasonKey: 'storageErrors.contractPending' as const,
  issues: (implemented[capability] ? [] : blockers[capability]) as readonly string[],
})

export const storageProjectWritable = (
  project: ApiSchemas['Project'] | null | undefined,
): boolean => {
  void sessionGeneration.value
  if (!project || !isSafeStorageId(project.id)) return false
  if (project.owner_org_id != null)
    return (
      isSafeStorageId(project.owner_org_id) &&
      canManageOrganization(organizationRoles.value[project.owner_org_id])
    )
  return (
    isLocalMode() ||
    (isSafeStorageId(project.owner_user_id) && project.owner_user_id === getSessionUserId())
  )
}

export type StorageProjectAction =
  | 'upload'
  | 'delete'
  | 'bind'
  | 'sourceUpdate'
  | 'repair'
  | 'migration'
  | 'exportMutation'
export const storageActionAllowed = (
  project: ApiSchemas['Project'] | null | undefined,
  action: StorageProjectAction,
): boolean => {
  if (
    !project ||
    !storageProjectWritable(project) ||
    !isStorageGeneration(project.storage_generation)
  )
    return false
  const capability =
    action === 'upload' || action === 'delete'
      ? 'manifestWrite'
      : action === 'bind'
        ? 'targetDiscovery'
        : action
  if (!getStorageContractGate(capability).available) return false
  if (action === 'repair')
    return ['active', 'draining', 'migrating'].includes(project.storage_state)
  return project.storage_state === 'active'
}

export const storageManifestWriteAllowed = (project: ApiSchemas['Project'] | null | undefined) =>
  storageActionAllowed(project, 'upload')

export const storageTaskCommitted = (task: ApiSchemas['StorageTask']): boolean =>
  task.status === 'completed' && task.phase === 'committed'
