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
import { apiClient } from '@/api/client-core'
import { fetchAuthConnections, type AuthConnection } from '@/api/auth-connections'
import { fetchLocalAccount, type LocalAccount } from '@/api/local-account'
import { ApiError } from '@/api/utils'
import { buildRequestFailureError } from '@/api/utils'
import { extractErrorMessage } from '@/utils/errors'
import { captureSession, isSessionCurrent, sessionGeneration } from '@/api/session-context'
import { useAuthStore } from '@/stores/auth'
import { useServiceStore } from '@/stores/service'
import AuthConnectionCreate from '@/components/admin/authentication/AuthConnectionCreate.vue'
import AuthConnectionEditor from '@/components/admin/authentication/AuthConnectionEditor.vue'
import LocalAccountPolicyEditor from '@/components/admin/authentication/LocalAccountPolicyEditor.vue'

const { t } = useI18n()
const message = useMessage()
const auth = useAuthStore()
const service = useServiceStore()
/** The editor owns dirty-close confirmation; the drawer only forwards its close gesture. */
const editorRef = shallowRef<InstanceType<typeof AuthConnectionEditor> | null>(null)
const localEditorRef = shallowRef<InstanceType<typeof LocalAccountPolicyEditor> | null>(null)
const allowed = computed(() => !service.isLocal && auth.user?.role === 'admin')
const connections = shallowRef<AuthConnection[]>([])
const selected = shallowRef<AuthConnection | null>(null)
const localRecord = shallowRef<LocalAccount | null>(null)
const localEditing = ref(false)
const localLoading = ref(false)
const localError = ref('')
const registrationEnabled = shallowRef<boolean | null>(null)
const registrationLoading = ref(false)
const registrationError = ref('')
const drawerTitle = computed(() => {
  const target = selected.value
  if (!target) return ''
  const action = t('authAdmin.edit')
  const name = target.configuration.display_name.trim() || target.id
  return `${action} · ${name}`
})
const localDrawerTitle = computed(() => t('localAccount.editorTitle'))
const registrationHint = computed(() => {
  if (registrationLoading.value) return t('localAccount.registrationHintLoading')
  if (registrationEnabled.value === true) return t('localAccount.registrationHintEnabled')
  if (registrationEnabled.value === false) return t('localAccount.registrationHintClosed')
  return t('localAccount.registrationHintUnknown')
})
const creating = ref(false)
const loading = ref(false)
const error = ref('')
let sequence = 0
let localSequence = 0
let registrationSequence = 0
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
type LocalSummaryTone = 'enabled' | 'disabled' | 'optional' | 'required'
type LocalSummaryItem = {
  key: string
  label: string
  value: string
  tone: LocalSummaryTone
}
const localSwitchSummary = (key: string, label: string, enabled: boolean): LocalSummaryItem => ({
  key,
  label,
  value: t(enabled ? 'localAccount.enabled' : 'localAccount.disabled'),
  tone: enabled ? 'enabled' : 'disabled',
})
const localSummaryGroups = computed<Array<{ label: string; items: LocalSummaryItem[] }>>(() => {
  const record = localRecord.value
  if (!record) return []
  const policy = record.policy
  const capability = record.capabilities
  const emailRequired = capability.signup.email_requirement === 'required'
  return [
    {
      label: t('localAccount.loginGroup'),
      items: [
        localSwitchSummary(
          'password-login',
          t('localAccount.passwordLogin'),
          policy.password_login_enabled,
        ),
        localSwitchSummary(
          'username-login',
          t('localAccount.usernameLogin'),
          policy.username_login_enabled,
        ),
      ],
    },
    {
      label: t('localAccount.registrationGroup'),
      items: [
        localSwitchSummary(
          'enrollment',
          t('localAccount.enrollment'),
          policy.password_enrollment_enabled,
        ),
        localSwitchSummary('signup', t('localAccount.signup'), policy.signup.enabled),
        {
          key: 'email-requirement',
          label: t('localAccount.emailRequirementLabel'),
          value: t(emailRequired ? 'localAccount.required' : 'localAccount.optional'),
          tone: emailRequired ? 'required' : 'optional',
        },
      ],
    },
  ]
})
const localSummaryTagType = (tone: LocalSummaryTone) => {
  if (tone === 'enabled') return 'success'
  if (tone === 'required') return 'info'
  return 'default'
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
    error.value = extractErrorMessage(cause, t('authAdmin.requestFailed'))
  } finally {
    if (alive && isSessionCurrent(context) && current === sequence) loading.value = false
  }
}

