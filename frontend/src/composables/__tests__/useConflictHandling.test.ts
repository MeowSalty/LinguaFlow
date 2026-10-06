import { describe, expect, it } from 'vitest'
import type { ApiSchemas } from '@/api/client'
import { useConflictHandling } from '../useConflictHandling'

const resource = (id: number) => ({ id, name: `resource-${id}` }) as ApiSchemas['Resource']
describe('source update confirmation queue', () => {
  it('preserves candidate order and distinguishes pending, published, failed and skipped results', () => {
    const queue = useConflictHandling()
    const files = [1, 2, 3, 4].map((id) => new File([String(id)], `${id}.json`))
    files.forEach((file, index) => queue.handleExplorerConflict(resource(index + 1), file))
    queue.openNext()
    expect(queue.conflictFile.value).toBe(files[0])
    queue.settle('completed')
    queue.openNext()
    expect(queue.conflictResource.value?.id).toBe(2)
    queue.settle('failed')
    queue.openNext()
    queue.settle('skipped')
    queue.openNext()
    expect(queue.conflictFile.value).toBe(files[3])
    expect(queue.summary.value).toEqual({ pending: 1, completed: 1, failed: 1, skipped: 1 })
  })
  it('closing the drawer does not skip an unresolved candidate and resetting clears identities', () => {
    const queue = useConflictHandling()
    queue.handleExplorerConflict(resource(1), new File(['a'], 'a.json'))
    queue.openNext()
    queue.conflictDialogVisible.value = false
    expect(queue.summary.value.pending).toBe(1)
    queue.openNext()
    expect(queue.conflictResource.value?.id).toBe(1)
    queue.resetConflictState()
    expect(queue.conflictResource.value).toBeNull()
    expect(queue.conflictFile.value).toBeNull()
  })
})
