import { captureSession, isSessionCurrent, type SessionSnapshot } from '../api/session-core'
import { getAuthEpoch } from '../api/token-storage'

let prepared: { context: SessionSnapshot; epoch: string; signal: AbortSignal } | undefined

/** Only ownership metadata crosses into main.ts. Short-lived credentials stay in the coordinator. */
export function markCallbackPrepared(signal: AbortSignal): void {
  prepared = { context: captureSession(), epoch: getAuthEpoch(), signal }
}

export const hasPreparedCallback = (): boolean => prepared !== undefined

export function finishCallbackStartup(): void {
  prepared = undefined
}

export function assertCallbackPrepared(): void {
  if (
    prepared &&
    (prepared.signal.aborted ||
      !isSessionCurrent(prepared.context) ||
      getAuthEpoch() !== prepared.epoch)
  )
    throw new Error('Invalidated callback startup')
}
