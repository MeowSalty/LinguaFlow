import { computed, reactive, shallowRef } from 'vue'
import type { ApiSchemas } from '@/api/client-core'
import { quotaDraft, resolveQuota, sameQuotaDraft, type QuotaDraft } from './storage-quota'

type Policy = ApiSchemas['StoragePolicy']
type Request = ApiSchemas['StoragePolicyRequest']
type Draft = Pick<Request, 'mode' | 'default_choice'> & {
  logical_limit_bytes: QuotaDraft
  default_space_capacity_bytes: QuotaDraft
}
const editable = (value: Policy | Request): Draft => ({
  mode: value.mode,
  default_choice: value.default_choice,
  logical_limit_bytes: quotaDraft(value.logical_limit_bytes),
  default_space_capacity_bytes: quotaDraft(value.default_space_capacity_bytes),
})
const empty = (): Draft => ({
  mode: 'site_only',
  default_choice: 'site',
  logical_limit_bytes: { mode: 'unselected' },
  default_space_capacity_bytes: { mode: 'unselected' },
})
const same = (left: Draft, right: Draft) =>
  left.mode === right.mode &&
  left.default_choice === right.default_choice &&
  sameQuotaDraft(left.logical_limit_bytes, right.logical_limit_bytes) &&
  sameQuotaDraft(left.default_space_capacity_bytes, right.default_space_capacity_bytes)

/** Read refreshes may change capabilities, but never silently rebase raw edited fields. */
export function createStoragePolicyDraft() {
  const form = reactive<Draft>(empty())
  const baseline = shallowRef<Policy | null>(null)
  const server = shallowRef<Policy | null>(null)
  const dirty = computed(() => !!baseline.value && !same(form, editable(baseline.value)))
  const valid = computed(
    () =>
      resolveQuota(form.logical_limit_bytes).ok &&
      resolveQuota(form.default_space_capacity_bytes).ok,
  )
  const conflict = computed(
    () =>
      !!baseline.value && !!server.value && baseline.value.generation !== server.value.generation,
  )
  function accept(value: Policy) {
    server.value = baseline.value = value
    Object.assign(form, editable(value))
  }
  function receive(value: Policy) {
    if (!baseline.value || !dirty.value) accept(value)
    else server.value = value
  }
  function restore(before: Policy, submitted: Request, latest: Policy | null) {
    baseline.value = before
    server.value = latest ?? before
    Object.assign(form, editable(submitted))
  }
  function reload() {
    if (server.value) accept(server.value)
  }
  function adoptBaseline(value = server.value) {
    if (value) server.value = baseline.value = value
  }
  function chooseMode(mode: Draft['mode']) {
    form.mode = mode
    if (mode === 'site_only') form.default_choice = 'site'
    else if (mode === 'user_required') form.default_choice = 'user'
  }
  function snapshot(): Request | null {
    const logical = resolveQuota(form.logical_limit_bytes)
    const capacity = resolveQuota(form.default_space_capacity_bytes)
    if (!baseline.value || conflict.value || !logical.ok || !capacity.ok) return null
    return {
      mode: form.mode,
      default_choice: form.default_choice,
      generation: baseline.value.generation,
      logical_limit_bytes: logical.value,
      default_space_capacity_bytes: capacity.value,
    }
  }
  function clear() {
    baseline.value = server.value = null
    Object.assign(form, empty())
  }
  return {
    form,
    baseline,
    server,
    dirty,
    valid,
    conflict,
    accept,
    receive,
    restore,
    reload,
    adoptBaseline,
    chooseMode,
    snapshot,
    clear,
  }
}
