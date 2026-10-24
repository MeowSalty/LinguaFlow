import { authCallbackMessages } from '../locales/auth-callback'

type Message = keyof typeof authCallbackMessages

export function showCallbackStatus(message: Message, complete = false): void {
  const panel = document.getElementById('auth-startup')
  if (!panel) return
  panel.dataset.state = complete ? 'complete' : 'loading'
  document.getElementById('auth-startup-message')!.textContent = authCallbackMessages[message]
  if (complete) {
    document.getElementById('auth-startup-slow')!.hidden = true
    document.getElementById('auth-startup-actions')!.hidden = false
  }
}

export function showCallbackFailure(message: Message = 'callbackUnavailable'): void {
  showCallbackStatus(message, true)
  const panel = document.getElementById('auth-startup')
  if (!panel) return
  panel.dataset.state = 'failed'
  document.getElementById('auth-startup-title')!.textContent =
    authCallbackMessages.callbackFailedTitle
}

export function releaseCallbackShell(): void {
  document.documentElement.removeAttribute('data-auth-callback')
  document.getElementById('auth-startup')?.remove()
}

export function clearEarlyCallbackStorage(): void {
  delete (window as Window & { __linguaflowAuthTicket?: string }).__linguaflowAuthTicket
  try {
    for (const key of [
      'linguaflow.external-flow.v2',
      'linguaflow.external-flow.v1',
      'linguaflow.external-result.v1',
      'linguaflow.connection-test-relay.v1',
    ])
      sessionStorage.removeItem(key)
  } catch {
    /* A failed cleanup never authorizes a second attempt in this document. */
  }
}
