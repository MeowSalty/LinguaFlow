<script setup lang="ts">
import { computed, onScopeDispose, ref, shallowRef, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  NAlert,
  NButton,
  NCard,
  NDrawer,
  NDrawerContent,
  NEmpty,
  NModal,
  NSpace,
  NSpin,
  NTag,
  useMessage,
} from 'naive-ui'
import type { ApiSchemas } from '@/api/client-core'
import { fetchAuthConnections, type AuthConnection } from '@/api/auth-connections'
import { ApiError } from '@/api/utils'
import { captureSession, isSessionCurrent, sessionGeneration } from '@/api/session-context'
import { startExternalTest, takeExternalTestResult } from '@/auth/external-browser'
import { useAuthStore } from '@/stores/auth'
import { useServiceStore } from '@/stores/service'
import AuthConnectionCreate from '@/components/admin/authentication/AuthConnectionCreate.vue'
import AuthConnectionEditor from '@/components/admin/authentication/AuthConnectionEditor.vue'
import AuthDiagnostics from '@/components/admin/authentication/AuthDiagnostics.vue'
const { t } = useI18n()
const message = useMessage()
const auth = useAuthStore()
const service = useServiceStore()
const allowed = computed(() => !service.isLocal && auth.user?.role === 'admin')
const connections = shallowRef<AuthConnection[]>([])
const selected = shallowRef<AuthConnection | null>(null)
const testResult = shallowRef<ApiSchemas['ExternalTestResponse'] | null>(null)
const creating = ref(false)
const loading = ref(false)
const testing = ref('')
const error = ref('')
let request = 0
let alive = true
onScopeDispose(() => {
  alive = false
  request++
})
const sorted = computed(() =>
  [...connections.value].sort(
    (a, b) =>
      Number(a.archived) - Number(b.archived) ||
      (a.published?.display_order ?? a.draft?.display_order ?? 0) -
        (b.published?.display_order ?? b.draft?.display_order ?? 0),
  ),
)
async function load() {
  if (!allowed.value) return
  const context = captureSession()
  const sequence = ++request
  loading.value = true
  error.value = ''
  try {
    const items = await fetchAuthConnections()
    if (alive && isSessionCurrent(context) && request === sequence) connections.value = items
  } catch (cause) {
    if (
      alive &&
      isSessionCurrent(context) &&
      request === sequence &&
      cause instanceof ApiError &&
      [403, 404].includes(cause.status ?? 0)
    ) {
      connections.value = []
      selected.value = null
      testResult.value = null
    }
    if (alive && isSessionCurrent(context) && request === sequence)
      error.value = cause instanceof Error ? cause.message : t('authAdmin.requestFailed')
  } finally {
    if (alive && isSessionCurrent(context) && request === sequence) loading.value = false
  }
}
function changed(record: AuthConnection) {
  connections.value = [...connections.value.filter((item) => item.id !== record.id), record]
}
function denied(detail: string) {
  connections.value = []
  selected.value = null
  testResult.value = null
  creating.value = false
  error.value = detail
}
function created(record: AuthConnection) {
  changed(record)
  creating.value = false
  selected.value = record
  message.success(t('authAdmin.created'))
}
async function test(record: AuthConnection) {
  if (
    !allowed.value ||
    testing.value ||
    record.archived ||
    !record.draft ||
    record.kind === 'password'
  )
    return
  const context = captureSession()
  testing.value = record.id
  error.value = ''
  try {
    await startExternalTest(
      record.id,
      record.aggregate_revision,
      record.draft_revision ?? record.draft.revision,
    )
  } catch (cause) {
    if (alive && isSessionCurrent(context))
      error.value = cause instanceof Error ? cause.message : t('authAdmin.requestFailed')
  } finally {
    if (alive && isSessionCurrent(context)) testing.value = ''
  }
}
watch(
  [sessionGeneration, allowed],
  () => {
    request++
    connections.value = []
    selected.value = null
    creating.value = false
    error.value = ''
    testing.value = ''
    loading.value = false
    testResult.value = allowed.value ? takeExternalTestResult() : null
    if (allowed.value) void load()
  },
  { immediate: true, flush: 'sync' },
)
</script>

