<script setup lang="ts">
import { computed, onUnmounted, ref, shallowRef, watch } from 'vue'
import { storageConfirmationButtons } from '@/components/storage/confirmation'
import {
  NAlert,
  NButton,
  NCard,
  NForm,
  NFormItem,
  NSelect,
  NSkeleton,
  useDialog,
  useMessage,
} from 'naive-ui'
import { useI18n } from 'vue-i18n'
import type { ApiSchemas } from '@/api/client-core'
import { getStorageDiagnostics } from '@/api/storage'
import { ApiError } from '@/api/utils'
import { captureSession, isSessionCurrent, sessionGeneration } from '@/api/session-context'
import {
  storageAccessDenied,
  storageErrorMessage,
  storageTaskErrorMessage,
} from '@/api/storage-errors'
import { createStoragePolicyDraft } from '@/utils/storage-policy-draft'
import { hasStoragePolicyCapabilities } from '@/utils/storage-availability'
import { invalidateStorageSnapshots, subscribeStorageRefresh } from '@/utils/storage-snapshots'
import { useStorageStore } from '@/stores/storage'
import { useAuthStore } from '@/stores/auth'
import { formatDateTime } from '@/utils/datetime'
import PageHeader from '@/components/common/PageHeader.vue'
import StorageManager from '@/components/storage/StorageManager.vue'
import StorageQuotaInput from '@/components/storage/StorageQuotaInput.vue'
import StorageDiagnostics from '@/components/storage/StorageDiagnostics.vue'
import StorageTabs from '@/components/storage/StorageTabs.vue'
import StorageAppearance from '@/components/storage/StorageAppearance.vue'
import { formatStorageQuota } from '@/components/storage/capacity'
import { resolveQuota, type QuotaDraft } from '@/utils/storage-quota'

const { t } = useI18n(),
  message = useMessage(),
  dialog = useDialog()
const store = useStorageStore(),
  auth = useAuthStore()
const activeTab = ref<'overview' | 'connections' | 'policy'>('overview')
const tabs = computed(() => [
  { name: 'overview', label: t('storageAdmin.overview') },
  { name: 'connections', label: t('storageAdmin.connections') },
  { name: 'policy', label: t('storageAdmin.policy') },
])
function changeTab(value: string) {
  if (value === 'overview' || value === 'connections' || value === 'policy') activeTab.value = value
}
const draft = createStoragePolicyDraft()
const { form, dirty, conflict } = draft
const modeAllowed = computed(
  () =>
    !!store.policy &&
    hasStoragePolicyCapabilities(store.policy) &&
    store.policy.allowed_policy_modes.includes(form.mode),
)
const canSave = computed(
  () =>
    auth.user?.role === 'admin' &&
    store.scope.kind === 'site' &&
    !store.denied &&
    !!draft.baseline.value &&
    !conflict.value &&
    modeAllowed.value &&
    !store.policyStale &&
    !store.policyLoading &&
    !store.busy.policy &&
    !store.unknownWrites.policy &&
    draft.valid.value &&
    (form.mode !== 'site_only' || form.default_choice === 'site') &&
    (form.mode !== 'user_required' || form.default_choice === 'user'),
)
const modes = computed(() =>
  (['site_only', 'both', 'user_required'] as const).map((value) => ({
    value,
    label: t(`storage.modes.${value}`),
    disabled: !store.policy?.allowed_policy_modes?.includes(value),
  })),
)
const choices = computed(() =>
  (form.mode === 'site_only'
    ? (['site'] as const)
    : form.mode === 'user_required'
      ? (['user'] as const)
      : (['site', 'user'] as const)
  ).map((value) => ({ value, label: t(`storage.choices.${value}`) })),
)
const diagnostics = shallowRef<ApiSchemas['StorageDiagnostics'] | null>(null)
const diagnosticsError = ref<string | null>(null),
  diagnosticsLoading = ref(false)
