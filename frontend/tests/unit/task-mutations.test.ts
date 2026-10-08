import { beforeEach, afterEach, it, expect, vi } from 'vitest'
import { createPinia, setActivePinia, disposePinia, type Pinia } from 'pinia'
import { useTaskMutationsStore } from '@/stores/taskMutations'
import { changeSessionContext } from '@/api/session-context'
import type { TaskHistoryTarget } from '@/api/task-history'
vi.mock('@/i18n', () => ({ t: (key: string) => key }))
let pinia: Pinia
beforeEach(() => {
  pinia = createPinia()
  setActivePinia(pinia)
  changeSessionContext('/api/v1', 1, true)
})
afterEach(() => disposePinia(pinia))
const target: TaskHistoryTarget = { kind: 'translation', id: '1', project_id: 7 }
const deferred = () => {
  let resolve!: () => void
  const promise = new Promise<void>((done) => {
    resolve = done
  })
  return { resolve, promise }
}
it('locks a whole batch atomically and shares locks across action types while keeping kinds independent', async () => {
  const store = useTaskMutationsStore(),
    waiting = deferred(),
    other = { ...target, id: '2' }
  const work = store.run([target], 'resume', () => waiting.promise)
  const write = vi.fn()
  await expect(store.run([other, target], 'delete', write)).rejects.toMatchObject({ status: 409 })
  expect(write).not.toHaveBeenCalled()
  expect(store.isPending(other)).toBe(false)
  await store.run([{ ...target, kind: 'glossary_sync' }], 'delete', async () => {})
  waiting.resolve()
  await work
  expect(store.isPending(target)).toBe(false)
})
it('an old-session finally cannot release a new-session lock', async () => {
  const store = useTaskMutationsStore(),
    old = deferred(),
    next = deferred()
  const first = store.run([target], 'delete', () => old.promise)
  changeSessionContext('/api/v1', 2)
  const second = store.run([target], 'resume', () => next.promise)
  old.resolve()
  await expect(first).rejects.toThrow('Session changed')
  expect(store.isPending(target)).toBe(true)
  next.resolve()
  await second
})
