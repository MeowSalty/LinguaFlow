import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import type { ApiSchemas } from '@/api/client'
import { createBackend, updateBackend, deleteBackend, fetchBackends } from '@/api/backends'
import { createScopedEntityState } from './scopedEntity'

export const useBackendsStore = defineStore('backends', () => {
  const state = createScopedEntityState<
    ApiSchemas['Backend'],
    ApiSchemas['CreateBackendRequest'],
    ApiSchemas['UpdateBackendRequest']
  >({
    list: (orgId, signal) => fetchBackends(undefined, orgId ?? undefined, signal),
    create: (body, orgId) => createBackend(body, undefined, orgId ?? undefined),
    update: (id, body, orgId) => updateBackend(id, body, undefined, orgId ?? undefined),
    remove: (id, orgId) => deleteBackend(id, undefined, orgId ?? undefined),
  })
  const typeFilter = ref<ApiSchemas['Backend']['type'] | 'all'>('all')
  const filteredItems = computed(() =>
    state.filteredItems.value.filter(
      (item) => typeFilter.value === 'all' || item.type === typeFilter.value,
    ),
  )
  return {
    ...state,
    typeFilter,
    filteredItems,
    deletingBackendIds: state.deletingIds,
    createError: state.error,
    updateError: state.error,
    deleteError: state.error,
    backendCount: state.totalCount,
    openaiCount: computed(() => state.items.value.filter((item) => item.type === 'openai').length),
    anthropicCount: computed(
      () => state.items.value.filter((item) => item.type === 'anthropic').length,
    ),
    googleCount: computed(() => state.items.value.filter((item) => item.type === 'google').length),
    loadBackends: state.load,
    createBackend: state.create,
    updateBackend: state.update,
    deleteBackend: state.remove,
    resetFilters: () => {
      state.resetFilters()
      typeFilter.value = 'all'
    },
  }
})
