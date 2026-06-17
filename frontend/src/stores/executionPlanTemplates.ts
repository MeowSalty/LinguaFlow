import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import {
  type ApiSchemas,
  createExecutionPlanTemplate as createRequest,
  deleteExecutionPlanTemplate as deleteRequest,
  fetchExecutionPlanTemplates,
  updateExecutionPlanTemplate as updateRequest,
} from '@/api/client'
import { t } from '@/i18n'

type ExecutionPlanTemplate = ApiSchemas['ExecutionPlanTemplate']
type CreateRequest = ApiSchemas['CreateExecutionPlanTemplateRequest']
type UpdateRequest = ApiSchemas['UpdateExecutionPlanTemplateRequest']
type Scope = ExecutionPlanTemplate['scope']

const includesNormalized = (source: string | undefined, query: string): boolean => {
  return source?.toLowerCase().includes(query) ?? false
}

export const useExecutionPlanTemplatesStore = defineStore('executionPlanTemplates', () => {
  // ── 状态 ──
  const items = ref<ExecutionPlanTemplate[]>([])

  const loading = ref(false)
  const creating = ref(false)
  const updating = ref(false)
  const deletingIds = ref<number[]>([])

  const error = ref<string | null>(null)

  const searchQuery = ref('')
  const scopeFilter = ref<Scope | 'all'>('all')

  // ── 计算属性 ──
  const sortedItems = computed(() =>
    [...items.value].sort(
      (a, b) => new Date(b.created_at ?? '').getTime() - new Date(a.created_at ?? '').getTime(),
    ),
  )

  const filteredItems = computed(() => {
    const query = searchQuery.value.trim().toLowerCase()

    return sortedItems.value.filter((item) => {
      const matchesScope = scopeFilter.value === 'all' || item.scope === scopeFilter.value
      const matchesQuery =
        query.length === 0 ||
        includesNormalized(item.name, query) ||
        includesNormalized(item.description, query)

      return matchesScope && matchesQuery
    })
  })

  const totalCount = computed(() => items.value.length)
  const systemCount = computed(() => items.value.filter((i) => i.scope === 'system').length)
  const userCount = computed(() => items.value.filter((i) => i.scope === 'user').length)
  const orgCount = computed(() => items.value.filter((i) => i.scope === 'org').length)

  // ── 轮次统计 ──
  const avgRoundsPerPlan = computed(() => {
    if (items.value.length === 0) return 0
    const totalRounds = items.value.reduce((sum, i) => sum + (i.rounds?.length ?? 0), 0)
    return Math.round((totalRounds / items.value.length) * 10) / 10
  })

  const maxRoundsPlan = computed(() => {
    if (items.value.length === 0) return null
    return items.value.reduce(
      (max, i) => ((i.rounds?.length ?? 0) > (max.rounds?.length ?? 0) ? i : max),
      items.value[0]!,
    )
  })

  // ── 方法 ──
  const loadTemplates = async (): Promise<void> => {
    loading.value = true
    error.value = null

    try {
      const response = await fetchExecutionPlanTemplates()
      items.value = response.items
    } catch (loadError) {
      error.value =
        loadError instanceof Error
          ? loadError.message
          : t('api.errors.fetchExecutionPlanTemplatesFailed')
    } finally {
      loading.value = false
    }
  }

  const createTemplate = async (payload: CreateRequest): Promise<ExecutionPlanTemplate> => {
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
          : t('api.errors.createExecutionPlanTemplateFailed')
      throw submitError
    } finally {
      creating.value = false
    }
  }

  const updateTemplate = async (
    templateId: number,
    payload: UpdateRequest,
  ): Promise<ExecutionPlanTemplate> => {
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
          : t('api.errors.updateExecutionPlanTemplateFailed')
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
          : t('api.errors.deleteExecutionPlanTemplateFailed')
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
    sortedItems,
    filteredItems,
    totalCount,
    systemCount,
    userCount,
    orgCount,
    avgRoundsPerPlan,
    maxRoundsPlan,
    loadTemplates,
    createTemplate,
    updateTemplate,
    deleteTemplate,
  }
})