<template>
  <main class="mx-auto w-full max-w-6xl space-y-6 p-4 sm:p-6">
    <header class="flex flex-wrap items-start justify-between gap-4">
      <div>
        <h1 class="text-2xl font-semibold text-lf-text">{{ t('authAdmin.title') }}</h1>
        <p class="mt-2 max-w-3xl text-sm text-lf-text-muted">{{ t('authAdmin.subtitle') }}</p>
      </div>
      <n-space v-if="allowed"
        ><n-button :disabled="loading || Boolean(testing)" @click="load">{{
          t('authAdmin.refresh')
        }}</n-button
        ><n-button type="primary" :disabled="Boolean(testing)" @click="creating = true">{{
          t('authAdmin.create')
        }}</n-button></n-space
      >
    </header>
    <n-alert v-if="!allowed" type="info">{{ t('authAdmin.serverOnly') }}</n-alert>
    <template v-else>
      <n-alert v-if="error" type="error" role="alert"
        ><div class="space-y-3">
          <p>{{ error }}</p>
          <n-button :disabled="loading" @click="load">{{ t('authAdmin.retry') }}</n-button>
        </div></n-alert
      >
      <n-card v-if="testResult" :title="t('authAdmin.testResult')" size="small"
        ><p class="mb-3 text-sm">
          {{ testResult.connection_id }} ·
          {{ t('authAdmin.revision', { revision: testResult.revision }) }} ·
          {{
            t(
              testResult.fresh_supported
                ? 'authAdmin.freshSupported'
                : 'authAdmin.freshUnsupported',
            )
          }}
        </p>
        <AuthDiagnostics :rules="testResult.rules" :attributes="testResult.attributes"
      /></n-card>
      <n-spin :show="loading"
        ><div class="min-h-24 space-y-4">
          <n-empty
            v-if="!loading && !error && !connections.length"
            :description="t('authAdmin.empty')"
            class="py-12"
          />
          <n-card
            v-for="connection in sorted"
            :key="connection.id"
            :data-testid="'auth-connection-' + connection.id"
            size="small"
          >
            <div class="flex flex-wrap items-start justify-between gap-4">
              <div class="min-w-0 flex-1">
                <div class="flex flex-wrap items-center gap-2">
                  <h2 class="break-words text-base font-semibold">
                    {{
                      connection.published?.display_name ??
                      connection.draft?.display_name ??
                      connection.id
                    }}
                  </h2>
                  <n-tag size="small" :bordered="false">{{
                    t(`authAdmin.${connection.kind}`)
                  }}</n-tag
                  ><n-tag v-if="connection.archived" size="small" type="warning">{{
                    t('authAdmin.archived')
                  }}</n-tag
                  ><n-tag v-else-if="connection.published" size="small" type="success">{{
                    t('authAdmin.published')
                  }}</n-tag
                  ><n-tag v-if="connection.draft" size="small"
                    >{{ t('authAdmin.draft') }} · {{ connection.draft_revision }}</n-tag
                  >
                </div>
                <p class="mt-2 break-words text-sm text-lf-text-muted">
                  {{ connection.published?.description ?? connection.draft?.description }}
                </p>
                <p class="mt-2 text-xs text-lf-text-muted">
                  {{ t('authAdmin.revision', { revision: connection.aggregate_revision }) }}
                </p>
              </div>
              <n-space
                ><n-button :disabled="Boolean(testing) || loading" @click="selected = connection">{{
                  t(connection.kind === 'password' ? 'authAdmin.viewPolicy' : 'authAdmin.edit')
                }}</n-button
                ><n-button
                  v-if="connection.kind !== 'password'"
                  :disabled="
                    connection.archived || !connection.draft || Boolean(testing) || loading
                  "
                  :loading="testing === connection.id"
                  @click="test(connection)"
                  >{{ t('authAdmin.test') }}</n-button
                ></n-space
              >
            </div>
          </n-card>
        </div></n-spin
      >
      <n-modal
        :show="creating"
        preset="card"
        :title="t('authAdmin.create')"
        :closable="false"
        :mask-closable="false"
        :close-on-esc="false"
        style="width: min(620px, calc(100vw - 32px)); max-height: 90vh; overflow: auto"
        ><AuthConnectionCreate
          v-if="creating"
          @created="created"
          @denied="denied"
          @close="creating = false"
      /></n-modal>
      <n-drawer
        :show="Boolean(selected)"
        :width="820"
        :mask-closable="false"
        :close-on-esc="false"
        style="max-width: 100vw"
        ><n-drawer-content
          :title="t(selected?.kind === 'password' ? 'authAdmin.viewPolicy' : 'authAdmin.edit')"
          ><AuthConnectionEditor
            v-if="selected"
            :key="`${sessionGeneration}:${selected.id}`"
            :connection="selected"
            :test-result="testResult?.connection_id === selected.id ? testResult : null"
            @changed="changed"
            @denied="denied"
            @close="selected = null" /></n-drawer-content
      ></n-drawer>
    </template>
  </main>
</template>
