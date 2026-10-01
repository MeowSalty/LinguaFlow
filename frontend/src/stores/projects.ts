import { defineStore } from 'pinia'
import { computed, ref } from 'vue'
import type { ApiSchemas } from '@/api/client'
import {
  createProject,
  createOrgProject,
  updateProject,
  deleteProject,
  fetchProjects,
  fetchOrgProjects,
} from '@/api/projects'
import { createScopedEntityState } from './scopedEntity'

export type GlossaryFilter = 'all' | 'enabled' | 'disabled'
export const useProjectsStore = defineStore('projects', () => {
  const state = createScopedEntityState<
    ApiSchemas['Project'],
    ApiSchemas['CreateProjectRequest'],
    ApiSchemas['UpdateProjectRequest']
  >({
    list: (orgId, signal) =>
      orgId === null
        ? fetchProjects(undefined, signal)
        : fetchOrgProjects(orgId, undefined, signal),
    create: (body, orgId) => (orgId === null ? createProject(body) : createOrgProject(orgId, body)),
    update: (id, body) => updateProject(id, body),
    remove: (id) => deleteProject(id),
  })
  const glossaryFilter = ref<GlossaryFilter>('all')
  const sortedItems = computed(() =>
    [...state.items.value].sort(
      (a, b) =>
        new Date(b.updated_at ?? b.created_at ?? 0).getTime() -
        new Date(a.updated_at ?? a.created_at ?? 0).getTime(),
    ),
  )
  const filteredItems = computed(() =>
    sortedItems.value.filter(
      (item) =>
        state.filteredItems.value.includes(item) &&
        (glossaryFilter.value === 'all' ||
          (glossaryFilter.value === 'enabled') === item.glossary_enabled),
    ),
  )
  const glossaryEnabledCount = computed(
    () => state.items.value.filter((item) => item.glossary_enabled).length,
  )
  return {
    ...state,
    glossaryFilter,
    sortedItems,
    filteredItems,
    glossaryEnabledCount,
    glossaryDisabledCount: computed(() => state.items.value.length - glossaryEnabledCount.value),
    deletingProjectIds: state.deletingIds,
    createError: state.error,
    updateError: state.error,
    deleteError: state.error,
    loadProjects: state.load,
    createProject: state.create,
    updateProject: state.update,
    deleteProject: state.remove,
    isDeletingProject: (id: number) => state.deletingIds.value.includes(id),
    setGlossaryFilter: (value: GlossaryFilter) => {
      glossaryFilter.value = value
    },
    resetFilters: () => {
      state.resetFilters()
      glossaryFilter.value = 'all'
    },
  }
})
