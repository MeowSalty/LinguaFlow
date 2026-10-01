import { defineStore } from 'pinia'
import type { ApiSchemas } from '@/api/client'
import {
  fetchPrunePromptTemplates,
  createPrunePromptTemplate,
  updatePrunePromptTemplate,
  deletePrunePromptTemplate,
} from '@/api/prune-prompt-templates'
import { createScopedEntityState } from './scopedEntity'

export const usePrunePromptTemplatesStore = defineStore('prunePromptTemplates', () => {
  const state = createScopedEntityState<
    ApiSchemas['PrunePromptTemplate'],
    ApiSchemas['CreatePrunePromptTemplateRequest'],
    ApiSchemas['UpdatePrunePromptTemplateRequest']
  >({
    list: (orgId, signal) => fetchPrunePromptTemplates(undefined, orgId ?? undefined, signal),
    create: (body, orgId) =>
      createPrunePromptTemplate({ ...body, ...(orgId == null ? {} : { org_id: orgId }) }),
    update: (id, body) => updatePrunePromptTemplate(id, body),
    remove: (id) => deletePrunePromptTemplate(id),
  })
  return {
    ...state,
    loadTemplates: state.load,
    createTemplate: state.create,
    updateTemplate: state.update,
    deleteTemplate: state.remove,
  }
})
