import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { captureSession, changeSessionContext } from '@/api/session-context'
import { installCrossTabSessionSync } from '@/auth/cross-tab-session'

const state = vi.hoisted(() => ({ epoch: 'first' }))
vi.mock('@/api/token-storage', () => ({
  getAuthEpoch: () => state.epoch,
  authTokenStorageKeys: { authEpoch: 'epoch', apiBaseUrl: 'base' },
}))
let target: EventTarget
const reload = vi.fn()
let stop: () => void
beforeEach(() => {
  state.epoch = 'first'
  reload.mockClear()
  target = new EventTarget()
  vi.stubGlobal('window', Object.assign(target, { localStorage: {}, location: { reload } }))
  changeSessionContext('https://service.example/api/v1', 1, true)
})
afterEach(() => {
  stop?.()
  vi.unstubAllGlobals()
  changeSessionContext('/api/v1', null, true)
})
const notify = (key: string, value: string) =>
  target.dispatchEvent(
    Object.assign(new Event('storage'), { key, newValue: value, storageArea: null }),
  )

it('suspends old and newly captured request scopes before reloading a different identity', () => {
  const old = captureSession()
  stop = installCrossTabSessionSync(() => false)
  state.epoch = 'second'
  notify('epoch', 'second')
  expect(old.signal.aborted).toBe(true)
  expect(captureSession().signal.aborted).toBe(true)
  expect(captureSession().userId).toBeNull()
  expect(reload).toHaveBeenCalledTimes(1)
})
it('ignores ordinary token rotation and equivalent service addresses', () => {
  stop = installCrossTabSessionSync(() => false)
  notify('access-token', 'rotated')
  notify('base', 'https://service.example/api/v1/')
  expect(reload).not.toHaveBeenCalled()
})
it('ignores local-mode credential clearing while still detecting a service change', () => {
  stop = installCrossTabSessionSync(() => true)
  state.epoch = 'local'
  notify('epoch', 'local')
  expect(reload).not.toHaveBeenCalled()
  notify('base', 'https://other.example/api/v1')
  expect(reload).toHaveBeenCalledTimes(1)
})
