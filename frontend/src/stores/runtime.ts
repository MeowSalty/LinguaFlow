import { computed, onScopeDispose, shallowRef, watch } from 'vue'
import { defineStore } from 'pinia'
import { fetchRuntimeSummary, type RuntimeSummary } from '@/api/runtime'
import { captureSession, isSessionCurrent, onSessionChange } from '@/api/session-context'
import { ApiError } from '@/api/utils'
import { t } from '@/i18n'
import { useAuthStore } from './auth'

export const RUNTIME_POLL_INTERVAL = 5_000

export const useRuntimeStore = defineStore('runtime', () => {
  const auth = useAuthStore()
  const snapshot = shallowRef<RuntimeSummary | null>(null)
  const loading = shallowRef(false)
  const error = shallowRef<string | null>(null)
  const forbidden = shallowRef(false)
  const authorized = computed(() => auth.isAuthenticated && auth.user?.role === 'admin')
  const stale = computed(() => snapshot.value !== null && error.value !== null)
  let subscribers = 0
  let generation = 0
  let timer: ReturnType<typeof setTimeout> | undefined
  let controller: AbortController | undefined
  let pending: Promise<void> | undefined

  const visible = () => typeof document === 'undefined' || document.visibilityState !== 'hidden'
  const canPoll = () => subscribers > 0 && visible() && authorized.value && !forbidden.value
  const clearTimer = () => {
    if (timer !== undefined) clearTimeout(timer)
    timer = undefined
  }
  const cancel = () => {
    generation++
    clearTimer()
    controller?.abort()
    controller = undefined
    pending = undefined
    loading.value = false
  }
  const reset = () => {
    cancel()
    snapshot.value = null
    error.value = null
    forbidden.value = false
  }
  const schedule = () => {
    clearTimer()
    if (canPoll()) timer = setTimeout(() => void refresh(), RUNTIME_POLL_INTERVAL)
  }
  const refresh = (): Promise<void> => {
    if (!canPoll()) return Promise.resolve()
    if (pending) return pending
    clearTimer()
    const session = captureSession()
    const requestGeneration = generation
    const requestController = new AbortController()
    controller = requestController
    const current = () =>
      requestGeneration === generation &&
      isSessionCurrent(session) &&
      authorized.value &&
      !requestController.signal.aborted
    loading.value = true
    pending = (async () => {
      try {
        const result = await fetchRuntimeSummary(requestController.signal)
        if (!current()) return
        // Replace the complete sample, including when the server restarts.
        snapshot.value = result
        error.value = null
      } catch (failure) {
        if (!current()) return
        if (failure instanceof ApiError && failure.status === 403) {
          snapshot.value = null
          forbidden.value = true
          error.value = t('runtime.forbidden')
        } else {
          error.value = failure instanceof Error ? failure.message : t('runtime.fetchFailed')
        }
      } finally {
        if (current()) {
          loading.value = false
          pending = undefined
          controller = undefined
          schedule()
        }
      }
    })()
    return pending
  }
  const onVisibilityChange = () => {
    if (!visible()) cancel()
    else void refresh()
  }
  const subscribe = (): (() => void) => {
    subscribers++
    if (subscribers === 1) {
      document.addEventListener('visibilitychange', onVisibilityChange)
      void refresh()
    }
    let released = false
    return () => {
      if (released) return
      released = true
      subscribers--
      if (subscribers === 0) {
        document.removeEventListener('visibilitychange', onVisibilityChange)
        cancel()
      }
    }
  }
  const removeSessionListener = onSessionChange(reset)
  watch(
    authorized,
    (allowed) => {
      reset()
      if (allowed) void refresh()
    },
    { flush: 'sync' },
  )
  onScopeDispose(() => {
    reset()
    removeSessionListener()
    document.removeEventListener('visibilitychange', onVisibilityChange)
  })

  return { snapshot, loading, error, forbidden, authorized, stale, refresh, subscribe, reset }
})
