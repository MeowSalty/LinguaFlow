import { describe, expect, it } from 'vitest'
import { ACTIVE_POLL_INTERVALS, resolveAdaptiveInterval } from '../adaptivePolling'

describe('active task refresh rates', () => {
  it.each([
    [['pausing'], ACTIVE_POLL_INTERVALS.running],
    [['paused', 'pausing'], ACTIVE_POLL_INTERVALS.running],
    [['pending', 'pausing', 'completed'], ACTIVE_POLL_INTERVALS.running],
    [['paused'], ACTIVE_POLL_INTERVALS.paused],
    [['completed', 'failed', 'cancelled'], null],
  ])('refreshes %j with interval %s', (statuses, interval) => {
    expect(resolveAdaptiveInterval(statuses)).toBe(interval)
  })

  it('uses the caller running interval while draining', () => {
    expect(
      resolveAdaptiveInterval(['pausing'], { running: 3000, pending: 6000, paused: 20000 }),
    ).toBe(3000)
  })
})
