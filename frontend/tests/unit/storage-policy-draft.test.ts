import { describe, expect, it } from 'vitest'
import type { ApiSchemas } from '@/api/client-core'
import { createStoragePolicyDraft } from '@/utils/storage-policy-draft'
import { quotaDraft, resolveQuota } from '@/utils/storage-quota'
import { policyCapabilities } from '../storage-fixtures'

const policy = (generation = 2): ApiSchemas['StoragePolicy'] => ({
  ...policyCapabilities(),
  mode: 'both',
  default_choice: 'user',
  generation,
  logical_limit_bytes: 100,
  default_space_capacity_bytes: 200,
})

describe('policy draft baseline and refresh protection', () => {
  it('refreshes clean forms but preserves dirty values and their original generation', () => {
    const draft = createStoragePolicyDraft()
    draft.receive(policy())
    draft.receive({ ...policy(3), logical_limit_bytes: 200 })
    expect(resolveQuota(draft.form.logical_limit_bytes)).toEqual({ ok: true, value: 200 })
    draft.form.logical_limit_bytes = quotaDraft(250)
    draft.receive({ ...policy(4), logical_limit_bytes: 400 })
    expect(draft.form.logical_limit_bytes).toEqual(quotaDraft(250))
    expect(draft.baseline.value?.generation).toBe(3)
    expect(draft.conflict.value).toBe(true)
    expect(draft.snapshot()).toBeNull()
    draft.adoptBaseline()
    expect(draft.snapshot()).toEqual({
      mode: 'both',
      default_choice: 'user',
      logical_limit_bytes: 250,
      default_space_capacity_bytes: 200,
      generation: 4,
    })
    expect(draft.dirty.value).toBe(true)
    draft.reload()
    expect(draft.form.logical_limit_bytes).toEqual(quotaDraft(400))
    expect(draft.dirty.value).toBe(false)
  })
  it('does not downgrade a restricted saved mode or dirty draft on runtime refresh', () => {
    const draft = createStoragePolicyDraft()
    draft.receive(policy())
    draft.form.default_space_capacity_bytes = quotaDraft(null)
    draft.receive({
      ...policy(),
      allowed_policy_modes: ['site_only'],
      runtime: { deployment_enabled: false, maintenance: false },
    })
    expect(draft.form.mode).toBe('both')
    expect(draft.form.default_space_capacity_bytes).toEqual({ mode: 'unlimited' })
    draft.chooseMode('site_only')
    expect(draft.form.mode).toBe('site_only')
    expect(draft.form.default_choice).toBe('site')
    expect(draft.form.default_space_capacity_bytes).toEqual({ mode: 'unlimited' })
    expect(draft.dirty.value).toBe(true)
  })
  it('accepts the PUT response before a refresh and protects later edits', () => {
    const draft = createStoragePolicyDraft()
    draft.receive(policy())
    draft.form.logical_limit_bytes = quotaDraft(null)
    const frozen = draft.snapshot()!
    draft.accept({ ...policy(3), logical_limit_bytes: null })
    expect(draft.dirty.value).toBe(false)
    draft.form.logical_limit_bytes = quotaDraft(300)
    draft.receive({ ...policy(3), logical_limit_bytes: null })
    expect(draft.snapshot()?.generation).toBe(3)
    expect(draft.form.logical_limit_bytes).toEqual(quotaDraft(300))
    expect(frozen.logical_limit_bytes).toBeNull()
    expect(frozen.generation).toBe(2)
    draft.clear()
    expect(draft.snapshot()).toBeNull()
    expect(draft.form.logical_limit_bytes).toEqual({ mode: 'unselected' })
    expect(draft.form.default_space_capacity_bytes).toEqual({ mode: 'unselected' })
  })
  it.each(['', '0', '1e3', '-1', '0.1', 'NaN', 'Infinity', '9007199254740992'])(
    'retains invalid raw text through refresh and explicit baseline adoption: %s',
    (input) => {
      const draft = createStoragePolicyDraft()
      draft.accept({ ...policy(), logical_limit_bytes: null, default_space_capacity_bytes: null })
      draft.form.logical_limit_bytes = { mode: 'limited', input, unit: 'B' }
      expect(draft.dirty.value).toBe(true)
      expect(draft.valid.value).toBe(false)
      draft.receive({ ...policy(3), logical_limit_bytes: 1000 })
      expect(draft.form.logical_limit_bytes).toEqual({ mode: 'limited', input, unit: 'B' })
      draft.adoptBaseline()
      expect(draft.snapshot()).toBeNull()
      expect(draft.form.logical_limit_bytes).toEqual({ mode: 'limited', input, unit: 'B' })
    },
  )
  it.each([
    [null, null],
    [100, null],
    [null, 200],
    [100, 200],
  ])('submits an atomic five-field request for %s and %s', (logical, capacity) => {
    const draft = createStoragePolicyDraft()
    draft.accept({
      ...policy(),
      logical_limit_bytes: logical,
      default_space_capacity_bytes: capacity,
    })
    expect(draft.snapshot()).toEqual({
      mode: 'both',
      default_choice: 'user',
      generation: 2,
      logical_limit_bytes: logical,
      default_space_capacity_bytes: capacity,
    })
  })
  it('does not replace a historical 100 GiB default with unlimited', () => {
    const draft = createStoragePolicyDraft()
    draft.accept({ ...policy(), default_space_capacity_bytes: 100 * 1024 ** 3 })
    expect(draft.snapshot()?.default_space_capacity_bytes).toBe(100 * 1024 ** 3)
  })
  it('preserves raw unit edits even when the parsed bytes are unchanged', () => {
    const draft = createStoragePolicyDraft()
    draft.accept({ ...policy(), logical_limit_bytes: 512 })
    draft.form.logical_limit_bytes = { mode: 'limited', input: '0.5', unit: 'KiB' }
    draft.receive({ ...policy(3), logical_limit_bytes: 1024 })
    expect(draft.dirty.value).toBe(true)
    expect(draft.form.logical_limit_bytes).toEqual({ mode: 'limited', input: '0.5', unit: 'KiB' })
  })
  it('requires explicit latest-baseline adoption after restoring an unknown submission', () => {
    const draft = createStoragePolicyDraft()
    const submitted = {
      mode: 'both' as const,
      default_choice: 'user' as const,
      generation: 2,
      logical_limit_bytes: null,
      default_space_capacity_bytes: 1024,
    }
    draft.restore(policy(), submitted, { ...policy(3), logical_limit_bytes: null })
    expect(draft.baseline.value?.generation).toBe(2)
    expect(draft.snapshot()).toBeNull()
    expect(resolveQuota(draft.form.default_space_capacity_bytes)).toEqual({ ok: true, value: 1024 })
    draft.adoptBaseline()
    expect(draft.snapshot()).toEqual({ ...submitted, generation: 3 })
    draft.reload()
    expect(draft.snapshot()?.default_space_capacity_bytes).toBe(200)
  })
  it('does not invent missing policy quotas', () => {
    const draft = createStoragePolicyDraft()
    draft.receive({
      ...policy(),
      default_space_capacity_bytes: undefined,
    } as unknown as ApiSchemas['StoragePolicy'])
    expect(draft.form.default_space_capacity_bytes).toEqual({ mode: 'unselected' })
    expect(draft.snapshot()).toBeNull()
  })
})