async function loadLocalAccount() {
  if (!allowed.value) return
  const context = captureSession()
  const current = ++localSequence
  localLoading.value = true
  localError.value = ''
  try {
    const record = await fetchLocalAccount()
    if (alive && isSessionCurrent(context) && current === localSequence) localRecord.value = record
  } catch (cause) {
    if (!alive || !isSessionCurrent(context) || current !== localSequence) return
    if (cause instanceof ApiError && [403, 404].includes(cause.status ?? 0)) {
      localRecord.value = null
      localEditing.value = false
    }
    localError.value = extractErrorMessage(cause, t('localAccount.requestFailed'))
  } finally {
    if (alive && isSessionCurrent(context) && current === localSequence) localLoading.value = false
  }
}

async function loadRegistrationSummary() {
  if (!allowed.value) return
  const context = captureSession()
  const current = ++registrationSequence
  registrationLoading.value = true
  registrationError.value = ''
  try {
    const { data, error, response } = await apiClient.GET('/admin/settings')
    if (!alive || !isSessionCurrent(context) || current !== registrationSequence) return
    if (error || !data || !response.ok)
      throw buildRequestFailureError(t('localAccount.publicError'), error, response)
    if (typeof data.settings?.registration_enabled !== 'boolean')
      throw new ApiError(t('localAccount.publicError'), response.status)
    registrationEnabled.value = data.settings.registration_enabled
  } catch (cause) {
    if (!alive || !isSessionCurrent(context) || current !== registrationSequence) return
    registrationError.value = extractErrorMessage(cause, t('localAccount.requestFailed'))
  } finally {
    if (alive && isSessionCurrent(context) && current === registrationSequence)
      registrationLoading.value = false
  }
}

function localRecordChanged(record: LocalAccount) {
  localRecord.value = record
}

function localDenied(detail: string) {
  localRecord.value = null
  localEditing.value = false
  localError.value = detail
}

function requestLocalDrawerClose(show: boolean) {
  if (show) return
  localEditorRef.value?.requestClose()
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
    localRecord.value = null
    localEditing.value = false
    localError.value = ''
    localLoading.value = false
    registrationEnabled.value = null
    registrationError.value = ''
    registrationLoading.value = false
    if (allowed.value) {
      void load()
      void loadLocalAccount()
      void loadRegistrationSummary()
    }
  },
  { immediate: true, flush: 'sync' },
)
</script>

