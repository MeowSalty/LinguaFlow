import { describe, expect, it } from 'vitest'
import {
  assertSessionCurrent,
  captureSession,
  changeSessionContext,
  getSessionScope,
  isAuthSessionCurrent,
  isSessionCurrent,
  onSessionChange,
  onSessionGenerationChange,
  StaleSessionError,
  suspendSessionContext,
} from '../../src/api/session-core'
import { sessionGeneration } from '../../src/api/session-context'

describe('session core transport boundary', () => {
  it('shares one scope with the Vue wrapper and invalidates its captured request', () => {
    changeSessionContext('https://core.example/api/v1', 17, true)
    const before = captureSession()
    changeSessionContext('https://core.example/api/v1', 18)
    expect(before.signal.aborted).toBe(true)
    expect(captureSession().generation).toBe(sessionGeneration.value)
  })

  it('preserves authentication generation only for a forced same-scope view reset', () => {
    changeSessionContext('https://auth.example/api/v1', 9, true)
    const authenticated = captureSession()
    changeSessionContext('https://auth.example/api/v1', 9, true, true)
    expect(isAuthSessionCurrent(authenticated)).toBe(true)
    changeSessionContext('https://auth.example/api/v1', 10)
    expect(isAuthSessionCurrent(authenticated)).toBe(false)
  })

  it('notifies generation observers before ordinary listeners and supports unsubscribe', () => {
    const events: string[] = []
    const removeGeneration = onSessionGenerationChange((generation) =>
      events.push('generation:' + generation),
    )
    const removeSession = onSessionChange(() => events.push('session:' + sessionGeneration.value))
    const initialGeneration = captureSession().generation
    changeSessionContext('https://order.example/api/v1', 4, true)
    expect(events).toEqual([
      'generation:' + initialGeneration,
      'generation:' + (initialGeneration + 1),
      'session:' + (initialGeneration + 1),
    ])
    removeGeneration()
    removeSession()
    suspendSessionContext()
    expect(events).toHaveLength(3)
  })

  it('suspends identity and cancels requests without opening another request scope', () => {
    changeSessionContext('https://suspend.example/api/v1', 21, true)
    const before = captureSession()

    suspendSessionContext()

    const suspended = captureSession()
    expect(before.signal.aborted).toBe(true)
    expect(suspended.signal).toBe(before.signal)
    expect(suspended.baseUrl).toBe(before.baseUrl)
    expect(suspended.userId).toBeNull()
    expect(getSessionScope()).toBeNull()
    expect(isAuthSessionCurrent(before)).toBe(false)
    expect(isSessionCurrent(suspended)).toBe(false)
    expect(() => assertSessionCurrent(suspended)).toThrow(StaleSessionError)
    expect(sessionGeneration.value).toBe(suspended.generation)
  })

  it('lets earlier ordinary listeners see the new ref after all generation observers sync', () => {
    const synced: number[] = []
    const observed: Array<{ ref: number; snapshot: number; synced: number[] }> = []
    const removeSession = onSessionChange(() => {
      observed.push({
        ref: sessionGeneration.value,
        snapshot: captureSession().generation,
        synced: [...synced],
      })
    })
    const removeFirst = onSessionGenerationChange((generation) => {
      synced[0] = generation
    })
    const removeSecond = onSessionGenerationChange((generation) => {
      synced[1] = generation
    })
    try {
      const initial = captureSession().generation
      expect(synced).toEqual([initial, initial])
      changeSessionContext('https://late-observer.example/api/v1', 22, true)
      const next = captureSession().generation
      expect(next).toBeGreaterThan(initial)
      expect(observed).toEqual([{ ref: next, snapshot: next, synced: [next, next] }])
    } finally {
      removeSession()
      removeFirst()
      removeSecond()
    }
  })

  it.each(['generation', 'session'] as const)(
    'skips a %s listener unsubscribed during notification',
    (kind) => {
      const calls: string[] = []
      let notifying = false
      let removeNext = () => {}
      const subscribe = kind === 'generation' ? onSessionGenerationChange : onSessionChange
      const removeFirst = subscribe(() => {
        if (notifying) removeNext()
      })
      removeNext = subscribe(() => {
        if (notifying) calls.push('removed')
      })
      try {
        notifying = true
        changeSessionContext('https://unsubscribe.example/api/v1', 23, true)
        expect(calls).toEqual([])
      } finally {
        removeFirst()
        removeNext()
      }
    },
  )
})
