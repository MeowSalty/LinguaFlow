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
  fetchProject,
} from '@/api/projects'
import { storageActionAllowed, storageProjectWritable } from '@/utils/storage-contract'
import { assertSessionCurrent, captureSession } from '@/api/session-context'
import { storageRequestError } from '@/api/storage-errors'
import { createScopedEntityState } from './scopedEntity'
import { useOrganizationsStore } from './organizations'

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
  const canDelete = (project: ApiSchemas['Project']) =>
    state.canEdit(project) && storageActionAllowed(project, 'delete')
  const removeProject = async (id: number) => {
    const previous = state.items.value.find((item) => item.id === id)
    if (!previous || !canDelete(previous)) throw storageRequestError({ status: 403 })
    const session = captureSession()
    if (previous.owner_org_id) {
      const organizations = useOrganizationsStore()
      await organizations.refresh()
      assertSessionCurrent(session)
      if (organizations.error || !organizations.canWrite(previous.owner_org_id))
        throw storageRequestError({ status: 403 })
    }
    const current = await fetchProject(id)
    assertSessionCurrent(session)
    if (!canDelete(current) || current.storage_generation !== previous.storage_generation)
      throw storageRequestError({ status: 409 }, { error_code: 'storage_generation_conflict' })
    return state.remove(id)
  }
  return {
    ...state,
    canEdit: (project?: ApiSchemas['Project']) =>
      state.canEdit(project) && (!project || storageProjectWritable(project)),
    canDelete,
    remove: removeProject,
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
    deleteProject: removeProject,
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
