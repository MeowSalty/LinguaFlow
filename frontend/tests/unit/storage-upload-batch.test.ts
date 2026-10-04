import { beforeEach, describe, expect, it, vi } from 'vitest'
import { createPinia, setActivePinia } from 'pinia'
import { useResourceStore } from '@/stores/resource'
import { changeSessionContext } from '@/api/session-context'
import type { ApiSchemas } from '@/api/client'

const api = vi.hoisted(() => ({ upload: vi.fn() }))
vi.mock('@/api/client', () => ({
  deleteProjectResource: vi.fn(),
  downloadProjectResource: vi.fn(),
  downloadResourceResult: vi.fn(),
  fetchProjectResources: vi.fn(),
  fetchProjectResourceTree: vi.fn(),
  precheckProjectResources: vi.fn(),
  uploadProjectResourcesWithProgress: api.upload,
}))
vi.mock('@/i18n', () => ({ t: (key: string) => key }))
const resource = {
  id: 7,
  name: 'source.txt',
  path: 'source.txt',
  format: 'txt',
  total_segments: 1,
  translated_segments: 0,
  approved_segments: 0,
} as ApiSchemas['Resource']
describe('upload batches', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    setActivePinia(createPinia())
    changeSessionContext('/api/v1', 1, true)
    api.upload.mockResolvedValue({ items: [{ action: 'created', path: 'source.txt', resource }] })
  })
  it('keeps the same key, bytes and ordered paths on replay and does not append duplicate resources', async () => {
    const store = useResourceStore()
    const file = new File(['hello'], 'source.txt')
    await store.uploadResources(1, [file], ['source.txt'], 'batch')
    await store.uploadResources(1, [file], ['source.txt'], 'batch')
    expect(store.resources.map((item) => item.id)).toEqual([7])
    const first = api.upload.mock.calls[0]!
    const replay = api.upload.mock.calls[1]!
    expect(replay[3].idempotencyKey).toBe(first[3].idempotencyKey)
    expect(replay[1][0]).toBe(file)
    expect(replay[2]).toEqual(['source.txt'])
  })
  it('refuses changed bytes or paths for an existing batch before sending another request', async () => {
    const store = useResourceStore()
    const file = new File(['hello'], 'source.txt')
    await store.uploadResources(1, [file], ['source.txt'], 'batch')
    await expect(
      store.uploadResources(1, [new File(['different'], 'source.txt')], ['source.txt'], 'batch'),
    ).rejects.toThrow()
    await expect(store.uploadResources(1, [file], ['other/source.txt'], 'batch')).rejects.toThrow()
    expect(api.upload).toHaveBeenCalledTimes(1)
  })
  it('does not restore uploaded resources after the login session has changed', async () => {
    const store = useResourceStore()
    let resolve!: (result: unknown) => void
    api.upload.mockReturnValue(
      new Promise((yes) => {
        resolve = yes
      }),
    )
    const result = store.uploadResources(1, [new File(['hello'], 'source.txt')], undefined, 'batch')
    changeSessionContext('/api/v1', 2, true)
    resolve({ items: [{ action: 'created', path: 'source.txt', resource }] })
    await expect(result).rejects.toThrow('Session changed')
    expect(store.resources).toEqual([])
  })
})
