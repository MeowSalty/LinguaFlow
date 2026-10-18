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
import { fetchAuthConnections, type AuthConnection } from '@/api/auth-connections'
import { ApiError } from '@/api/utils'
import { captureSession, isSessionCurrent, sessionGeneration } from '@/api/session-context'
import { useAuthStore } from '@/stores/auth'
import { useServiceStore } from '@/stores/service'
import AuthConnectionCreate from '@/components/admin/authentication/AuthConnectionCreate.vue'
import AuthConnectionEditor from '@/components/admin/authentication/AuthConnectionEditor.vue'

const { t } = useI18n()
const message = useMessage()
const auth = useAuthStore()
const service = useServiceStore()
/** The editor owns dirty-close confirmation; the drawer only forwards its close gesture. */
const editorRef = shallowRef<InstanceType<typeof AuthConnectionEditor> | null>(null)
const allowed = computed(() => !service.isLocal && auth.user?.role === 'admin')
const connections = shallowRef<AuthConnection[]>([])
const selected = shallowRef<AuthConnection | null>(null)
const drawerTitle = computed(() => {
  const target = selected.value
  if (!target) return ''
  const action = t(target.kind === 'password' ? 'authAdmin.viewPolicy' : 'authAdmin.edit')
  const name = target.configuration.display_name.trim() || target.id
  return `${action} · ${name}`
})
const creating = ref(false)
const loading = ref(false)
const error = ref('')
let sequence = 0
let alive = true
onScopeDispose(() => {
  alive = false
  sequence++
})

const sorted = computed(() =>
  [...connections.value].sort(
    (a, b) =>
      Number(a.archived) - Number(b.archived) ||
      (a.configuration.display_order ?? 0) - (b.configuration.display_order ?? 0) ||
      a.configuration.display_name.localeCompare(b.configuration.display_name),
  ),
)

function formatTime(value: string) {
  const date = new Date(value)
  return Number.isFinite(date.getTime()) ? date.toLocaleString() : value
}
function testSummary(connection: AuthConnection) {
  const validation = connection.validation
  if (!validation.applied_test_id)
    return `${t('authAdmin.lastTest')} · ${t('authAdmin.neverTested')}`
  const fresh = t(
    validation.fresh_supported ? 'authAdmin.freshSupported' : 'authAdmin.freshUnsupported',
  )
  return `${t('authAdmin.lastTest')} · ${fresh}${validation.tested_at ? ` · ${formatTime(validation.tested_at)}` : ''}`
}
function hasEffectiveUse(connection: AuthConnection) {
  return (
    connection.capabilities.can_login ||
    connection.capabilities.can_signup ||
    connection.capabilities.can_link
  )
}

async function load() {
  if (!allowed.value) return
  const context = captureSession()
  const current = ++sequence
  loading.value = true
  error.value = ''
  try {
    const items = await fetchAuthConnections()
    if (alive && isSessionCurrent(context) && current === sequence) connections.value = items
  } catch (cause) {
    if (!alive || !isSessionCurrent(context) || current !== sequence) return
    if (cause instanceof ApiError && [403, 404].includes(cause.status ?? 0)) {
      connections.value = []
      selected.value = null
    }
    error.value = cause instanceof Error ? cause.message : t('authAdmin.requestFailed')
  } finally {
    if (alive && isSessionCurrent(context) && current === sequence) loading.value = false
  }
}
function changed(record: AuthConnection) {
  connections.value = [...connections.value.filter((item) => item.id !== record.id), record]
}
/**
 * The drawer header close icon reports through `update:show`, never `close`; the drawer stays
 * controlled by `selected`, so a false value only asks the editor to run its close flow.
 */
