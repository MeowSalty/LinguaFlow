import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import {
  type ApiSchemas,
  createPrunePromptTemplate as createRequest,
  deletePrunePromptTemplate as deleteRequest,
  fetchPrunePromptTemplates,
  updatePrunePromptTemplate as updateRequest,
} from '@/api/client'
import { t } from '@/i18n'

type PrunePromptTemplate = ApiSchemas['PrunePromptTemplate']
type CreateRequest = ApiSchemas['CreatePrunePromptTemplateRequest']
type UpdateRequest = ApiSchemas['UpdatePrunePromptTemplateRequest']
type Scope = PrunePromptTemplate['scope']

const includesNormalized = (source: string | undefined, query: string): boolean =>
  source?.toLowerCase().includes(query) ?? false

export const usePrunePromptTemplatesStore = defineStore('prunePromptTemplates', () => {
  const items = ref<PrunePromptTemplate[]>([])
  const loading = ref(false)
  const creating = ref(false)
  const updating = ref(false)
  const deletingIds = ref<number[]>([])
  const error = ref<string | null>(null)
  const searchQuery = ref('')
  const scopeFilter = ref<Scope | 'all'>('all')

  const sortedItems = computed(() =>
    [...items.value].sort(
      (a, b) => new Date(b.updated_at ?? '').getTime() - new Date(a.updated_at ?? '').getTime(),
    ),
  )

  const filteredItems = computed(() => {
    const query = searchQuery.value.trim().toLowerCase()
    return sortedItems.value.filter(
      (item) =>
        (scopeFilter.value === 'all' || item.scope === scopeFilter.value) &&
        (!query ||
          includesNormalized(item.name, query) ||
          includesNormalized(item.description, query)),
    )
  })

  const loadTemplates = async (): Promise<void> => {
    loading.value = true
    error.value = null
    try {
      items.value = (await fetchPrunePromptTemplates()).items
    } catch (loadError) {
      error.value =
        loadError instanceof Error
          ? loadError.message
          : t('api.errors.fetchPrunePromptTemplatesFailed')
    } finally {
      loading.value = false
    }
  }

  const createTemplate = async (payload: CreateRequest): Promise<PrunePromptTemplate> => {
    creating.value = true
    error.value = null
    try {
      const template = await createRequest(payload)
      items.value = [template, ...items.value.filter((item) => item.id !== template.id)]
      return template
    } catch (submitError) {
      error.value =
        submitError instanceof Error
          ? submitError.message
          : t('api.errors.createPrunePromptTemplateFailed')
      throw submitError
    } finally {
      creating.value = false
    }
  }

  const updateTemplate = async (
    templateId: number,
    payload: UpdateRequest,
  ): Promise<PrunePromptTemplate> => {
    updating.value = true
    error.value = null
    try {
      const template = await updateRequest(templateId, payload)
      items.value = items.value.map((item) => (item.id === template.id ? template : item))
      return template
    } catch (submitError) {
      error.value =
        submitError instanceof Error
          ? submitError.message
          : t('api.errors.updatePrunePromptTemplateFailed')
      throw submitError
    } finally {
      updating.value = false
    }
  }

  const deleteTemplate = async (templateId: number): Promise<void> => {
    deletingIds.value = [...deletingIds.value, templateId]
    error.value = null
    try {
      await deleteRequest(templateId)
      items.value = items.value.filter((item) => item.id !== templateId)
    } catch (submitError) {
      error.value =
        submitError instanceof Error
          ? submitError.message
          : t('api.errors.deletePrunePromptTemplateFailed')
      throw submitError
    } finally {
      deletingIds.value = deletingIds.value.filter((id) => id !== templateId)
    }
  }

  return {
    items,
    loading,
    creating,
    updating,
    deletingIds,
    error,
    searchQuery,
    scopeFilter,
    filteredItems,
    loadTemplates,
    createTemplate,
    updateTemplate,
    deleteTemplate,
  }
})
