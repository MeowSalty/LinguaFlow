import { describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({ t: vi.fn() }))
vi.mock('@/i18n', () => ({
  i18n: { global: { locale: { value: 'zh-Hans' }, t: mocks.t, d: vi.fn() } },
}))

import { formatDuration } from '@/utils/datetime'

describe('formatDuration', () => {
  it('formats sub-minute spans in seconds', () => {
    formatDuration(45)
    expect(mocks.t).toHaveBeenCalledWith('runtime.durationSeconds', { value: 45 })
  })

  it('formats sub-hour spans in whole minutes', () => {
    formatDuration(60.5)
    expect(mocks.t).toHaveBeenCalledWith('runtime.durationMinutes', { value: 1 })
  })

  it('formats sub-day spans as hours and minutes', () => {
    formatDuration(3661)
    expect(mocks.t).toHaveBeenCalledWith('runtime.durationHoursMinutes', { hours: 1, minutes: 1 })
  })

  it('formats longer spans as days and hours', () => {
    formatDuration(90061)
    expect(mocks.t).toHaveBeenCalledWith('runtime.durationDaysHours', { days: 1, hours: 1 })
  })
})
