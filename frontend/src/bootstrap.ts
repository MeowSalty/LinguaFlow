import { useAuthStore } from '@/stores/auth'
import { useServiceStore, type ServiceMode } from '@/stores/service'

export type BootstrapUserNotice = 'localUserFailed' | 'modeUnreachable'

const bootstrapNotices: BootstrapUserNotice[] = []

export const consumeBootstrapNotices = (): BootstrapUserNotice[] => {
  const copy = [...bootstrapNotices]
  bootstrapNotices.length = 0
  return copy
}

export const bootstrapApp = async (): Promise<void> => {
  const service = useServiceStore()
  const auth = useAuthStore()
  bootstrapNotices.length = 0

  const resolved = await service.resolveBaseUrlForBootstrap()

  if (!service.hasSelected) {
    service.isAppReady = true
    auth.isReady = true
    return
  }

  if (resolved.mode === null) {
    await service.refreshMode()
  }

  const mode: ServiceMode = service.mode ?? resolved.mode ?? 'server'
  if (service.mode === null && resolved.mode === null) {
    bootstrapNotices.push('modeUnreachable')
  }

  await auth.bootstrapForMode(mode)

  if (mode === 'local' && !auth.user) {
    bootstrapNotices.push('localUserFailed')
  }

  service.isAppReady = true
}
