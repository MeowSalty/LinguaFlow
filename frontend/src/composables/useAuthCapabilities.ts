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
  const stop = onSessionChange(() => void reload())
  onScopeDispose(() => {
    alive = false
    sequence++
    stop()
  })
  void reload()
  const connections = computed(() =>
    [...(capabilities.value?.connections ?? [])].sort((a, b) => a.display_order - b.display_order),
  )
  const loginConnections = computed(() =>
    connections.value.filter((connection) => connection.can_login),
  )
  const signupConnections = computed(() =>
    connections.value.filter((connection) => connection.can_signup),
  )
  const canSignup = computed(
    () =>
      capabilities.value?.mode === 'server' &&
      (capabilities.value.local_account.can_signup || signupConnections.value.length > 0),
  )
  return {
    capabilities,
    connections,
    loginConnections,
    signupConnections,
    canSignup,
    loading,
    error,
    reload,
  }
}