<template>
  <main class="mx-auto w-full max-w-6xl space-y-6 p-4 sm:p-6">
    <header class="flex flex-wrap items-start justify-between gap-4">
      <div>
        <h1 class="text-2xl font-semibold text-lf-text">{{ t('authentication.adminTitle') }}</h1>
        <p class="mt-2 max-w-3xl text-sm text-lf-text-muted">{{ t('localAccount.subtitle') }}</p>
      </div>
    </header>
    <n-alert v-if="!allowed" type="info">{{ t('authAdmin.serverOnly') }}</n-alert>
    <template v-else>
      <section class="space-y-4" data-testid="auth-resource-summaries">
        <n-card size="small" data-testid="auth-public-registration">
          <div class="grid gap-3 lg:grid-cols-[auto_minmax(0,1fr)_auto] lg:items-center lg:gap-6">
            <div class="flex min-w-0 flex-wrap items-center gap-2">
              <h2 class="text-base font-semibold">
                {{ t('localAccount.publicRegistration') }}
              </h2>
              <n-tag v-if="registrationLoading" class="shrink-0" size="small">{{
                t('localAccount.loading')
              }}</n-tag>
              <n-tag
                v-else-if="registrationEnabled === null"
                class="shrink-0"
                size="small"
                type="warning"
                data-testid="auth-public-registration-state"
              >
                {{ t('localAccount.registrationUnknown') }}
              </n-tag>
              <n-tag
                v-else
                class="shrink-0"
                size="small"
                :type="registrationEnabled ? 'success' : 'default'"
                data-testid="auth-public-registration-state"
              >
                {{
                  t(
                    registrationEnabled
                      ? 'localAccount.registrationEnabled'
                      : 'localAccount.registrationClosed',
                  )
                }}
              </n-tag>
            </div>
            <p
              class="min-w-0 text-sm text-lf-text-muted"
              data-testid="auth-public-registration-hint"
            >
              {{ registrationHint }}
            </p>
            <RouterLink
              class="text-sm font-medium text-lf-accent-sky lg:justify-self-end"
              to="/admin/settings"
            >
              {{ t('localAccount.systemSettings') }}
            </RouterLink>
          </div>
          <n-alert
            v-if="registrationError"
            type="error"
            class="mt-3"
            role="alert"
            data-testid="auth-public-registration-error"
          >
            <div class="space-y-2">
              <p>{{ registrationError }}</p>
              <n-button
                :disabled="registrationLoading"
                data-testid="auth-public-registration-retry"
                @click="loadRegistrationSummary"
              >
                {{ t('localAccount.retry') }}
              </n-button>
            </div>
          </n-alert>
        </n-card>
        <n-card size="small" data-testid="auth-local-account-card">
          <div class="flex flex-wrap items-start justify-between gap-3">
            <h2 class="text-base font-semibold">{{ t('localAccount.policyTitle') }}</h2>
            <n-button
              type="primary"
              :disabled="localLoading || !localRecord"
              data-testid="auth-local-account-edit"
              @click="localEditing = true"
            >
              {{ t('localAccount.edit') }}
            </n-button>
          </div>
          <n-alert
            v-if="localError"
            type="error"
            class="mt-3"
            role="alert"
            data-testid="auth-local-account-error"
          >
            <div class="space-y-2">
              <p>{{ localError }}</p>
              <n-button
                :disabled="localLoading"
                data-testid="auth-local-account-retry"
                @click="loadLocalAccount"
              >
                {{ t('localAccount.retry') }}
              </n-button>
            </div>
          </n-alert>
          <div
            v-if="localSummaryGroups.length"
            class="mt-4 grid gap-5 lg:grid-cols-2"
            data-testid="auth-local-account-summary"
          >
            <section v-for="group in localSummaryGroups" :key="group.label" class="space-y-2">
              <h3 class="text-xs font-medium tracking-wide text-lf-text-subtle">
                {{ group.label }}
              </h3>
              <dl class="grid gap-x-5 gap-y-2 sm:grid-cols-2">
                <div
                  v-for="item in group.items"
                  :key="item.key"
                  class="flex items-center justify-between gap-3 border-b border-lf-border/70 py-1.5"
                  :class="{ 'sm:col-span-2': item.key === 'email-requirement' }"
                  :data-testid="`auth-local-account-summary-${item.key}`"
                >
                  <dt class="min-w-0 text-sm text-lf-text-muted">{{ item.label }}</dt>
                  <dd class="shrink-0">
                    <n-tag size="small" :type="localSummaryTagType(item.tone)" :bordered="false">
                      {{ item.value }}
                    </n-tag>
                  </dd>
                </div>
              </dl>
            </section>
          </div>
        </n-card>
      </section>
      <section class="space-y-4" data-testid="auth-external-connections">
        <div class="flex flex-wrap items-start justify-between gap-4">
          <div class="min-w-0">
            <h2 class="text-base font-semibold">{{ t('authAdmin.connectionsTitle') }}</h2>
            <p class="mt-1 max-w-2xl text-sm text-lf-text-muted">
              {{ t('authAdmin.connectionsHint') }}
            </p>
          </div>
          <n-space>
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
        </div>
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
                    <h3 class="break-words text-base font-semibold">
                      {{ connection.configuration.display_name || connection.id }}
                    </h3>
                    <n-tag size="small" :bordered="false">{{
                      t(`authAdmin.${connection.kind}`)
                    }}</n-tag>
                    <n-tag v-if="connection.archived" size="small" type="warning">{{
                      t('authAdmin.archived')
                    }}</n-tag>
                    <n-tag
                      v-else-if="!connection.archived"
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
                    <span class="text-xs text-lf-text-muted">{{
                      t('authAdmin.capabilities')
                    }}</span>
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
                  {{ t('authAdmin.edit') }}
                </n-button>
              </div>
            </n-card>
          </div>
        </n-spin>
      </section>
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
      <n-drawer
        :show="localEditing"
        :width="760"
        :mask-closable="false"
        :close-on-esc="false"
        style="max-width: 100vw"
        @update:show="requestLocalDrawerClose"
      >
        <n-drawer-content :title="localDrawerTitle" :body-content-style="{ padding: '0' }" closable>
          <LocalAccountPolicyEditor
            v-if="localRecord && localEditing"
            ref="localEditorRef"
            :key="`local:${sessionGeneration}`"
            :record="localRecord"
            @changed="localRecordChanged"
            @denied="localDenied"
            @close="localEditing = false"
          />
        </n-drawer-content>
      </n-drawer>
    </template>
  </main>
</template>
