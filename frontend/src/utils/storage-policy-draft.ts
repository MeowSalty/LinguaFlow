import { computed, reactive, shallowRef } from 'vue'
import type { ApiSchemas } from '@/api/client-core'

type Policy = ApiSchemas['StoragePolicy']
type Draft = Omit<ApiSchemas['StoragePolicyRequest'], 'generation'>
const editable = (value: Policy): Draft => ({
  mode: value.mode,
  default_choice: value.default_choice,
  logical_limit_bytes: value.logical_limit_bytes,
})
const same = (left: Draft, right: Draft) =>
  left.mode === right.mode &&
  left.default_choice === right.default_choice &&
  left.logical_limit_bytes === right.logical_limit_bytes

/** Read refreshes may change available modes, but never silently rebase edited fields. */
export function createStoragePolicyDraft() {
  const form = reactive<Draft>({
    mode: 'site_only',
    default_choice: 'site',
    logical_limit_bytes: 1,
  })
  const baseline = shallowRef<Policy | null>(null)
  const server = shallowRef<Policy | null>(null)
  const dirty = computed(() => !!baseline.value && !same(form, baseline.value))
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
  function reload() {
    if (server.value) accept(server.value)
  }
  function adoptBaseline() {
    if (server.value) baseline.value = server.value
  }
  function chooseMode(mode: Draft['mode']) {
    form.mode = mode
    if (mode === 'site_only') form.default_choice = 'site'
    else if (mode === 'user_required') form.default_choice = 'user'
  }
  function snapshot(): ApiSchemas['StoragePolicyRequest'] | null {
    return baseline.value && !conflict.value
      ? { ...form, generation: baseline.value.generation }
      : null
  }
  function clear() {
    baseline.value = server.value = null
    Object.assign(form, { mode: 'site_only', default_choice: 'site', logical_limit_bytes: 1 })
  }
  return {
    form,
    baseline,
    server,
    dirty,
    conflict,
    accept,
    receive,
    reload,
    adoptBaseline,
    chooseMode,
    snapshot,
    clear,
  }
}
