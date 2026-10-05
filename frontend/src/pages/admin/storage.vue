<script setup lang="ts">
import { computed, onUnmounted, ref, shallowRef, watch } from 'vue'
import {
  NAlert,
  NButton,
  NCard,
  NForm,
  NFormItem,
  NInputNumber,
  NSelect,
  NSkeleton,
  useDialog,
  useMessage,
} from 'naive-ui'
import { useI18n } from 'vue-i18n'
import type { ApiSchemas } from '@/api/client-core'
import { getStorageDiagnostics } from '@/api/storage'
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
import StorageCapacity from '@/components/storage/StorageCapacity.vue'

const { t } = useI18n(),
  message = useMessage(),
  dialog = useDialog()
const store = useStorageStore(),
  auth = useAuthStore()
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
    !!draft.baseline.value &&
    !conflict.value &&
    modeAllowed.value &&
    !store.policyStale &&
    !store.policyLoading &&
    !store.busy.policy &&
    !store.unknownWrites.policy &&
    Number.isSafeInteger(form.logical_limit_bytes) &&
    form.logical_limit_bytes > 0 &&
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
const cursor = ref<number | undefined>(undefined)
let sequence = 0,
  controller = new AbortController()
let active = true
watch(
  () => store.policy,
  (value) => {
    if (value) draft.receive(value)
    else draft.clear()
  },
  { immediate: true },
)
function reviewBaseline() {
  const latest = draft.server.value,
    baseline = draft.baseline.value
  if (!latest || !baseline || !conflict.value) return
  const describe = (value: typeof latest) =>
    `${t(`storage.modes.${value.mode}`)} / ${t(`storage.choices.${value.default_choice}`)} / ${value.logical_limit_bytes.toLocaleString()}`
  const session = captureSession()
  dialog.warning({
    title: t('storageManagement.reviewPolicy'),
    content: t('storageManagement.policyComparison', {
      before: describe(baseline),
      after: describe(latest),
      draft: describe({ ...latest, ...form }),
    }),
    positiveText: t('storageManagement.adoptBaseline'),
    negativeText: t('storage.cancel'),
    onPositiveClick: () => {
      if (
        active &&
        isSessionCurrent(session) &&
        auth.user?.role === 'admin' &&
        draft.server.value === latest
      )
        draft.adoptBaseline()
    },
  })
}
async function loadDiagnostics(next?: number) {
  if (auth.user?.role !== 'admin') return
  controller.abort()
  controller = new AbortController()
  const request = ++sequence,
    session = captureSession()
  const current = () =>
    request === sequence && isSessionCurrent(session) && auth.user?.role === 'admin'
  diagnosticsLoading.value = true
  diagnosticsError.value = null
  try {
    const result = await getStorageDiagnostics(
      { cursor: next, limit: 50 },
      { signal: controller.signal },
    )
    if (current()) {
      diagnostics.value = result
      cursor.value = next
    }
  } catch (error) {
    if (current()) {
      if (storageAccessDenied(error)) diagnostics.value = null
      diagnosticsError.value = storageErrorMessage(error)
    }
  } finally {
    if (current()) diagnosticsLoading.value = false
  }
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
  invalidate: store.markPolicyStale,
  refresh: () => store.loadPolicy(),
})
watch(
  () => [sessionGeneration.value, auth.user?.role],
  () => {
    ++sequence
    controller.abort()
    diagnostics.value = null
    diagnosticsError.value = null
    diagnosticsLoading.value = false
    draft.clear()
    if (auth.user?.role === 'admin') {
      store.setScope({ kind: 'site' })
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
  <div class="lf-page lf-content-narrow">
    <PageHeader :title="t('storage.adminTitle')" :subtitle="t('storage.adminSubtitle')" />
    <NCard :title="t('storage.policy')" size="small">
      <template #header-extra
        ><NButton :loading="store.policyLoading" @click="store.loadPolicy">{{
          t('storage.refresh')
        }}</NButton></template
      >
      <NAlert v-if="store.policyError || store.writeErrors.policy" type="warning" class="mb-4">{{
        store.policyError || store.writeErrors.policy
      }}</NAlert>
      <NAlert v-if="store.policySavedPendingRefresh" type="info" class="mb-4">{{
        t('storageManagement.savedPendingRefresh')
      }}</NAlert>
      <NSkeleton v-if="store.policyLoading && !store.policy" height="180px" />
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
            :disabled="!!store.busy.policy"
            @click="draft.chooseMode('site_only')"
            >{{ t('storageManagement.useSiteOnly') }}</NButton
          >
          <p class="mt-2 text-xs">{{ t('storageManagement.siteOnlyHint') }}</p>
        </NAlert>
        <NAlert v-if="conflict" type="warning" class="mb-4">
          <p>{{ t('storageManagement.policyConflict') }}</p>
          <div class="mt-3 flex flex-wrap gap-2">
            <NButton @click="draft.reload">{{ t('storageManagement.reloadPolicy') }}</NButton
            ><NButton @click="reviewBaseline">{{ t('storageManagement.reviewPolicy') }}</NButton>
          </div>
        </NAlert>
        <div class="grid grid-cols-1 gap-x-4 sm:grid-cols-2">
          <NFormItem :label="t('storage.policyMode')"
            ><NSelect
              :value="form.mode"
              :options="modes"
              :disabled="!!store.busy.policy"
              @update:value="draft.chooseMode"
          /></NFormItem>
          <NFormItem :label="t('storage.policyDefault')"
            ><NSelect
              v-model:value="form.default_choice"
              :options="choices"
              :disabled="!!store.busy.policy"
          /></NFormItem>
        </div>
        <NFormItem :label="t('storage.logicalLimit')"
          ><NInputNumber
            :value="form.logical_limit_bytes"
            :disabled="!!store.busy.policy"
            :min="1"
            :max="Number.MAX_SAFE_INTEGER"
            class="w-full"
            @update:value="(value) => (form.logical_limit_bytes = value ?? 0)"
        /></NFormItem>
        <div class="flex flex-wrap items-center justify-between gap-3">
          <p class="text-xs text-lf-text-muted">
            {{ t('storage.policyHint')
            }}<span v-if="dirty"> · {{ t('storageManagement.unsavedDraft') }}</span>
          </p>
          <NButton
            type="primary"
            attr-type="submit"
            :loading="!!store.busy.policy"
            :disabled="!canSave"
            >{{ t('storage.save') }}</NButton
          >
        </div>
      </NForm>
    </NCard>
    <StorageManager :scope="{ kind: 'site' }" />
    <NCard :title="t('storage.diagnostics')" size="small">
      <template #header-extra
        ><NButton :loading="diagnosticsLoading" @click="loadDiagnostics(cursor)">{{
          t('storage.refresh')
        }}</NButton></template
      >
      <div class="space-y-4">
        <p class="text-sm text-lf-text-muted">{{ t('storage.diagnosticsHint') }}</p>
        <NAlert v-if="diagnosticsError" type="warning">{{ diagnosticsError }}</NAlert>
        <NSkeleton v-if="diagnosticsLoading && !diagnostics" height="160px" />
        <template v-if="diagnostics">
          <dl class="grid grid-cols-2 gap-3 text-sm">
            <div>
              <dt class="text-lf-text-muted">{{ t('storage.temporary') }}</dt>
              <dd>
                {{ t('storage.bytes', { value: diagnostics.temporary_bytes.toLocaleString() }) }}
              </dd>
            </div>
            <div>
              <dt class="text-lf-text-muted">{{ t('storage.recoveryBacklog') }}</dt>
              <dd>{{ diagnostics.recovery_backlog }}</dd>
            </div>
            <div v-if="diagnostics.oldest_intent_at">
              <dt class="text-lf-text-muted">{{ t('storage.oldestIntent') }}</dt>
              <dd>
                {{
                  formatDateTime(diagnostics.oldest_intent_at, {
                    dateStyle: 'medium',
                    timeStyle: 'short',
                  })
                }}
              </dd>
            </div>
            <div>
              <dt class="text-lf-text-muted">{{ t('storage.latestBackup') }}</dt>
              <dd>
                {{
                  diagnostics.latest_backup
                    ? t(`storage.backupStates.${diagnostics.latest_backup.status}`)
                    : t('storage.noBackup')
                }}<span v-if="diagnostics.latest_backup">
                  ·
                  {{
                    formatDateTime(diagnostics.latest_backup.created_at, {
                      dateStyle: 'medium',
                      timeStyle: 'short',
                    })
                  }}</span
                >
              </dd>
            </div>
          </dl>
          <div v-if="Object.keys(diagnostics.blocked_cleanup_by_code).length">
            <h3 class="mb-2 text-sm font-medium">{{ t('storage.blockedCleanup') }}</h3>
            <p
              v-for="(count, code) in diagnostics.blocked_cleanup_by_code"
              :key="code"
              class="text-xs"
            >
              {{ code }} · {{ count }}
            </p>
          </div>
          <div v-if="Object.keys(diagnostics.migrations_by_phase).length">
            <h3 class="mb-2 text-sm font-medium">{{ t('storage.migrationPhases') }}</h3>
            <p
              v-for="(count, phase) in diagnostics.migrations_by_phase"
              :key="phase"
              class="text-xs"
            >
              {{ phase }} · {{ count }}
            </p>
          </div>
          <NCard
            v-for="space in diagnostics.spaces"
            :key="space.id"
            :title="`${t('storage.selectSpace')} #${space.id}`"
            size="small"
          >
            <StorageCapacity :space="space" />
            <dl class="mt-3 grid grid-cols-3 gap-2 text-xs">
              <div>
                <dt>{{ t('storage.unchecked') }}</dt>
                <dd>{{ space.unchecked_objects }}</dd>
              </div>
              <div>
                <dt>{{ t('storage.missing') }}</dt>
                <dd>{{ space.missing_objects }}</dd>
              </div>
              <div>
                <dt>{{ t('storage.corrupt') }}</dt>
                <dd>{{ space.corrupt_objects }}</dd>
              </div>
            </dl>
            <p v-if="space.last_checked_at" class="mt-3 text-xs text-lf-text-subtle">
              {{ t('storage.lastChecked') }} ·
              {{
                formatDateTime(space.last_checked_at, { dateStyle: 'medium', timeStyle: 'short' })
              }}
            </p>
          </NCard>
          <p class="text-xs text-lf-text-subtle">{{ t('storage.diagnosticPageHint') }}</p>
          <div class="flex justify-end gap-2">
            <NButton
              v-if="cursor !== undefined"
              :disabled="diagnosticsLoading"
              @click="loadDiagnostics()"
              >{{ t('storage.firstPage') }}</NButton
            ><NButton
              v-if="diagnostics.next_cursor !== undefined"
              :disabled="diagnosticsLoading"
              @click="loadDiagnostics(diagnostics.next_cursor)"
              >{{ t('storage.nextPage') }}</NButton
            >
          </div>
        </template>
      </div>
    </NCard>
  </div>
</template>
