import type { ApiSchemas } from '@/api/client-core'
import { quotaDraft, sameQuotaDraft, type QuotaDraft } from './storage-quota'

type Space = ApiSchemas['StorageSpace']
export type SpaceQuotaDraft = { baseline: Space; draft: QuotaDraft; latest: Space | null }
export const createSpaceQuotaDraft = (
  baseline: Space,
  draft = quotaDraft(baseline.capacity_bytes),
): SpaceQuotaDraft => ({ baseline, draft, latest: null })
export const spaceQuotaDirty = (value: SpaceQuotaDraft): boolean =>
  !sameQuotaDraft(value.draft, quotaDraft(value.baseline.capacity_bytes))

/** Refreshing a status generation also requires review before reusing a dirty quota draft. */
export function observeSpaceQuota(value: SpaceQuotaDraft, latest: Space): SpaceQuotaDraft {
  if (
    latest.id !== value.baseline.id ||
    latest.connection_id !== value.baseline.connection_id ||
    latest.management_generation < value.baseline.management_generation
  )
    return value
  if (!spaceQuotaDirty(value)) return createSpaceQuotaDraft(latest)
  return latest.management_generation !== value.baseline.management_generation
    ? { ...value, latest }
    : value
}

export function reviewSpaceQuota(value: SpaceQuotaDraft, keepDraft: boolean): SpaceQuotaDraft {
  return value.latest
    ? createSpaceQuotaDraft(
        value.latest,
        keepDraft ? value.draft : quotaDraft(value.latest.capacity_bytes),
      )
    : value
}
