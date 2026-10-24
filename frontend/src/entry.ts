import { clearEarlyCallbackStorage, showCallbackFailure } from './auth/callback-shell'

const loadApplication = () => import('./main')

if (location.pathname.replace(/\/$/, '') === '/auth/complete') {
  try {
    const { runAuthCallback } = await import('./auth/callback-entry')
    await runAuthCallback(loadApplication)
  } catch {
    clearEarlyCallbackStorage()
    showCallbackFailure()
  }
} else {
  await loadApplication()
}
