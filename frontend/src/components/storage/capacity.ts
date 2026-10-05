export type CapacityLedger = {
  capacity_bytes?: number | null
  reserved_bytes?: number | null
  candidate_bytes?: number | null
  live_bytes?: number | null
  pending_delete_bytes?: number | null
}

export const storageCapacityUnits = ['B', 'KiB', 'MiB', 'GiB', 'TiB'] as const
export type StorageCapacityUnit = (typeof storageCapacityUnits)[number]
const unitBytes = (unit: StorageCapacityUnit) => 1024n ** BigInt(storageCapacityUnits.indexOf(unit))
const safeBytes = (value: number | undefined | null): value is number =>
  typeof value === 'number' && Number.isSafeInteger(value) && value >= 0

export function storageCapacityUnit(value: number): StorageCapacityUnit {
  if (!safeBytes(value)) return 'B'
  let index = 0
  while (index < storageCapacityUnits.length - 1 && value >= 1024 ** (index + 1)) index++
  return storageCapacityUnits[index]!
}

export function formatStorageBytes(value: number | undefined | null): string {
  if (!safeBytes(value)) return '—'
  const unit = storageCapacityUnit(value)
  const amount = value / Number(unitBytes(unit))
  return `${amount.toLocaleString(undefined, { maximumFractionDigits: 2 })} ${unit}`
}

/** Powers of 1024 have finite decimal expansions; keep every digit when editing. */
export function storageCapacityValue(value: number, unit: StorageCapacityUnit): string {
  if (!safeBytes(value)) return ''
  const divisor = unitBytes(unit)
  const integer = BigInt(value) / divisor
  let remainder = BigInt(value) % divisor
  let fraction = ''
  while (remainder > 0n) {
    remainder *= 10n
    fraction += (remainder / divisor).toString()
    remainder %= divisor
  }
  return fraction ? `${integer}.${fraction}` : integer.toString()
}

/** Never round a fractional byte or allow a value outside the API's safe integer range. */
export function parseStorageCapacity(value: string, unit: StorageCapacityUnit): number | null {
  const input = value.trim()
  if (input.length > 128 || !/^\d+(?:\.\d*)?$/.test(input)) return null
  const [integer = '', fraction = ''] = input.split('.')
  const denominator = 10n ** BigInt(fraction.length)
  const numerator = BigInt(integer + fraction) * unitBytes(unit)
  if (numerator % denominator !== 0n) return null
  const bytes = numerator / denominator
  return bytes > 0n && bytes <= BigInt(Number.MAX_SAFE_INTEGER) ? Number(bytes) : null
}

export function capacityTotals(space: CapacityLedger) {
  const values = [
    space.reserved_bytes,
    space.candidate_bytes,
    space.live_bytes,
    space.pending_delete_bytes,
  ]
  if (!values.every(safeBytes)) return null
  const accounted = values.reduce((sum, value) => sum + value, 0)
  if (!Number.isSafeInteger(accounted)) return null
  const capacity = space.capacity_bytes
  const available = safeBytes(capacity) ? Math.max(capacity - accounted, 0) : null
  return { accounted, available }
}
