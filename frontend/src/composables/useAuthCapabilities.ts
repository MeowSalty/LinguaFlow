import { computed, onScopeDispose, shallowRef } from 'vue'
import { fetchAuthCapabilities } from '@/api/auth-capabilities'
import type { ApiSchemas } from '@/api/client-core'
import { captureSession, isSessionCurrent, onSessionChange } from '@/api/session-context'
import { extractErrorMessage } from '@/utils/errors'
import { t } from '@/i18n'

export function useAuthCapabilities() {
  const capabilities = shallowRef<ApiSchemas['AuthCapabilities'] | null>(null)
  const loading = shallowRef(true)
  const error = shallowRef('')
  let alive = true
  let sequence = 0
  let base = captureSession().baseUrl
  async function reload() {
    const id = ++sequence
    const context = captureSession()
    loading.value = true
    error.value = ''
    capabilities.value = null
    try {
      const result = await fetchAuthCapabilities()
      if (alive && id === sequence && isSessionCurrent(context)) capabilities.value = result
    } catch (cause) {
      if (alive && id === sequence && isSessionCurrent(context))
        error.value = extractErrorMessage(cause, t('authentication.capabilitiesFailed'))
    } finally {
      if (alive && id === sequence) loading.value = false
    }
  }
  const stop = onSessionChange(() => {
    if (base !== captureSession().baseUrl) {
      base = captureSession().baseUrl
      void reload()
    }
  })
  onScopeDispose(() => {
    alive = false
    sequence++
    stop()
  })
  void reload()
  const connections = computed(() =>
    [...(capabilities.value?.connections ?? [])]
      .filter((connection) => connection.can_login)
      .sort((a, b) => a.display_order - b.display_order),
  )
  const canSignup = computed(
    () =>
      capabilities.value?.mode === 'server' &&
      (capabilities.value.password.can_signup ||
        connections.value.some((connection) => connection.can_signup)),
  )
  return { capabilities, connections, canSignup, loading, error, reload }
}
