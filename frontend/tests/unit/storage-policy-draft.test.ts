import { describe, expect, it } from 'vitest'
import type { ApiSchemas } from '@/api/client-core'
import { createStoragePolicyDraft } from '@/utils/storage-policy-draft'
import { policyCapabilities } from '../storage-fixtures'

const policy = (generation = 2): ApiSchemas['StoragePolicy'] => ({
  ...policyCapabilities(),
  mode: 'both',
  default_choice: 'user',
  generation,
  logical_limit_bytes: 100,
})

describe('policy draft baseline and refresh protection', () => {
  it('refreshes clean forms but preserves dirty values and their original generation', () => {
    const draft = createStoragePolicyDraft()
    draft.receive(policy())
    draft.receive({ ...policy(3), logical_limit_bytes: 200 })
    expect(draft.form.logical_limit_bytes).toBe(200)
    draft.form.logical_limit_bytes = 250
    draft.receive({ ...policy(4), logical_limit_bytes: 400 })
    expect(draft.form.logical_limit_bytes).toBe(250)
    expect(draft.baseline.value?.generation).toBe(3)
    expect(draft.conflict.value).toBe(true)
    expect(draft.snapshot()).toBeNull()
    draft.adoptBaseline()
    expect(draft.snapshot()).toEqual({
      mode: 'both',
      default_choice: 'user',
      logical_limit_bytes: 250,
      generation: 4,
    })
    expect(draft.dirty.value).toBe(true)
    draft.reload()
    expect(draft.form.logical_limit_bytes).toBe(400)
    expect(draft.dirty.value).toBe(false)
  })
  it('does not downgrade a restricted saved mode or dirty draft on runtime refresh', () => {
    const draft = createStoragePolicyDraft()
    draft.receive(policy())
    draft.form.logical_limit_bytes = 999
    draft.receive({
      ...policy(),
      allowed_policy_modes: ['site_only'],
      runtime: { deployment_enabled: false, maintenance: false },
    })
    expect(draft.form).toEqual({ mode: 'both', default_choice: 'user', logical_limit_bytes: 999 })
    draft.chooseMode('site_only')
    expect(draft.form).toEqual({
      mode: 'site_only',
      default_choice: 'site',
      logical_limit_bytes: 999,
    })
    expect(draft.dirty.value).toBe(true)
  })
  it('accepts the PUT response before a refresh and protects edits made during that refresh', () => {
    const draft = createStoragePolicyDraft()
    draft.receive(policy())
    draft.form.logical_limit_bytes = 200
    const frozen = draft.snapshot()!
    draft.accept({ ...policy(3), logical_limit_bytes: 200 })
    expect(draft.dirty.value).toBe(false)
    expect(draft.conflict.value).toBe(false)
    draft.form.logical_limit_bytes = 300
    draft.receive({ ...policy(3), logical_limit_bytes: 200 })
    expect(draft.snapshot()?.generation).toBe(3)
    expect(draft.form.logical_limit_bytes).toBe(300)
    expect(frozen.logical_limit_bytes).toBe(200)
    expect(frozen.generation).toBe(2)
    draft.clear()
    expect(draft.snapshot()).toBeNull()
    expect(draft.form.logical_limit_bytes).toBe(1)
  })
})
