export type CapacityLedger = {
  capacity_bytes?: number
  reserved_bytes: number
  candidate_bytes: number
  live_bytes: number
  pending_delete_bytes: number
}

export function capacityTotals(space: CapacityLedger) {
  const values = [
    space.reserved_bytes,
    space.candidate_bytes,
    space.live_bytes,
    space.pending_delete_bytes,
  ]
  if (!values.every((value) => Number.isSafeInteger(value) && value >= 0)) return null
  const accounted = values.reduce((sum, value) => sum + value, 0)
  if (!Number.isSafeInteger(accounted)) return null
  const capacity = space.capacity_bytes
  const available =
    capacity !== undefined && Number.isSafeInteger(capacity) && capacity >= 0
      ? Math.max(capacity - accounted, 0)
      : null
  return { accounted, available }
}
