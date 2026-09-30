import { defineStore } from 'pinia'
import type { ApiSchemas } from '@/api/client'
import {
  fetchPromptTemplates,
  createPromptTemplate,
  updatePromptTemplate,
  deletePromptTemplate,
} from '@/api/prompt-templates'
import { createScopedEntityState } from './scopedEntity'

export const usePromptTemplatesStore = defineStore('promptTemplates', () => {
  const state = createScopedEntityState<
    ApiSchemas['TranslationPromptTemplate'],
    ApiSchemas['CreateTranslationPromptTemplateRequest'],
    ApiSchemas['UpdateTranslationPromptTemplateRequest']
  >({
    list: (orgId, signal) => fetchPromptTemplates(undefined, orgId ?? undefined, signal),
    create: (body, orgId) =>
      createPromptTemplate({ ...body, ...(orgId == null ? {} : { org_id: orgId }) }),
    update: (id, body) => updatePromptTemplate(id, body),
    remove: (id) => deletePromptTemplate(id),
  })
  return {
    ...state,
    loadTemplates: state.load,
    createTemplate: state.create,
    updateTemplate: state.update,
    deleteTemplate: state.remove,
  }
})
