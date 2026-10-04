import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, disposePinia, setActivePinia, type Pinia } from 'pinia'
import { useProjectStore } from '@/stores/project'
import { useResourceStore } from '@/stores/resource'
import type { ApiSchemas } from '@/api/client'
import { ApiError } from '@/api/utils'
import { changeSessionContext } from '@/api/session-context'

const api = vi.hoisted(() => ({ project: vi.fn(), tree: vi.fn(), list: vi.fn(), upload: vi.fn() }))
vi.mock('@/api/client', () => ({
  fetchProject: api.project,
  fetchProjectResourceTree: api.tree,
  fetchProjectResources: api.list,
  uploadProjectResourcesWithProgress: api.upload,
  deleteProjectResource: vi.fn(),
  downloadProjectResource: vi.fn(),
  downloadResourceResult: vi.fn(),
  precheckProjectResources: vi.fn(),
}))
vi.mock('@/i18n', () => ({ t: (key: string) => key }))
const project = (id: number): ApiSchemas['Project'] => ({
  id,
  name: `project-${id}`,
  glossary_enabled: false,
  source_lang: 'en',
  target_lang: 'zh-Hans',
})
const resource = (id: number): ApiSchemas['Resource'] => ({
  id,
  name: `${id}.txt`,
  path: `${id}.txt`,
  directory: '',
  format: 'txt',
  total_segments: 1,
  translated_segments: 0,
  approved_segments: 0,
  created_at: '2026-10-04T00:00:00Z',
  updated_at: '2026-10-04T00:00:00Z',
})
const tree = (id: number): ApiSchemas['ResourceTreeResponse'] => ({
  root: {
    type: 'directory',
    name: '',
    path: '',
    children: [{ type: 'resource', name: `${id}.txt`, path: `${id}.txt`, resource: resource(id) }],
  },
})
function deferred<T>() {
  let resolve!: (value: T) => void, reject!: (error: unknown) => void
  const promise = new Promise<T>((yes, no) => {
    resolve = yes
    reject = no
  })
  return { promise, resolve, reject }
}
let pinia: Pinia
beforeEach(() => {
  vi.resetAllMocks()
  changeSessionContext('/api/v1', 1, true)
  pinia = createPinia()
  setActivePinia(pinia)
})
afterEach(() => disposePinia(pinia))

describe('project metadata target isolation', () => {
  it('clears project A immediately on switching to B and rejects a late A refresh', async () => {
    const old = deferred<ApiSchemas['Project']>(),
      next = deferred<ApiSchemas['Project']>()
    api.project
      .mockResolvedValueOnce(project(1))
      .mockReturnValueOnce(old.promise)
      .mockReturnValueOnce(next.promise)
    const store = useProjectStore()
    await store.loadProject(1)
    const first = store.loadProject(1),
      second = store.loadProject(2)
    expect(store.project).toBeNull()
    old.resolve(project(1))
    await first
    expect(store.project).toBeNull()
    expect(store.loadingProject).toBe(true)
    next.resolve(project(2))
    await second
    expect(store.project?.id).toBe(2)
  })
  it.each([403, 404])(
    'retains only a same-target transient snapshot and clears it on %s',
    async (status) => {
      api.project
        .mockResolvedValueOnce(project(1))
        .mockRejectedValueOnce(new ApiError('offline', 503))
        .mockRejectedValueOnce(new ApiError('gone', status))
      const store = useProjectStore()
      await store.loadProject(1)
      await store.loadProject(1)
      expect(store.project?.id).toBe(1)
      await store.loadProject(1)
      expect(store.project).toBeNull()
      expect(store.projectError).toBe('gone')
    },
  )
  it('does not retain a previous project after the new target fails or the session changes', async () => {
    api.project
      .mockResolvedValueOnce(project(1))
      .mockRejectedValueOnce(new ApiError('offline', 503))
      .mockResolvedValueOnce(project(2))
    const store = useProjectStore()
    await store.loadProject(1)
    await store.loadProject(2)
    expect(store.project).toBeNull()
    await store.loadProject(2)
    changeSessionContext('/api/v1', 2)
    expect(store.project).toBeNull()
    expect(store._currentProjectId).toBeNull()
  })
})

