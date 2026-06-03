import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import {
  type ApiSchemas,
  createTemplate as createTemplateRequest,
  deleteTemplate as deleteTemplateRequest,
  fetchTemplates,
  updateTemplate as updateTemplateRequest,
} from '@/api/client'
import { t } from '@/i18n'

type TranslationTemplate = ApiSchemas['TranslationTemplate']
type CreateTemplatePayload = ApiSchemas['CreateTemplateRequest']
type UpdateTemplatePayload = ApiSchemas['UpdateTemplateRequest']
type TemplateScope = TranslationTemplate['scope']

const includesNormalized = (source: string | undefined, query: string): boolean => {
  return source?.toLowerCase().includes(query) ?? false
}

export const useTemplatesStore = defineStore('templates', () => {
  const items = ref<TranslationTemplate[]>([])

  const loading = ref(false)
  const creating = ref(false)
  const updating = ref(false)
  const deletingTemplateIds = ref<number[]>([])

  const error = ref<string | null>(null)
  const createError = ref<string | null>(null)
  const updateError = ref<string | null>(null)
  const deleteError = ref<string | null>(null)

  const searchQuery = ref('')
  const scopeFilter = ref<TemplateScope | 'all'>('all')

  const sortedItems = computed(() =>
    [...items.value].sort(
      (left, right) =>
        new Date(right.created_at ?? '').getTime() - new Date(left.created_at ?? '').getTime(),
    ),
  )

  const filteredItems = computed(() => {
    const query = searchQuery.value.trim().toLowerCase()

    return sortedItems.value.filter((template) => {
      const matchesScope = scopeFilter.value === 'all' || template.scope === scopeFilter.value
      const matchesQuery =
        query.length === 0 ||
        includesNormalized(template.name, query) ||
        includesNormalized(template.description, query)

      return matchesScope && matchesQuery
    })
  })

  const totalCount = computed(() => items.value.length)
  const builtinCount = computed(() => items.value.filter((t) => t.is_builtin).length)
  const userCount = computed(() => items.value.filter((t) => t.scope === 'user').length)
  const orgCount = computed(() => items.value.filter((t) => t.scope === 'org').length)

  const loadTemplates = async (): Promise<void> => {
    loading.value = true
    error.value = null

    try {
      const response = await fetchTemplates()
      items.value = response.items
    } catch (loadError) {
      error.value =
        loadError instanceof Error ? loadError.message : t('api.errors.fetchTemplatesFailed')
    } finally {
      loading.value = false
    }
  }

  const createTemplate = async (payload: CreateTemplatePayload): Promise<TranslationTemplate> => {
    creating.value = true
    createError.value = null

    try {
      const template = await createTemplateRequest(payload)
      items.value = [template, ...items.value.filter((item) => item.id !== template.id)]
      return template
    } catch (submitError) {
      createError.value =
        submitError instanceof Error ? submitError.message : t('api.errors.createTemplateFailed')
      throw submitError
    } finally {
      creating.value = false
    }
  }

  const updateTemplate = async (
    templateId: number,
    payload: UpdateTemplatePayload,
  ): Promise<TranslationTemplate> => {
    updating.value = true
    updateError.value = null

    try {
      const template = await updateTemplateRequest(templateId, payload)
      items.value = items.value.map((item) => (item.id === template.id ? template : item))
      return template
    } catch (submitError) {
      updateError.value =
        submitError instanceof Error ? submitError.message : t('api.errors.updateTemplateFailed')
      throw submitError
    } finally {
      updating.value = false
    }
  }

  const deleteTemplate = async (templateId: number): Promise<void> => {
    deletingTemplateIds.value = [...deletingTemplateIds.value, templateId]
    deleteError.value = null

    try {
      await deleteTemplateRequest(templateId)
      items.value = items.value.filter((item) => item.id !== templateId)
    } catch (submitError) {
      deleteError.value =
        submitError instanceof Error ? submitError.message : t('api.errors.deleteTemplateFailed')
      throw submitError
    } finally {
      deletingTemplateIds.value = deletingTemplateIds.value.filter((id) => id !== templateId)
    }
  }

  return {
    items,
    loading,
    creating,
    updating,
    deletingTemplateIds,
    error,
    createError,
    updateError,
    deleteError,
    searchQuery,
    scopeFilter,
    sortedItems,
    filteredItems,
    totalCount,
    builtinCount,
    userCount,
    orgCount,
    loadTemplates,
    createTemplate,
    updateTemplate,
    deleteTemplate,
  }
})
