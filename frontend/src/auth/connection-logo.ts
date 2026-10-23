import { captureSession } from '@/api/session-context'

/** Only load the selected instance's validated public PNG endpoint. */
export function connectionLogoUrl(connectionId: string): string {
  return `${captureSession().baseUrl}/auth/connections/${encodeURIComponent(connectionId)}/logo`
}
