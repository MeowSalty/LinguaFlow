import { describe, expect, it } from 'vitest'
import type { ApiSchemas } from '@/api/client-core'
import {
  createSpaceQuotaDraft,
  observeSpaceQuota,
  reviewSpaceQuota,
  spaceQuotaDirty,
} from '@/utils/storage-quota-draft'
import { quotaDraft } from '@/utils/storage-quota'
import { spaceActions } from '../storage-fixtures'

const space = (generation = 3, capacity: number | null = 200): ApiSchemas['StorageSpace'] => ({
  id: 11,
  connection_id: 1,
  name: 'space',
  status: 'active',
  verified: true,
  management_generation: generation,
  management_actions: spaceActions(),
  capacity_bytes: capacity,
  available_bytes: capacity,
  reserved_bytes: 0,
  candidate_bytes: 0,
  live_bytes: 0,
  pending_delete_bytes: 0,
})
describe('space quota draft review', () => {
  it('preserves invalid raw input and units across a status generation change', () => {
    const draft = createSpaceQuotaDraft(space(), { mode: 'limited', input: '1e4', unit: 'GiB' })
    const refreshed = observeSpaceQuota(draft, { ...space(4), status: 'read_only' })
    expect(spaceQuotaDirty(refreshed)).toBe(true)
    expect(refreshed.baseline.management_generation).toBe(3)
    expect(refreshed.draft).toEqual(draft.draft)
    expect(refreshed.latest?.management_generation).toBe(4)
    const reviewed = reviewSpaceQuota(refreshed, true)
    expect(reviewed.baseline.management_generation).toBe(4)
    expect(reviewed.draft).toEqual(draft.draft)
    expect(reviewed.latest).toBeNull()
  })
  it('supports explicitly loading the current value instead of keeping the draft', () => {
    const original = createSpaceQuotaDraft(space(), quotaDraft(null))
    const latest = observeSpaceQuota(original, space(4, 500))
    expect(reviewSpaceQuota(latest, false).draft).toEqual(quotaDraft(500))
  })
  it('refreshes clean drafts, ignores lower generations and unrelated spaces', () => {
    const original = createSpaceQuotaDraft(space())
    expect(observeSpaceQuota(original, space(4, null)).draft).toEqual(quotaDraft(null))
    expect(observeSpaceQuota(original, space(2))).toBe(original)
    expect(observeSpaceQuota(original, { ...space(4), id: 12 })).toBe(original)
  })
})
