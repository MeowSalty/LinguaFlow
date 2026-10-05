import type { ApiSchemas } from '@/api/client-core'

export const storageRuntime = (
  deployment_enabled = true,
  maintenance = false,
): ApiSchemas['StorageRuntime'] => ({ deployment_enabled, maintenance })
export const storageAction = (
  allowed = true,
  reason_codes: ApiSchemas['StorageActionReasonCode'][] = [],
): ApiSchemas['StorageActionAvailability'] => ({
  allowed,
  reason_codes: allowed ? [] : reason_codes.length ? reason_codes : ['storage_deployment_disabled'],
})
export const connectionActions = (): ApiSchemas['StorageConnectionManagementActions'] => ({
  create_space: storageAction(),
  authorize_read: storageAction(),
  authorize_write: storageAction(),
  check_read: storageAction(),
  check_write: storageAction(),
  revoke_auth: storageAction(),
  set_status: storageAction(),
})
export const spaceActions = (): ApiSchemas['StorageSpaceManagementActions'] => ({
  set_status: storageAction(),
})
export const storageCapabilities = (
  scope: 'user' | 'org' = 'user',
  owner_id = 1,
): ApiSchemas['StorageCapabilities'] => ({
  scope,
  owner_id,
  runtime: storageRuntime(),
  management_actions: { create_connection: storageAction() },
})
export const policyCapabilities = () => ({
  runtime: storageRuntime(),
  allowed_policy_modes: [
    'site_only',
    'both',
    'user_required',
  ] as ApiSchemas['StoragePolicy']['allowed_policy_modes'],
  policy_restriction_codes: [] as ApiSchemas['StoragePolicy']['policy_restriction_codes'],
})