describe('resource tree and list target isolation', () => {
  it('does not let tree A overwrite list B, including selections and loading state', async () => {
    const old = deferred<ApiSchemas['ResourceTreeResponse']>(),
      next = deferred<ApiSchemas['ResourceListResponse']>()
    api.tree.mockResolvedValueOnce(tree(11)).mockReturnValueOnce(old.promise)
    api.list.mockReturnValue(next.promise)
    const store = useResourceStore()
    await store.loadResourceTree(1)
    store.setSelectedResourceIds([11])
    store.setActiveResource(11)
    store.navigateTo('old')
    const first = store.loadResourceTree(1),
      second = store.loadResources(2)
    expect(store.resourceTree).toBeNull()
    expect(store.resources).toEqual([])
    expect(store.selectedResourceIds).toEqual([])
    expect(store.activeResourceId).toBeNull()
    expect(store.currentPath).toBe('')
    old.resolve(tree(12))
    await first
    expect(store.resourceTree).toBeNull()
    expect(store.loadingResources).toBe(true)
    next.resolve({ items: [resource(21)] })
    await second
    expect(store.resources.map((item) => item.id)).toEqual([21])
  })
  it('does not let list A overwrite tree B when A returns last', async () => {
    const old = deferred<ApiSchemas['ResourceListResponse']>()
    api.list.mockReturnValue(old.promise)
    api.tree.mockResolvedValue(tree(21))
    const store = useResourceStore()
    const first = store.loadResources(1)
    await store.loadResourceTree(2)
    old.resolve({ items: [resource(11)] })
    await first
    expect(store.resources.map((item) => item.id)).toEqual([21])
    expect(store.resourceTree?.children?.[0]?.resource?.id).toBe(21)
    expect(store.loadingResources).toBe(false)
  })
  it('never appends an old project list when a caller asks to paginate a new project', async () => {
    api.list
      .mockResolvedValueOnce({ items: [resource(11)] })
      .mockResolvedValueOnce({ items: [resource(21)] })
    const store = useResourceStore()
    await store.loadResources(1)
    store.resourcesCursor = 'old-project-cursor'
    await store.loadResources(2, true)
    expect(api.list.mock.calls[1]?.[1].cursor).toBeUndefined()
    expect(store.resources.map((item) => item.id)).toEqual([21])
  })
  it('allows a newer filtered list to remain authoritative when an older tree arrives', async () => {
    const old = deferred<ApiSchemas['ResourceTreeResponse']>()
    api.tree.mockReturnValue(old.promise)
    api.list.mockResolvedValue({ items: [resource(12)] })
    const store = useResourceStore()
    const first = store.loadResourceTree(1)
    store.resourceSearch = '12'
    await store.loadResources(1)
    old.resolve(tree(11))
    await first
    expect(store.resources.map((item) => item.id)).toEqual([12])
    expect(store.resourceTree?.children?.[0]?.resource?.id).toBe(11)
  })
  it('drops responses for a changed filter and keeps the new request loading state', async () => {
    const old = deferred<ApiSchemas['ResourceListResponse']>(),
      next = deferred<ApiSchemas['ResourceListResponse']>()
    api.list.mockReturnValueOnce(old.promise).mockReturnValueOnce(next.promise)
    const store = useResourceStore()
    const first = store.loadResources(1)
    store.resourceSearch = 'new'
    const second = store.loadResources(1)
    old.resolve({ items: [resource(11)] })
    await first
    expect(store.resources).toEqual([])
    expect(store.loadingResources).toBe(true)
    next.resolve({ items: [resource(12)] })
    await second
    expect(store.resources.map((item) => item.id)).toEqual([12])
  })
  it.each([403, 404])(
    'clears tree and derived list on %s after preserving a same-project temporary failure',
    async (status) => {
      api.tree
        .mockResolvedValueOnce(tree(11))
        .mockRejectedValueOnce(new ApiError('offline', 503))
        .mockRejectedValueOnce(new ApiError('gone', status))
      const store = useResourceStore()
      await store.loadResourceTree(1)
      await store.loadResourceTree(1)
      expect(store.resources.map((item) => item.id)).toEqual([11])
      expect(store.resourceTree).not.toBeNull()
      await store.loadResourceTree(1)
      expect(store.resources).toEqual([])
      expect(store.resourceTree).toBeNull()
    },
  )
  it('a denied list clears its old tree and prevents an older tree request restoring it', async () => {
    const old = deferred<ApiSchemas['ResourceTreeResponse']>()
    api.tree.mockResolvedValueOnce(tree(11)).mockReturnValueOnce(old.promise)
    api.list.mockRejectedValue(new ApiError('denied', 403))
    const store = useResourceStore()
    await store.loadResourceTree(1)
    const first = store.loadResourceTree(1)
    await store.loadResources(1)
    old.resolve(tree(12))
    await first
    expect(store.resourceTree).toBeNull()
    expect(store.resources).toEqual([])
  })
  it('does not publish an upload response into a different project', async () => {
    const upload = deferred<ApiSchemas['ResourceUploadBatchResponse']>()
    api.upload.mockReturnValue(upload.promise)
    api.tree.mockResolvedValue(tree(21))
    const store = useResourceStore()
    const uploading = store.uploadResources(1, [new File(['content'], 'source.txt')])
    await store.loadResourceTree(2)
    upload.resolve({ items: [{ action: 'created', path: '11.txt', resource: resource(11) }] })
    expect((await uploading).summary.created).toBe(1)
    expect(store.resources.map((item) => item.id)).toEqual([21])
    expect(store.lastUploadResult).toBeNull()
  })
  it('does not let a pre-publication resource read remove newly uploaded resources', async () => {
    const old = deferred<ApiSchemas['ResourceTreeResponse']>()
    api.tree.mockReturnValue(old.promise)
    api.upload.mockResolvedValue({
      items: [{ action: 'created', path: '12.txt', resource: resource(12) }],
    })
    const store = useResourceStore()
    const first = store.loadResourceTree(1)
    await store.uploadResources(1, [new File(['content'], '12.txt')])
    old.resolve(tree(11))
    await first
    expect(store.resources.map((item) => item.id)).toEqual([12])
  })
})
