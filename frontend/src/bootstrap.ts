import { useAuthStore } from '@/stores/auth'
import { useServiceStore, type ServiceMode } from '@/stores/service'
import { hasAnyBlockedCapability } from '@/utils/secureContext'

export type BootstrapUserNotice = 'localUserFailed' | 'modeUnreachable' | 'insecureContext'

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

  if (hasAnyBlockedCapability()) {
    bootstrapNotices.push('insecureContext')
  }

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
