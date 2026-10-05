import { createPinia, setActivePinia } from 'pinia'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { ApiSchemas } from '@/api/client'
import { changeSessionContext } from '@/api/session-context'
import { useProjectsStore } from '../projects'
import { StorageApiError } from '@/api/storage-errors'

const api = vi.hoisted(() => ({
  fetchProject: vi.fn(),
  fetchProjects: vi.fn(),
  fetchOrgProjects: vi.fn(),
  createProject: vi.fn(),
  createOrgProject: vi.fn(),
  updateProject: vi.fn(),
  deleteProject: vi.fn(),
  getProjectStorage: vi.fn(),
}))
vi.mock('@/api/projects', () => api)
vi.mock('@/api/storage', () => ({ getProjectStorage: api.getProjectStorage }))
vi.mock('@/i18n', () => ({ t: (key: string) => key }))
const project = {
  id: 7,
  name: 'project',
  owner_user_id: 1,
  owner_org_id: null,
  storage_generation: 0,
  storage_state: 'active',
} as ApiSchemas['Project']
const storage = {
  project_id: 7,
  storage_generation: 0,
  storage_state: 'active',
  runtime: { deployment_enabled: false, maintenance: false },
}
beforeEach(() => {
  setActivePinia(createPinia())
  changeSessionContext('/api/v1', 1, true)
  api.fetchProject.mockResolvedValue(project)
  api.getProjectStorage.mockResolvedValue(storage)
})
describe('project deletion storage admission', () => {
  it.each(['create', 'delete'] as const)(
    'retains the project list after a policy refusal during %s',
    async (action) => {
      const store = useProjectsStore()
      store.items = [project]
      const cause = new StorageApiError('policy', 403, { error_code: 'storage_policy_violation' })
      if (action === 'create') api.createProject.mockRejectedValueOnce(cause)
      else api.deleteProject.mockRejectedValueOnce(cause)
      await expect(
        action === 'create'
          ? store.createProject({ name: 'new', source_lang: 'en', target_lang: 'zh-Hans' })
          : store.deleteProject(7),
      ).rejects.toThrow('policy')
      expect(store.items).toEqual([project])
    },
  )
  it('retains logical deletion when deployment is disabled', async () => {
    const store = useProjectsStore()
    store.items = [project]
    await store.deleteProject(7)
    expect(api.getProjectStorage).toHaveBeenCalledWith(7)
    expect(api.deleteProject).toHaveBeenCalledWith(7)
  })
  it.each([
    { ...storage, runtime: { deployment_enabled: true, maintenance: true } },
    { ...storage, runtime: undefined },
    { ...storage, storage_generation: 1 },
    { ...storage, project_id: 8 },
  ])('does not delete using stale, incomplete, or maintenance metadata: %j', async (summary) => {
    const store = useProjectsStore()
    store.items = [project]
    api.getProjectStorage.mockResolvedValue(summary)
    await expect(store.deleteProject(7)).rejects.toThrow()
    expect(api.deleteProject).not.toHaveBeenCalled()
    expect(store.items).toEqual([project])
  })
})
