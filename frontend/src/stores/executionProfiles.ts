import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import {
  type ApiSchemas,
  createExecutionProfile as createRequest,
  deleteExecutionProfile as deleteRequest,
  fetchExecutionProfiles,
  updateExecutionProfile as updateRequest,
} from '@/api/client'
import { t } from '@/i18n'

type ExecutionProfile = ApiSchemas['ExecutionProfile']
type CreateRequest = ApiSchemas['CreateExecutionProfileRequest']
type UpdateRequest = ApiSchemas['UpdateExecutionProfileRequest']
type Scope = ExecutionProfile['scope']

const includesNormalized = (source: string | undefined, query: string): boolean => {
  return source?.toLowerCase().includes(query) ?? false
}

export const useExecutionProfilesStore = defineStore('executionProfiles', () => {
  // ── 状态 ──
  const items = ref<ExecutionProfile[]>([])

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

  // ── 配置特征统计 ──
  const withGlossaryCount = computed(
    () => items.value.filter((i) => i.config?.glossary?.bootstrap?.max_terms_per_1000_chars).length,
  )

  // ── 方法 ──
  const loadProfiles = async (): Promise<void> => {
    loading.value = true
    error.value = null

    try {
      const response = await fetchExecutionProfiles()
      items.value = response.items
    } catch (loadError) {
      error.value =
        loadError instanceof Error
          ? loadError.message
          : t('api.errors.fetchExecutionProfilesFailed')
    } finally {
      loading.value = false
    }
  }

  const createProfile = async (payload: CreateRequest): Promise<ExecutionProfile> => {
    creating.value = true
    error.value = null

    try {
      const profile = await createRequest(payload)
      items.value = [profile, ...items.value.filter((item) => item.id !== profile.id)]
      return profile
    } catch (submitError) {
      error.value =
        submitError instanceof Error
          ? submitError.message
          : t('api.errors.createExecutionProfileFailed')
      throw submitError
    } finally {
      creating.value = false
    }
  }

  const updateProfile = async (
    profileId: number,
    payload: UpdateRequest,
  ): Promise<ExecutionProfile> => {
    updating.value = true
    error.value = null

    try {
      const profile = await updateRequest(profileId, payload)
      items.value = items.value.map((item) => (item.id === profile.id ? profile : item))
      return profile
    } catch (submitError) {
      error.value =
        submitError instanceof Error
          ? submitError.message
          : t('api.errors.updateExecutionProfileFailed')
      throw submitError
    } finally {
      updating.value = false
    }
  }

  const deleteProfile = async (profileId: number): Promise<void> => {
    deletingIds.value = [...deletingIds.value, profileId]
    error.value = null

    try {
      await deleteRequest(profileId)
      items.value = items.value.filter((item) => item.id !== profileId)
    } catch (submitError) {
      error.value =
        submitError instanceof Error
          ? submitError.message
          : t('api.errors.deleteExecutionProfileFailed')
      throw submitError
    } finally {
      deletingIds.value = deletingIds.value.filter((id) => id !== profileId)
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
    withGlossaryCount,
    loadProfiles,
    createProfile,
    updateProfile,
    deleteProfile,
  }
})
