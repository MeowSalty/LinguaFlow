import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import {
  type ApiSchemas,
  createBackend as createBackendRequest,
  deleteBackend as deleteBackendRequest,
  fetchBackends,
  updateBackend as updateBackendRequest,
} from '@/api/client'
import { t } from '@/i18n'

type Backend = ApiSchemas['Backend']
type CreateBackendPayload = ApiSchemas['CreateBackendRequest']
type UpdateBackendPayload = ApiSchemas['UpdateBackendRequest']
type BackendType = Backend['type']

const includesNormalized = (source: string | undefined, query: string): boolean => {
  return source?.toLowerCase().includes(query) ?? false
}

export const useBackendsStore = defineStore('backends', () => {
  const items = ref<Backend[]>([])

  const loading = ref(false)
  const creating = ref(false)
  const updating = ref(false)
  const deletingBackendIds = ref<number[]>([])

  const error = ref<string | null>(null)
  const createError = ref<string | null>(null)
  const updateError = ref<string | null>(null)
  const deleteError = ref<string | null>(null)

  const searchQuery = ref('')
  const typeFilter = ref<BackendType | 'all'>('all')

  const sortedItems = computed(() => [...items.value].sort((left, right) => left.id - right.id))

  const filteredItems = computed(() => {
    const query = searchQuery.value.trim().toLowerCase()

    return sortedItems.value.filter((backend) => {
      const matchesType = typeFilter.value === 'all' || backend.type === typeFilter.value
      const matchesQuery =
        query.length === 0 ||
        includesNormalized(backend.name, query) ||
        includesNormalized(backend.type, query)

      return matchesType && matchesQuery
    })
  })

  const backendCount = computed(() => items.value.length)
  const openaiCount = computed(() => items.value.filter((b) => b.type === 'openai').length)
  const anthropicCount = computed(() => items.value.filter((b) => b.type === 'anthropic').length)
  const googleCount = computed(() => items.value.filter((b) => b.type === 'google').length)

  const loadBackends = async (): Promise<void> => {
    loading.value = true
    error.value = null

    try {
      const response = await fetchBackends()
      items.value = response.items
    } catch (loadError) {
      error.value =
        loadError instanceof Error ? loadError.message : t('api.errors.fetchBackendsFailed')
    } finally {
      loading.value = false
    }
  }

  const createBackend = async (payload: CreateBackendPayload): Promise<Backend> => {
    creating.value = true
    createError.value = null

    try {
      const backend = await createBackendRequest(payload)
      items.value = [backend, ...items.value.filter((item) => item.id !== backend.id)]
      return backend
    } catch (submitError) {
      createError.value =
        submitError instanceof Error ? submitError.message : t('api.errors.createBackendFailed')
      throw submitError
    } finally {
      creating.value = false
    }
  }

  const updateBackend = async (
    backendId: number,
    payload: UpdateBackendPayload,
  ): Promise<Backend> => {
    updating.value = true
    updateError.value = null

    try {
      const backend = await updateBackendRequest(backendId, payload)
      items.value = items.value.map((item) => (item.id === backend.id ? backend : item))
      return backend
    } catch (submitError) {
      updateError.value =
        submitError instanceof Error ? submitError.message : t('api.errors.updateBackendFailed')
      throw submitError
    } finally {
      updating.value = false
    }
  }

  const deleteBackend = async (backendId: number): Promise<void> => {
    deletingBackendIds.value = [...deletingBackendIds.value, backendId]
    deleteError.value = null

    try {
      await deleteBackendRequest(backendId)
      items.value = items.value.filter((item) => item.id !== backendId)
    } catch (submitError) {
      deleteError.value =
        submitError instanceof Error ? submitError.message : t('api.errors.deleteBackendFailed')
      throw submitError
    } finally {
      deletingBackendIds.value = deletingBackendIds.value.filter((id) => id !== backendId)
    }
  }

  return {
    items,
    loading,
    creating,
    updating,
    deletingBackendIds,
    error,
    createError,
    updateError,
    deleteError,
    searchQuery,
    typeFilter,
    sortedItems,
    filteredItems,
    backendCount,
    openaiCount,
    anthropicCount,
    googleCount,
    loadBackends,
    createBackend,
    updateBackend,
    deleteBackend,
  }
})
