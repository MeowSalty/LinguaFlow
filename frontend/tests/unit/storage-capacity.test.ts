import { describe, expect, it } from 'vitest'
import { capacityTotals } from '@/components/storage/capacity'

const ledger = {
  capacity_bytes: 1000,
  reserved_bytes: 100,
  candidate_bytes: 200,
  live_bytes: 300,
  pending_delete_bytes: 40,
}
describe('storage capacity ledger', () => {
  it('counts pending cleanup and preserves accounted occupancy after a capacity reduction', () => {
    expect(capacityTotals(ledger)).toEqual({ accounted: 640, available: 360 })
    expect(capacityTotals({ ...ledger, capacity_bytes: 50 })).toEqual({
      accounted: 640,
      available: 0,
    })
  })
  it.each([undefined, NaN, -1, 1.5, Number.MAX_SAFE_INTEGER + 1])(
    'does not invent missing or unsafe occupancy: %s',
    (value) => {
      expect(capacityTotals({ ...ledger, live_bytes: value as number })).toBeNull()
    },
  )
  it('rejects overflow in a sum of individually safe amounts', () => {
    expect(capacityTotals({ ...ledger, live_bytes: Number.MAX_SAFE_INTEGER })).toBeNull()
  })
  it.each([undefined, NaN, -1, 1.5])(
    'retains known occupancy without inventing an unavailable limit: %s',
    (value) => {
      expect(capacityTotals({ ...ledger, capacity_bytes: value })).toEqual({
        accounted: 640,
        available: null,
      })
    },
  )
})
