import { describe, expect, it } from 'vitest'
import {
  capacityTotals,
  formatStorageBytes,
  parseStorageCapacity,
  storageCapacityUnits,
  storageCapacityValue,
} from '@/components/storage/capacity'

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

describe('readable storage bytes', () => {
  it.each([
    [0, '0 B'],
    [1, '1 B'],
    [1023, '1,023 B'],
    [1024, '1 KiB'],
    [1536, '1.5 KiB'],
    [1024 ** 2, '1 MiB'],
    [1024 ** 3, '1 GiB'],
    [1024 ** 4, '1 TiB'],
  ])('formats %s as %s', (bytes, expected) => {
    expect(formatStorageBytes(bytes)).toBe(expected)
  })
  it.each([undefined, null, NaN, Infinity, -1, 1.5, Number.MAX_SAFE_INTEGER + 1])(
    'keeps unknown or unsafe bytes distinct from zero: %s',
    (bytes) => expect(formatStorageBytes(bytes)).toBe('—'),
  )
})

describe('exact storage capacity input', () => {
  it.each(storageCapacityUnits)(
    'round trips every safe byte through %s without rounding',
    (unit) => {
      for (const bytes of [1, 1023, 1024, 1024 ** 3 + 1, Number.MAX_SAFE_INTEGER]) {
        const value = storageCapacityValue(bytes, unit)
        expect(parseStorageCapacity(value, unit)).toBe(bytes)
      }
    },
  )
  it('preserves fractional unit precision down to a single byte', () => {
    expect(storageCapacityValue(1, 'GiB')).toBe('0.000000000931322574615478515625')
    expect(parseStorageCapacity('1.5', 'GiB')).toBe(1610612736)
    expect(parseStorageCapacity('0.0009765625', 'KiB')).toBe(1)
    expect(parseStorageCapacity('9007199254740991', 'B')).toBe(Number.MAX_SAFE_INTEGER)
  })
  it.each(['', ' ', '0', '-1', '1e3', 'NaN', '1,024', '0.1', '9007199254740992'])(
    'rejects invalid or unsafe byte inputs: %s',
    (input) => expect(parseStorageCapacity(input, 'B')).toBeNull(),
  )
  it('rejects fractional bytes and unit multiplication overflow before converting to Number', () => {
    expect(parseStorageCapacity('0.1', 'KiB')).toBeNull()
    expect(parseStorageCapacity('8192', 'TiB')).toBeNull()
    expect(parseStorageCapacity('9007199254740991.000000000000000001', 'B')).toBeNull()
    expect(parseStorageCapacity('9'.repeat(129), 'TiB')).toBeNull()
  })
})
