import { createApp, ref } from 'vue'
import { createPinia, defineStore, disposePinia } from 'pinia'
import { describe, expect, it } from 'vitest'
import { installSessionIsolation } from '../sessionIsolation'
import { changeSessionContext } from '@/api/session-context'

describe('user store isolation', () => {
  it('disposes user state before obtaining stores for the next identity', () => {
    changeSessionContext('/api/v1', 1, true)
    const pinia = createPinia()
    installSessionIsolation(pinia)
    createApp({}).use(pinia)
    const usePrivateStore = defineStore('private-test', () => ({ secret: ref('') }))
    const first = usePrivateStore(pinia)
    first.secret = 'previous-user-data'
    changeSessionContext('/api/v1', 2)
    const second = usePrivateStore(pinia)
    expect(second).not.toBe(first)
    expect(second.secret).toBe('')
    first.secret = 'late-response'
    expect(second.secret).toBe('')
    disposePinia(pinia)
  })
})
