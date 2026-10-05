import type { ApiSchemas } from '@/api/client-core'

export type StorageSnapshotStatus = 'idle' | 'loading' | 'ready' | 'stale' | 'error' | 'unsupported'
const record = (value: unknown): value is Record<string, unknown> =>
  !!value && typeof value === 'object' && !Array.isArray(value)
const strings = (value: unknown): value is string[] =>
  Array.isArray(value) && value.every((item) => typeof item === 'string')
export const isStorageRuntime = (value: unknown): value is ApiSchemas['StorageRuntime'] =>
  record(value) &&
  typeof value.deployment_enabled === 'boolean' &&
  typeof value.maintenance === 'boolean'
export const isStorageActionAvailability = (
  value: unknown,
): value is ApiSchemas['StorageActionAvailability'] =>
  record(value) &&
  typeof value.allowed === 'boolean' &&
  strings(value.reason_codes) &&
  (value.allowed ? value.reason_codes.length === 0 : value.reason_codes.length > 0)

export const storageConnectionActions = [
  'create_space',
  'authorize_read',
  'authorize_write',
  'check_read',
  'check_write',
  'revoke_auth',
  'set_status',
] as const
export type StorageManagementAction = (typeof storageConnectionActions)[number]
export const hasConnectionManagementActions = (
  value: unknown,
): value is ApiSchemas['StorageConnection'] =>
  record(value) &&
  record(value.management_actions) &&
  storageConnectionActions.every((key) =>
    isStorageActionAvailability((value.management_actions as Record<string, unknown>)[key]),
  )
export const hasSpaceManagementActions = (value: unknown): value is ApiSchemas['StorageSpace'] =>
  record(value) &&
  record(value.management_actions) &&
  isStorageActionAvailability(value.management_actions.set_status)
export const isStorageCapabilities = (value: unknown): value is ApiSchemas['StorageCapabilities'] =>
  record(value) &&
  ['user', 'org'].includes(String(value.scope)) &&
  Number.isSafeInteger(value.owner_id) &&
  Number(value.owner_id) > 0 &&
  isStorageRuntime(value.runtime) &&
  record(value.management_actions) &&
  isStorageActionAvailability(value.management_actions.create_connection)
export const hasStoragePolicyCapabilities = (
  value: unknown,
): value is ApiSchemas['StoragePolicy'] =>
  record(value) &&
  isStorageRuntime(value.runtime) &&
  strings(value.allowed_policy_modes) &&
  value.allowed_policy_modes.length > 0 &&
  value.allowed_policy_modes.every((mode) =>
    ['site_only', 'both', 'user_required'].includes(mode),
  ) &&
  strings(value.policy_restriction_codes)
export const hasStorageRuntime = <T extends { runtime: ApiSchemas['StorageRuntime'] }>(
  value: T | null | undefined,
): value is T => !!value && isStorageRuntime(value.runtime)
export const storageManagementAllowed = (
  action: ApiSchemas['StorageActionAvailability'] | null | undefined,
  ready: boolean,
): boolean => ready && isStorageActionAvailability(action) && action.allowed
