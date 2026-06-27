import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import {
  type ApiSchemas,
  createTranslationProfile as createRequest,
  deleteTranslationProfile as deleteRequest,
  fetchTranslationProfiles,
  updateTranslationProfile as updateRequest,
} from '@/api/client'
import { t } from '@/i18n'

type TranslationProfile = ApiSchemas['TranslationProfile']
type CreateRequest = ApiSchemas['CreateTranslationProfileRequest']
type UpdateRequest = ApiSchemas['UpdateTranslationProfileRequest']
type Scope = TranslationProfile['scope']

const includesNormalized = (source: string | undefined, query: string): boolean => {
  return source?.toLowerCase().includes(query) ?? false
}

export const useTranslationProfilesStore = defineStore('translationProfiles', () => {
  // ── 状态 ──
  const items = ref<TranslationProfile[]>([])

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
  const withSplitCount = computed(() => items.value.filter((i) => i.config?.split?.enabled).length)

  // ── 方法 ──
  const loadProfiles = async (): Promise<void> => {
    loading.value = true
    error.value = null

    try {
      const response = await fetchTranslationProfiles()
      items.value = response.items
    } catch (loadError) {
      error.value =
        loadError instanceof Error
          ? loadError.message
          : t('api.errors.fetchTranslationProfilesFailed')
    } finally {
      loading.value = false
    }
  }

  const createProfile = async (payload: CreateRequest): Promise<TranslationProfile> => {
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
          : t('api.errors.createTranslationProfileFailed')
      throw submitError
    } finally {
      creating.value = false
    }
  }

  const updateProfile = async (
    profileId: number,
    payload: UpdateRequest,
  ): Promise<TranslationProfile> => {
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
          : t('api.errors.updateTranslationProfileFailed')
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
          : t('api.errors.deleteTranslationProfileFailed')
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
    withSplitCount,
    loadProfiles,
    createProfile,
    updateProfile,
    deleteProfile,
  }
})
