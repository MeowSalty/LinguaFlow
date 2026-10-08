import {
  parseStorageCapacity,
  storageCapacityUnit,
  storageCapacityValue,
  type StorageCapacityUnit,
} from '@/components/storage/capacity'
import { isStorageQuota } from './storage-contract'

export type LimitedQuotaDraft = { mode: 'limited'; input: string; unit: StorageCapacityUnit }
export type QuotaDraft = { mode: 'unselected' } | { mode: 'unlimited' } | LimitedQuotaDraft
export type QuotaResolution =
  | { ok: true; value: number | null }
  | { ok: false; reason: 'required' | 'invalid' }

export function resolveQuota(draft: QuotaDraft): QuotaResolution {
  if (draft.mode === 'unselected') return { ok: false, reason: 'required' }
  if (draft.mode === 'unlimited') return { ok: true, value: null }
  const value = parseStorageCapacity(draft.input, draft.unit)
  return value === null ? { ok: false, reason: 'invalid' } : { ok: true, value }
}

export function quotaDraft(value: unknown): QuotaDraft {
  if (!isStorageQuota(value)) return { mode: 'unselected' }
  if (value === null) return { mode: 'unlimited' }
  const unit = storageCapacityUnit(value)
  return { mode: 'limited', input: storageCapacityValue(value, unit), unit }
}

/** Raw edits, including invalid text and unit changes, must survive a server refresh. */
export function sameQuotaDraft(left: QuotaDraft, right: QuotaDraft): boolean {
  return (
    left.mode === right.mode &&
    (left.mode !== 'limited' ||
      (right.mode === 'limited' && left.input === right.input && left.unit === right.unit))
  )
}