function requestDrawerClose(show: boolean) {
  if (show) return
  editorRef.value?.requestClose()
}
function denied(detail: string) {
  connections.value = []
  selected.value = null
  creating.value = false
  error.value = detail
}
function created(record: AuthConnection) {
  changed(record)
  creating.value = false
  selected.value = record
  message.success(t('authAdmin.created'))
}
watch(
  [sessionGeneration, allowed],
  () => {
    sequence++
    connections.value = []
    selected.value = null
    creating.value = false
    error.value = ''
    loading.value = false
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
      <n-space v-if="allowed">
        <n-button :disabled="loading" data-testid="auth-connections-refresh" @click="load">{{
          t('authAdmin.refresh')
        }}</n-button>
        <n-button
          type="primary"
          :disabled="loading"
          data-testid="auth-connections-create"
          @click="creating = true"
        >
          {{ t('authAdmin.create') }}
        </n-button>
      </n-space>
    </header>
    <n-alert v-if="!allowed" type="info">{{ t('authAdmin.serverOnly') }}</n-alert>
    <template v-else>
      <n-alert v-if="error" type="error" role="alert">
        <div class="space-y-3">
          <p>{{ error }}</p>
          <n-button :disabled="loading" data-testid="auth-connections-retry" @click="load">{{
            t('authAdmin.retry')
          }}</n-button>
        </div>
      </n-alert>
      <n-spin :show="loading">
        <div class="min-h-24 space-y-4">
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
                    {{ connection.configuration.display_name || connection.id }}
                  </h2>
                  <n-tag size="small" :bordered="false">{{
                    t(`authAdmin.${connection.kind}`)
                  }}</n-tag>
                  <n-tag v-if="connection.archived" size="small" type="warning">{{
                    t('authAdmin.archived')
                  }}</n-tag>
                  <n-tag
                    v-else-if="connection.kind !== 'password'"
                    size="small"
                    :type="connection.state === 'enabled' ? 'success' : 'default'"
                    >{{ t(`authAdmin.${connection.state ?? 'configuring'}`) }}</n-tag
                  >
                  <n-tag
                    size="small"
                    :type="connection.configuration_complete ? 'success' : 'warning'"
                  >
                    {{
                      t(
                        connection.configuration_complete
                          ? 'authAdmin.complete'
                          : 'authAdmin.incomplete',
                      )
                    }}
                  </n-tag>
                </div>
                <p class="mt-2 break-words text-sm text-lf-text-muted">
                  {{ connection.configuration.description }}
                </p>
                <div class="mt-2 flex flex-wrap items-center gap-2">
                  <span class="text-xs text-lf-text-muted">{{ t('authAdmin.capabilities') }}</span>
                  <n-tag
                    v-if="connection.capabilities.can_login"
                    size="small"
                    :bordered="false"
                    type="success"
                    >{{ t('authAdmin.login') }}</n-tag
                  >
                  <n-tag
                    v-if="connection.capabilities.can_signup"
                    size="small"
                    :bordered="false"
                    type="success"
                    >{{ t('authAdmin.signup') }}</n-tag
                  >
                  <n-tag
                    v-if="connection.capabilities.can_link"
                    size="small"
                    :bordered="false"
                    type="success"
                    >{{ t('authAdmin.link') }}</n-tag
                  >
                  <n-tag v-if="!hasEffectiveUse(connection)" size="small" :bordered="false">{{
                    t('authAdmin.none')
                  }}</n-tag>
                </div>
                <p class="mt-2 text-xs text-lf-text-muted">{{ testSummary(connection) }}</p>
              </div>
              <n-button
                :data-testid="'auth-connection-open-' + connection.id"
                @click="selected = connection"
              >
                {{ t(connection.kind === 'password' ? 'authAdmin.viewPolicy' : 'authAdmin.edit') }}
              </n-button>
            </div>
          </n-card>
        </div>
      </n-spin>
      <n-modal
        :show="creating"
        preset="card"
        :title="t('authAdmin.create')"
        :closable="false"
        :mask-closable="false"
        :close-on-esc="false"
        style="width: min(560px, calc(100vw - 32px)); max-height: 90vh; overflow: auto"
      >
        <AuthConnectionCreate
          v-if="creating"
          @created="created"
          @denied="denied"
          @close="creating = false"
        />
      </n-modal>
      <n-drawer
        :show="Boolean(selected)"
        :width="900"
        :mask-closable="false"
        :close-on-esc="false"
        style="max-width: 100vw"
        @update:show="requestDrawerClose"
      >
        <n-drawer-content :title="drawerTitle" :body-content-style="{ padding: '0' }" closable>
          <AuthConnectionEditor
            v-if="selected"
            ref="editorRef"
            :key="`${sessionGeneration}:${selected.id}`"
            :connection="selected"
            @changed="changed"
            @denied="denied"
            @close="selected = null"
          />
        </n-drawer-content>
      </n-drawer>
    </template>
  </main>
</template>
