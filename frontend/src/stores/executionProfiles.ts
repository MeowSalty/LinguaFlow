import { defineStore } from 'pinia'
import type { ApiSchemas } from '@/api/client'
import {
  fetchExecutionProfiles,
  createExecutionProfile,
  updateExecutionProfile,
  deleteExecutionProfile,
} from '@/api/execution-profiles'
import { createScopedEntityState } from './scopedEntity'

export const useExecutionProfilesStore = defineStore('executionProfiles', () => {
  const state = createScopedEntityState<
    ApiSchemas['ExecutionProfile'],
    ApiSchemas['CreateExecutionProfileRequest'],
    ApiSchemas['UpdateExecutionProfileRequest']
  >({
    list: (orgId, signal) => fetchExecutionProfiles(undefined, orgId ?? undefined, signal),
    create: (body, orgId) =>
      createExecutionProfile({ ...body, ...(orgId == null ? {} : { org_id: orgId }) }),
    update: (id, body) => updateExecutionProfile(id, body),
    remove: (id) => deleteExecutionProfile(id),
  })
  return {
    ...state,
    loadProfiles: state.load,
    createProfile: state.create,
    updateProfile: state.update,
    deleteProfile: state.remove,
  }
})
