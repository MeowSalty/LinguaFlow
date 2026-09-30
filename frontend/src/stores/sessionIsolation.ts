import type { Pinia, PiniaPluginContext, StoreGeneric } from 'pinia'
import { onSessionChange } from '@/api/session-context'

// Disposing a store detaches its effect scope. New components obtain fresh instances;
// late closures retain only the detached instance, never the next user's store.
export const installSessionIsolation = (pinia: Pinia): void => {
  const stores = new Map<string, StoreGeneric>()
  const persistent = new Set(['auth', 'service', 'theme', 'locale'])
  pinia.use(({ store }: PiniaPluginContext) => {
    if (!persistent.has(store.$id)) stores.set(store.$id, store)
  })
  onSessionChange(() => {
    for (const [id, store] of stores) {
      store.$dispose()
      delete pinia.state.value[id]
    }
    stores.clear()
  })
}
