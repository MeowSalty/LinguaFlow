import { defineStore } from 'pinia'
import { computed, ref } from 'vue'

import {
  type ApiSchemas,
  createProject as createProjectRequest,
  deleteProject as deleteProjectRequest,
  fetchOrganizations,
  fetchProjects,
  updateProject as updateProjectRequest,
} from '@/api/client'
import { t } from '@/i18n'

type Project = ApiSchemas['Project']
type Organization = ApiSchemas['Organization']
type CreateProjectPayload = ApiSchemas['CreateProjectRequest']
type UpdateProjectPayload = ApiSchemas['UpdateProjectRequest']
type ResourceScope = Project['resource_scope']

type ScopeFilter = ResourceScope | 'all'

const getProjectTime = (project: Project): number => {
  const timestamp = project.updated_at ?? project.created_at

  if (!timestamp) {
    return 0
  }

  return new Date(timestamp).getTime()
}

const includesNormalized = (source: string | undefined, query: string): boolean => {
  return source?.toLowerCase().includes(query) ?? false
}

export const useProjectsStore = defineStore('projects', () => {
  const items = ref<Project[]>([])
  const organizations = ref<Organization[]>([])

  const loading = ref(false)
  const organizationsLoading = ref(false)
  const creating = ref(false)
  const updating = ref(false)
  const deletingProjectIds = ref<number[]>([])

  const error = ref<string | null>(null)
  const organizationsError = ref<string | null>(null)
  const createError = ref<string | null>(null)
  const updateError = ref<string | null>(null)
  const deleteError = ref<string | null>(null)

  const searchQuery = ref('')
  const scopeFilter = ref<ScopeFilter>('all')

  const sortedItems = computed(() =>
    [...items.value].sort((left, right) => getProjectTime(right) - getProjectTime(left)),
  )

  const filteredItems = computed(() => {
    const query = searchQuery.value.trim().toLowerCase()

    return sortedItems.value.filter((project) => {
      const matchesScope = scopeFilter.value === 'all' || project.resource_scope === scopeFilter.value
      const matchesQuery =
        query.length === 0 ||
        includesNormalized(project.name, query) ||
        includesNormalized(project.source_lang, query) ||
        includesNormalized(project.target_lang, query)

      return matchesScope && matchesQuery
    })
  })

  const projectCount = computed(() => items.value.length)
  const personalProjectCount = computed(
    () => items.value.filter((project) => project.resource_scope === 'project').length,
  )
  const organizationProjectCount = computed(
    () => items.value.filter((project) => project.resource_scope === 'organization').length,
  )
  const languagePairCount = computed(
    () =>
      new Set(
        items.value.map((project) => `${project.source_lang || '-'}>${project.target_lang || '-'}`),
      ).size,
  )

  const loadProjects = async (): Promise<void> => {
    loading.value = true
    error.value = null

    try {
      const response = await fetchProjects()
      items.value = response.items
    } catch (loadError) {
      error.value = loadError instanceof Error ? loadError.message : t('api.errors.loadProjectsFailed')
    } finally {
      loading.value = false
    }
  }

  const loadOrganizations = async (): Promise<void> => {
    organizationsLoading.value = true
    organizationsError.value = null

    try {
      const response = await fetchOrganizations()
      organizations.value = response.items
    } catch (loadError) {
      organizationsError.value =
        loadError instanceof Error ? loadError.message : t('api.errors.loadOrganizationsFailed')
    } finally {
      organizationsLoading.value = false
    }
  }

  const createProject = async (payload: CreateProjectPayload): Promise<Project> => {
    creating.value = true
    createError.value = null

    try {
      const project = await createProjectRequest(payload)
      items.value = [project, ...items.value.filter((item) => item.id !== project.id)]
      return project
    } catch (submitError) {
      createError.value =
        submitError instanceof Error ? submitError.message : t('api.errors.createProjectFailed')
      throw submitError
    } finally {
      creating.value = false
    }
  }

  const updateProject = async (projectId: number, payload: UpdateProjectPayload): Promise<Project> => {
    updating.value = true
    updateError.value = null

    try {
      const project = await updateProjectRequest(projectId, payload)
      items.value = items.value.map((item) => (item.id === project.id ? project : item))
      return project
    } catch (submitError) {
      updateError.value =
        submitError instanceof Error ? submitError.message : t('api.errors.updateProjectFailed')
      throw submitError
    } finally {
      updating.value = false
    }
  }

  const deleteProject = async (projectId: number): Promise<void> => {
    deletingProjectIds.value = [...deletingProjectIds.value, projectId]
    deleteError.value = null

    try {
      await deleteProjectRequest(projectId)
      items.value = items.value.filter((item) => item.id !== projectId)
    } catch (submitError) {
      deleteError.value =
        submitError instanceof Error ? submitError.message : t('api.errors.deleteProjectFailed')
      throw submitError
    } finally {
      deletingProjectIds.value = deletingProjectIds.value.filter((id) => id !== projectId)
    }
  }

  const isDeletingProject = (projectId: number): boolean => deletingProjectIds.value.includes(projectId)

  const resetFilters = (): void => {
    searchQuery.value = ''
    scopeFilter.value = 'all'
  }

  return {
    items,
    organizations,
    loading,
    organizationsLoading,
    creating,
    updating,
    deletingProjectIds,
    error,
    organizationsError,
    createError,
    updateError,
    deleteError,
    searchQuery,
    scopeFilter,
    sortedItems,
    filteredItems,
    projectCount,
    personalProjectCount,
    organizationProjectCount,
    languagePairCount,
    loadProjects,
    loadOrganizations,
    createProject,
    updateProject,
    deleteProject,
    isDeletingProject,
    resetFilters,
  }
})
