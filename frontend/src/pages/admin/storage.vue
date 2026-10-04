<script setup lang="ts">
import { computed, onUnmounted, reactive, ref, shallowRef, watch } from 'vue'
import {
  NAlert,
  NButton,
  NCard,
  NForm,
  NFormItem,
  NInputNumber,
  NSelect,
  NSkeleton,
  useMessage,
} from 'naive-ui'
import { useI18n } from 'vue-i18n'
import type { ApiSchemas } from '@/api/client-core'
import { getStorageDiagnostics } from '@/api/storage'
import { captureSession, isSessionCurrent, sessionGeneration } from '@/api/session-context'
import { isAccessDenied } from '@/api/utils'
import { useStorageStore } from '@/stores/storage'
import { useAuthStore } from '@/stores/auth'
import { formatDateTime } from '@/utils/datetime'
import PageHeader from '@/components/common/PageHeader.vue'
import StorageManager from '@/components/storage/StorageManager.vue'
import StorageCapacity from '@/components/storage/StorageCapacity.vue'

const { t } = useI18n(),
  message = useMessage()
const store = useStorageStore(),
  auth = useAuthStore()
const form = reactive({
  mode: 'site_only' as ApiSchemas['StoragePolicy']['mode'],
  default_choice: 'site' as ApiSchemas['StoragePolicy']['default_choice'],
  logical_limit_bytes: 1,
})
const modes = computed(() =>
  (['site_only', 'both', 'user_required'] as const).map((value) => ({
    value,
    label: t(`storage.modes.${value}`),
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
watch(
  () => store.policy,
  (value) => {
    if (value)
      Object.assign(form, {
        mode: value.mode,
        default_choice: value.default_choice,
        logical_limit_bytes: value.logical_limit_bytes,
      })
  },
)
watch(
  () => form.mode,
  (value) => {
    if (value === 'site_only') form.default_choice = 'site'
    else if (value === 'user_required') form.default_choice = 'user'
  },
)
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
      if (isAccessDenied(error)) diagnostics.value = null
      diagnosticsError.value = t(isAccessDenied(error) ? 'storage.denied' : 'storage.readFailed')
    }
  } finally {
    if (current()) diagnosticsLoading.value = false
  }
}
async function savePolicy() {
  if (
    !store.policy ||
    !Number.isSafeInteger(form.logical_limit_bytes) ||
    form.logical_limit_bytes < 1
  )
    return
  const result = await store.savePolicy({ ...form, generation: store.policy.generation })
  if (result.status === 'success') {
    message.success(t('storage.saved'))
    await store.loadPolicy()
  }
}
watch(
  () => [sessionGeneration.value, auth.user?.role],
  () => {
    ++sequence
    controller.abort()
    diagnostics.value = null
    diagnosticsError.value = null
    diagnosticsLoading.value = false
    if (auth.user?.role === 'admin') {
      store.setScope({ kind: 'site' })
      void store.loadPolicy()
      void loadDiagnostics()
    } else store.invalidate()
  },
  { immediate: true },
)
onUnmounted(() => {
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
      <NSkeleton v-if="store.policyLoading && !store.policy" height="180px" />
      <NForm v-else-if="store.policy" label-placement="top" @submit.prevent="savePolicy">
        <div class="grid grid-cols-1 gap-x-4 sm:grid-cols-2">
          <NFormItem :label="t('storage.policyMode')"
            ><NSelect v-model:value="form.mode" :options="modes"
          /></NFormItem>
          <NFormItem :label="t('storage.policyDefault')"
            ><NSelect v-model:value="form.default_choice" :options="choices"
          /></NFormItem>
        </div>
        <NFormItem :label="t('storage.logicalLimit')"
          ><NInputNumber
            :value="form.logical_limit_bytes"
            :min="1"
            :max="Number.MAX_SAFE_INTEGER"
            :precision="0"
            class="w-full"
            @update:value="(value) => (form.logical_limit_bytes = value ?? 0)"
        /></NFormItem>
        <div class="flex flex-wrap items-center justify-between gap-3">
          <p class="text-xs text-lf-text-muted">{{ t('storage.policyHint') }}</p>
          <NButton
            type="primary"
            attr-type="submit"
            :loading="!!store.busy.policy"
            :disabled="store.policyStale || store.policyLoading || !!store.unknownWrites.policy"
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
