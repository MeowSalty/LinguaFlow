import { defineStore } from 'pinia'
import type { ApiSchemas } from '@/api/client'
import {
  fetchBootstrapPromptTemplates,
  createBootstrapPromptTemplate,
  updateBootstrapPromptTemplate,
  deleteBootstrapPromptTemplate,
} from '@/api/bootstrap-prompt-templates'
import { createScopedEntityState } from './scopedEntity'

export const useBootstrapPromptTemplatesStore = defineStore('bootstrapPromptTemplates', () => {
  const state = createScopedEntityState<
    ApiSchemas['BootstrapPromptTemplate'],
    ApiSchemas['CreateBootstrapPromptTemplateRequest'],
    ApiSchemas['UpdateBootstrapPromptTemplateRequest']
  >({
    list: (orgId, signal) => fetchBootstrapPromptTemplates(undefined, orgId ?? undefined, signal),
    create: (body, orgId) =>
      createBootstrapPromptTemplate({ ...body, ...(orgId == null ? {} : { org_id: orgId }) }),
    update: (id, body) => updateBootstrapPromptTemplate(id, body),
    remove: (id) => deleteBootstrapPromptTemplate(id),
  })
  return {
    ...state,
    loadTemplates: state.load,
    createTemplate: state.create,
    updateTemplate: state.update,
    deleteTemplate: state.remove,
  }
})
