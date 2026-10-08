import { defineStore } from 'pinia'
import type { ApiSchemas } from '@/api/client'
import {
  fetchExecutionPlanTemplates,
  createExecutionPlanTemplate,
  updateExecutionPlanTemplate,
  deleteExecutionPlanTemplate,
} from '@/api/execution-plan-templates'
import { createScopedEntityState } from './scopedEntity'

export const useExecutionPlanTemplatesStore = defineStore('executionPlanTemplates', () => {
  const state = createScopedEntityState<
    ApiSchemas['ExecutionPlanTemplate'],
    ApiSchemas['CreateExecutionPlanTemplateRequest'],
    ApiSchemas['UpdateExecutionPlanTemplateRequest']
  >({
    list: (orgId, signal) => fetchExecutionPlanTemplates(undefined, orgId ?? undefined, signal),
    create: (body, orgId) =>
      createExecutionPlanTemplate({ ...body, ...(orgId == null ? {} : { org_id: orgId }) }),
    update: (id, body) => updateExecutionPlanTemplate(id, body),
    remove: (id) => deleteExecutionPlanTemplate(id),
  })
  return {
    ...state,
    loadTemplates: state.load,
    createTemplate: state.create,
    updateTemplate: state.update,
    deleteTemplate: state.remove,
  }
})