const diagnosticCursors = ref<Array<number | undefined>>([undefined])
const diagnosticPage = ref(0)
const cursor = computed(() => diagnosticCursors.value[diagnosticPage.value])
const diagnosticsStale = ref(false)
const diagnosticsUpdatedAt = ref<string | null>(null)
const names = computed(() => {
  const result: Record<number, string> = {}
  if (
    auth.user?.role !== 'admin' ||
    store.scope.kind !== 'site' ||
    store.denied ||
    !store.connections.loaded ||
    store.connections.stale
  )
    return result
  for (const connection of store.connections.items) {
    const spaces = store.spaces[connection.id]
    if (connection.scope !== 'site' || !spaces?.loaded || spaces.stale) continue
    for (const space of spaces.items)
      if (space.connection_id === connection.id) result[space.id] = space.name
  }
  return result
})
let sequence = 0,
  controller = new AbortController()
let active = true
let recoveryAttempt: number | null = null
function synchronizeDraft() {
  const recovery = store.policyRecovery
  if (recovery) {
    if (recoveryAttempt !== recovery.attemptId || !draft.baseline.value) {
      draft.restore(recovery.baseline, recovery.submitted, recovery.latest)
      recoveryAttempt = recovery.attemptId
    } else if (recovery.latest) draft.server.value = recovery.latest
  } else {
    recoveryAttempt = null
    if (store.policy) draft.receive(store.policy)
    else draft.clear()
  }
}
watch(() => [store.policy, store.policyRecovery], synchronizeDraft, { immediate: true })
function describeQuota(value: number | null | undefined) {
  const formatted = formatStorageQuota(value, t('storageCapacity.unlimited'))
  return typeof value === 'number' && Number.isSafeInteger(value) && value > 0
    ? `${formatted} (${t('storage.bytes', { value: value.toLocaleString() })})`
    : formatted === '—'
      ? t('storageCapacity.unavailable')
      : formatted
}
function describePolicy(value: ApiSchemas['StoragePolicyRequest']) {
  return `${t('storage.policyMode')}：${t(`storage.modes.${value.mode}`)}；${t('storage.policyDefault')}：${t(`storage.choices.${value.default_choice}`)}；${t('storageAdmin.logicalLimit')}：${describeQuota(value.logical_limit_bytes)}；${t('storageAdmin.defaultSpaceCapacity')}：${describeQuota(value.default_space_capacity_bytes)}；${t('storageAdmin.policyGeneration', { value: value.generation })}`
}
function describeQuotaDraft(value: QuotaDraft) {
  const result = resolveQuota(value)
  return result.ok
    ? describeQuota(result.value)
    : value.mode === 'limited'
      ? `${value.input || '—'} ${value.unit} (${t('storageAdmin.invalidDraft')})`
      : t('storageCapacity.selectMode')
}
function describeCurrentDraft() {
  return `${t('storage.policyMode')}：${t(`storage.modes.${form.mode}`)}；${t('storage.policyDefault')}：${t(`storage.choices.${form.default_choice}`)}；${t('storageAdmin.logicalLimit')}：${describeQuotaDraft(form.logical_limit_bytes)}；${t('storageAdmin.defaultSpaceCapacity')}：${describeQuotaDraft(form.default_space_capacity_bytes)}`
}
function reviewUnknown(loadLatest: boolean) {
  const recovery = store.policyRecovery
  if (!recovery || recovery.state !== 'ready' || !recovery.latest) return
  const session = captureSession()
  const { attemptId, reviewRevision } = recovery
  dialog.warning({
    title: t('storageAdmin.reviewUnknownTitle'),
    content: t(loadLatest ? 'storageAdmin.confirmLoadLatest' : 'storageAdmin.confirmKeepDraft'),
    positiveText: t('storageAdmin.confirmReviewed'),
    negativeText: t('storage.cancel'),
    ...storageConfirmationButtons,
    onPositiveClick: () => {
      if (
        !active ||
        !isSessionCurrent(session) ||
        auth.user?.role !== 'admin' ||
        store.scope.kind !== 'site' ||
        store.denied
      )
        return
      const latest = store.acknowledgePolicyUnknown(attemptId, reviewRevision)
      if (!latest) return
      if (loadLatest) draft.accept(latest)
      else draft.adoptBaseline(latest)
    },
  })
}
function reviewBaseline() {
  const latest = draft.server.value,
    baseline = draft.baseline.value
  if (!latest || !baseline || !conflict.value) return
  const session = captureSession()
  dialog.warning({
    title: t('storageManagement.reviewPolicy'),
    content: t('storageManagement.policyComparison', {
      before: describePolicy(baseline),
      after: describePolicy(latest),
      draft: describeCurrentDraft(),
    }),
    positiveText: t('storageManagement.adoptBaseline'),
    negativeText: t('storage.cancel'),
    ...storageConfirmationButtons,
    onPositiveClick: () => {
      if (
        active &&
        isSessionCurrent(session) &&
        auth.user?.role === 'admin' &&
        store.scope.kind === 'site' &&
        !store.denied &&
        !store.unknownWrites.policy &&
        draft.server.value === latest
      )
        draft.adoptBaseline()
    },
  })
}
function clearDiagnostics() {
  ++sequence
  controller.abort()
  diagnostics.value = null
  diagnosticsError.value = null
  diagnosticsLoading.value = false
  diagnosticsStale.value = false
  diagnosticsUpdatedAt.value = null
  diagnosticCursors.value = [undefined]
  diagnosticPage.value = 0
}
async function loadDiagnostics(next?: number, page = diagnosticPage.value) {
  if (auth.user?.role !== 'admin') return
  controller.abort()
  controller = new AbortController()
  const request = ++sequence,
    session = captureSession()
  const current = () =>
    request === sequence && isSessionCurrent(session) && auth.user?.role === 'admin'
  diagnosticsLoading.value = true
  diagnosticsError.value = null
  diagnosticsStale.value = !!diagnostics.value
  try {
    const result = await getStorageDiagnostics(
      { cursor: next, limit: 50 },
      { signal: controller.signal },
    )
    if (current()) {
      diagnostics.value = result
      // Commit navigation only after a successful read; failures preserve the previous page.
      if (page > diagnosticPage.value)
        diagnosticCursors.value = [...diagnosticCursors.value.slice(0, page), next]
      diagnosticPage.value = page
      diagnosticsStale.value = false
      diagnosticsUpdatedAt.value = new Date().toISOString()
    }
  } catch (error) {
    if (current()) {
      if (storageAccessDenied(error) || (error instanceof ApiError && error.status === 401))
        clearDiagnostics()
      diagnosticsError.value = storageErrorMessage(error)
    }
  } finally {
    if (current()) diagnosticsLoading.value = false
  }
}
function previousDiagnosticsPage() {
  if (diagnosticsLoading.value || diagnosticPage.value === 0) return
  const page = diagnosticPage.value - 1
  void loadDiagnostics(diagnosticCursors.value[page], page)
}
function nextDiagnosticsPage() {
  if (diagnosticsLoading.value || diagnostics.value?.next_cursor === undefined) return
  void loadDiagnostics(diagnostics.value.next_cursor, diagnosticPage.value + 1)
}
function refreshCurrentTab() {
  if (activeTab.value === 'overview') void loadDiagnostics(cursor.value)
  else if (activeTab.value === 'policy') void store.loadPolicy()
}
async function savePolicy() {
  const snapshot = draft.snapshot()
  if (!canSave.value || !snapshot) return
  const result = await store.savePolicy(snapshot)
  if (result.status === 'success') {
    invalidateStorageSnapshots()
    if (!active) return
    draft.accept(result.value)
    message.success(t('storage.saved'))
  }
}
const unsubscribeRefresh = subscribeStorageRefresh({
  invalidate: () => {
    store.markPolicyStale()
    ++sequence
    controller.abort()
    diagnosticsLoading.value = false
    diagnosticsStale.value = !!diagnostics.value
  },
  refresh: async () => {
    await Promise.all([
      store.loadPolicy(),
      ...(activeTab.value === 'overview' ? [loadDiagnostics(cursor.value)] : []),
    ])
  },
})
watch(activeTab, (tab) => {
  if (
    tab === 'overview' &&
    !diagnosticsLoading.value &&
    (!diagnostics.value || diagnosticsStale.value)
  )
    void loadDiagnostics(cursor.value)
})
watch(
  () => store.denied,
  (denied) => {
    if (denied) clearDiagnostics()
  },
)
watch(
  () => [sessionGeneration.value, auth.user?.role],
  () => {
    clearDiagnostics()
    draft.clear()
    if (auth.user?.role === 'admin') {
      store.setScope({ kind: 'site' })
      synchronizeDraft()
      void store.loadPolicy()
      void loadDiagnostics()
    } else store.invalidate()
  },
  { immediate: true },
)
onUnmounted(() => {
  active = false
  draft.clear()
  unsubscribeRefresh()
  ++sequence
  controller.abort()
})
</script>
<template>
  <StorageAppearance>
    <div class="lf-page lf-content-narrow min-w-0" data-testid="admin-storage-page">
      <PageHeader :title="t('storage.adminTitle')" :subtitle="t('storageAdmin.description')">
        <template #actions>
          <NButton
            v-if="activeTab !== 'connections' && auth.user?.role === 'admin'"
            :loading="activeTab === 'overview' ? diagnosticsLoading : store.policyLoading"
            @click="refreshCurrentTab"
            >{{ t('storage.refresh') }}</NButton
          >
        </template>
      </PageHeader>
      <NAlert v-if="auth.user?.role !== 'admin'" type="warning">{{ t('storage.denied') }}</NAlert>
      <StorageTabs
        :keep-mounted="['policy']"
        v-else
        :value="activeTab"
        :tabs="tabs"
        :label="t('storage.adminTitle')"
        @update:value="changeTab"
      >
        <template #overview>
          <div class="space-y-5" :aria-busy="diagnosticsLoading">
            <NAlert v-if="diagnosticsError" type="warning">{{ diagnosticsError }}</NAlert>
            <NAlert v-if="diagnosticsStale && diagnostics" type="info">
              {{ t('storageAdmin.snapshotStale') }}
            </NAlert>
            <p v-if="diagnosticsUpdatedAt" class="text-xs text-lf-text-muted">
              {{
                t('storageAdmin.updatedAt', {
                  time: formatDateTime(diagnosticsUpdatedAt, {
                    dateStyle: 'medium',
                    timeStyle: 'short',
                  }),
                })
              }}
            </p>
            <template v-if="diagnosticsLoading && !diagnostics">
              <NSkeleton height="180px" />
              <NSkeleton height="260px" />
            </template>
            <template v-if="diagnostics">
              <StorageDiagnostics
                :diagnostics="diagnostics"
                :names="names"
                :stale="diagnosticsStale"
              />
              <div class="flex flex-wrap items-center justify-between gap-3">
                <p class="max-w-md text-xs leading-5 text-lf-text-muted">
                  {{ t('storageAdmin.pageHint') }}
                </p>
                <div class="flex flex-wrap items-center gap-2">
                  <NButton
                    :disabled="diagnosticsLoading || diagnosticPage === 0"
                    @click="previousDiagnosticsPage"
                    >{{ t('storageAdmin.previousPage') }}</NButton
                  >
                  <span class="px-1 text-xs tabular-nums text-lf-text-muted" aria-live="polite">{{
                    t('storageAdmin.page', { page: diagnosticPage + 1 })
                  }}</span>
                  <NButton
                    :disabled="diagnosticsLoading || diagnostics.next_cursor === undefined"
                    @click="nextDiagnosticsPage"
                    >{{ t('storageAdmin.nextPage') }}</NButton
                  >
                </div>
              </div>
            </template>
          </div>
        </template>
        <template #connections>
          <StorageManager :scope="{ kind: 'site' }" />
        </template>
        <template #policy>
          <NCard :title="t('storageAdmin.policyTitle')" size="small">
            <p class="mb-5 text-sm text-lf-text-muted">{{ t('storageAdmin.policyDescription') }}</p>
            <NAlert
              v-if="store.policyError || store.writeErrors.policy"
              type="warning"
              class="mb-4"
              >{{ store.policyError || store.writeErrors.policy }}</NAlert
            >
            <NAlert v-if="store.policySavedPendingRefresh" type="info" class="mb-4">{{
              t('storageManagement.savedPendingRefresh')
            }}</NAlert>
            <NAlert v-else-if="store.policyStale && store.policy" type="info" class="mb-4">{{
              t('storageAdmin.policySnapshotStale')
            }}</NAlert>
            <NAlert
              v-if="store.policyRecovery"
              type="warning"
              class="mb-4"
              data-testid="policy-unknown-review"
            >
              <p>{{ t('storageAdmin.policyUnknown') }}</p>
              <dl class="mt-3 space-y-3 text-xs leading-5 break-words">
                <div>
                  <dt class="font-medium">{{ t('storageAdmin.originalPolicy') }}</dt>
                  <dd>{{ describePolicy(store.policyRecovery.baseline) }}</dd>
                </div>
                <div>
                  <dt class="font-medium">{{ t('storageAdmin.submittedPolicy') }}</dt>
                  <dd>{{ describePolicy(store.policyRecovery.submitted) }}</dd>
                </div>
                <div>
                  <dt class="font-medium">{{ t('storageAdmin.latestPolicy') }}</dt>
                  <dd>
                    {{
                      store.policyRecovery.latest
                        ? describePolicy(store.policyRecovery.latest)
                        : t('storageAdmin.latestPolicyUnavailable')
                    }}
                  </dd>
                </div>
              </dl>
              <p v-if="store.policyRecovery.state === 'error'" class="mt-3 text-xs">
                {{ t('storageAdmin.reviewReadFailed') }}
              </p>
              <div class="mt-3 flex flex-wrap gap-2">
                <NButton
                  :loading="store.policyRecovery.state === 'loading'"
                  @click="store.loadPolicy()"
                  >{{ t('storageAdmin.readLatestPolicy') }}</NButton
                >
                <NButton
                  :disabled="store.policyRecovery.state !== 'ready'"
                  @click="reviewUnknown(false)"
                  >{{ t('storageAdmin.reviewKeepDraft') }}</NButton
                >
                <NButton
                  :disabled="store.policyRecovery.state !== 'ready'"
                  @click="reviewUnknown(true)"
                  >{{ t('storageAdmin.reviewLoadLatest') }}</NButton
                >
              </div>
            </NAlert>
            <NSkeleton v-if="store.policyLoading && !store.policy" height="280px" />
            <NForm v-else-if="store.policy" label-placement="top" @submit.prevent="savePolicy">
              <NAlert
                v-if="store.policy.runtime?.deployment_enabled === false"
                type="info"
                class="mb-4"
                >{{ t('storageManagement.deploymentSetup') }}</NAlert
              >
              <NAlert v-if="store.policy.runtime?.maintenance" type="info" class="mb-4">{{
                t('storageManagement.policyMaintenance')
              }}</NAlert>
              <NAlert v-if="store.policy.configuration_needs_update" type="warning" class="mb-4">{{
                t('storageManagement.policyConfiguration')
              }}</NAlert>
              <p
                v-for="code in store.policy.policy_restriction_codes ?? []"
                :key="code"
                class="mb-3 text-sm text-lf-text-muted"
              >
                {{ storageTaskErrorMessage(code) }}
              </p>
              <NAlert v-if="!modeAllowed" type="warning" class="mb-4">
                <p>{{ t('storageManagement.modeRestricted') }}</p>
                <NButton
                  v-if="store.policy.allowed_policy_modes?.includes('site_only')"
                  class="mt-3"
                  :disabled="!!store.busy.policy || !!store.unknownWrites.policy"
                  @click="draft.chooseMode('site_only')"
                  >{{ t('storageManagement.useSiteOnly') }}</NButton
                >
                <p class="mt-2 text-xs">{{ t('storageManagement.siteOnlyHint') }}</p>
              </NAlert>
              <NAlert v-if="conflict && !store.policyRecovery" type="warning" class="mb-4">
                <p>{{ t('storageManagement.policyConflict') }}</p>
                <div class="mt-3 flex flex-wrap gap-2">
                  <NButton :disabled="!!store.busy.policy" @click="draft.reload()">{{
                    t('storageManagement.reloadPolicy')
                  }}</NButton>
                  <NButton :disabled="!!store.busy.policy" @click="reviewBaseline">{{
                    t('storageManagement.reviewPolicy')
                  }}</NButton>
                </div>
              </NAlert>
              <div class="grid grid-cols-1 gap-x-4 sm:grid-cols-2">
                <NFormItem :label="t('storage.policyMode')">
                  <NSelect
                    :value="form.mode"
                    :options="modes"
                    :disabled="!!store.busy.policy || !!store.unknownWrites.policy"
                    @update:value="draft.chooseMode"
                  />
                </NFormItem>
                <NFormItem :label="t('storage.policyDefault')">
                  <NSelect
                    v-model:value="form.default_choice"
                    :options="choices"
                    :disabled="!!store.busy.policy || !!store.unknownWrites.policy"
                  />
                </NFormItem>
              </div>
              <NFormItem :label="t('storageAdmin.logicalLimit')">
                <StorageQuotaInput
                  :value="form.logical_limit_bytes"
                  :disabled="!!store.busy.policy || !!store.unknownWrites.policy"
                  :label="t('storageAdmin.logicalLimit')"
                  @update:value="(value) => (form.logical_limit_bytes = value)"
                />
              </NFormItem>
              <p class="-mt-2 mb-5 text-xs leading-5 text-lf-text-muted">
                {{ t('storageAdmin.logicalLimitHint') }}<br />{{
                  t('storageAdmin.logicalLimitImpact')
                }}
              </p>
              <NFormItem :label="t('storageAdmin.defaultSpaceCapacity')">
                <StorageQuotaInput
                  :value="form.default_space_capacity_bytes"
                  :disabled="!!store.busy.policy || !!store.unknownWrites.policy"
                  :label="t('storageAdmin.defaultSpaceCapacity')"
                  @update:value="(value) => (form.default_space_capacity_bytes = value)"
                />
              </NFormItem>
              <p class="-mt-2 mb-5 text-xs leading-5 text-lf-text-muted">
                {{ t('storageAdmin.defaultSpaceCapacityHint') }}
              </p>
              <div class="policy-actions">
                <p class="text-xs leading-5 text-lf-text-muted">
                  {{ t('storage.policyHint') }}
                  <span v-if="dirty" class="block mt-1 font-medium">{{
                    t('storageManagement.unsavedDraft')
                  }}</span>
                </p>
                <div class="flex shrink-0 flex-wrap gap-2">
                  <NButton
                    :disabled="
                      (!dirty && !conflict) || !!store.busy.policy || !!store.unknownWrites.policy
                    "
                    @click="draft.reload()"
                    >{{ t('storageAdmin.cancelChanges') }}</NButton
                  >
                  <NButton
                    type="primary"
                    attr-type="submit"
                    :loading="!!store.busy.policy"
                    :disabled="!canSave || !dirty"
                    >{{ t('storageAdmin.saveChanges') }}</NButton
                  >
                </div>
              </div>
            </NForm>
            <p v-else-if="!store.policyError" class="text-sm text-lf-text-muted">
              {{ t('storageAdmin.policyEmpty') }}
            </p>
          </NCard>
        </template>
      </StorageTabs>
    </div>
  </StorageAppearance>
</template>

<style scoped>
.policy-actions {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  justify-content: space-between;
  gap: 16px;
  border-top: 1px solid var(--lf-border-soft);
  padding-top: 20px;
}
.policy-actions > p {
  flex: 1 1 250px;
}
@media (max-width: 479px) {
  .policy-actions > div {
    width: 100%;
  }
  .policy-actions > div > * {
    flex: 1 1 auto;
  }
}
</style>
