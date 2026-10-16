<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useMessage, type FormInst, type FormRules } from 'naive-ui'
import BlankLayout from '@/layouts/BlankLayout.vue'
import { useAuthStore } from '@/stores/auth'
import { captureSession, isSessionCurrent, onSessionChange } from '@/api/session-context'
import type { ApiSchemas } from '@/api/client-core'
import { ApiError } from '@/api/utils'
import {
  readExternalFlow,
  exchangeExternalTicket,
  completeExternalConfirmation,
  cancelExternalFlow,
  clearExternalFlow,
  saveExternalResult,
  type ExternalFlow,
} from '@/auth/external-browser'
import { safeReturnPath } from '@/auth/return-path'
import { validateUsername, normalizeUsername } from '@/utils/username'
import { validateNewPassword } from '@/utils/password'
import { extractErrorMessage } from '@/utils/errors'

definePage({ meta: { public: true, layout: 'blank' } })
const { t } = useI18n()
const router = useRouter()
const auth = useAuthStore()
const message = useMessage()
const formRef = shallowRef<FormInst | null>(null)
const flow = shallowRef<ExternalFlow | null>(null)
const confirmation = shallowRef<ApiSchemas['ExternalConfirmationResponse'] | null>(null)
const loading = shallowRef(true)
const busy = shallowRef(false)
const error = shallowRef('')
const canConfirm = shallowRef(true)
const now = shallowRef(Date.now())
const form = reactive({ username: '', display_name: '', password: '', confirmPassword: '' })
const signup = computed(() => confirmation.value?.kind === 'signup_confirmation')
const requiresPassword = computed(
  () => signup.value && confirmation.value?.signup_requirements.includes('local_password'),
)
const expired = computed(
  () =>
    !!confirmation.value &&
    Math.min(
      Date.parse(confirmation.value.expires_at),
      flow.value ? Date.parse(flow.value.expiresAt) : 0,
    ) <= now.value,
)
const returnTo = computed(() =>
  flow.value?.purpose === 'login'
    ? '/login'
    : flow.value?.purpose === 'test'
      ? '/admin/authentication'
      : '/settings/security',
)
const returnLabel = computed(() =>
  flow.value?.purpose === 'test'
    ? t('authentication.returnAdmin')
    : flow.value && flow.value.purpose !== 'login'
      ? t('authentication.returnSecurity')
      : t('authentication.restart'),
)
const rules = computed<FormRules>(() => ({
  username: [
    {
      required: true,
      trigger: ['blur', 'input'],
      validator(_rule, value: string) {
        const issue = validateUsername(value || '')
        return issue ? new Error(t(`accountInput.username.${issue}`)) : true
      },
    },
  ],
  password: [
    {
      required: !!requiresPassword.value,
      trigger: ['blur', 'input'],
      validator(_rule, value: string) {
        const issue = validateNewPassword(value || '')
        return issue ? new Error(t(`workbench.password.${issue}`)) : true
      },
    },
  ],
  confirmPassword: [
    {
      required: !!requiresPassword.value,
      trigger: ['blur', 'input'],
      validator(_rule, value: string) {
        return value && value === form.password
          ? true
          : new Error(t('register.validation.passwordMismatch'))
      },
    },
  ],
}))
let alive = true
const interval = setInterval(() => {
  now.value = Date.now()
}, 1000)
const stop = onSessionChange(() => {
  if (flow.value && !readExternalFlow()) {
    confirmation.value = null
    form.password = ''
    form.confirmPassword = ''
    error.value = t('authentication.flowInvalid')
  }
})
onBeforeUnmount(() => {
  alive = false
  stop()
  clearInterval(interval)
  form.password = ''
  form.confirmPassword = ''
})

async function finish(
  result: ApiSchemas['ExternalExchangeResponse'] | ApiSchemas['ExternalCompleteResponse'],
) {
  if (!flow.value) throw new Error(t('authentication.flowInvalid'))
  const destination = safeReturnPath(flow.value.returnTo)
  if (result.kind === 'session') {
    const context = captureSession()
    clearExternalFlow()
    auth.acceptExternalSession(result.session, context)
    await router.replace(destination)
  } else if (result.kind === 'linked') {
    clearExternalFlow()
    message.success(t('authentication.linked'))
    await router.replace('/settings/security')
  } else if (result.kind === 'reauthentication_proof' || result.kind === 'test_result') {
    saveExternalResult(result)
    await router.replace(
      result.kind === 'test_result' ? '/admin/authentication' : '/settings/security',
    )
  } else {
    confirmation.value = result
  }
}
onMounted(async () => {
  const context = captureSession()
  const ticketWindow = window as Window & { __linguaflowAuthTicket?: string }
  const ticket = ticketWindow.__linguaflowAuthTicket
  delete ticketWindow.__linguaflowAuthTicket
  flow.value = readExternalFlow()
  try {
    const result = await exchangeExternalTicket(ticket)
    if (alive && isSessionCurrent(context)) await finish(result)
  } catch (cause) {
    if (alive && isSessionCurrent(context))
      error.value = extractErrorMessage(cause, t('authentication.failed'))
  } finally {
    if (alive) loading.value = false
  }
})
async function confirm() {
  if (busy.value || expired.value || !canConfirm.value) return
  busy.value = true
  const context = captureSession()
  try {
    if (signup.value) {
      try {
        await formRef.value?.validate()
      } catch {
        return
      }
    }
    if (!isSessionCurrent(context) || !alive) return
    error.value = ''
    const result = await completeExternalConfirmation(
      signup.value
        ? {
            username: normalizeUsername(form.username),
            display_name: form.display_name.trim() || undefined,
            initial_password: requiresPassword.value ? form.password : undefined,
          }
        : {},
    )
    if (alive && isSessionCurrent(context)) await finish(result)
  } catch (cause) {
    if (alive && isSessionCurrent(context)) {
      canConfirm.value = readExternalFlow()?.stage === 'confirming'
      error.value =
        canConfirm.value || cause instanceof ApiError
          ? extractErrorMessage(cause, t('authentication.failed'))
          : t('authentication.resultUnknown')
    }
  } finally {
    busy.value = false
    form.password = ''
    form.confirmPassword = ''
  }
}
async function cancel() {
  if (busy.value) return
  busy.value = true
  const context = captureSession()
  try {
    if (readExternalFlow()) await cancelExternalFlow()
  } catch {
    if (isSessionCurrent(context)) message.warning(t('authentication.cancelFailed'))
  } finally {
    busy.value = false
    if (alive && isSessionCurrent(context)) {
      clearExternalFlow()
      await router.replace(returnTo.value)
    }
  }
}
</script>

<template>
  <BlankLayout
    :title="
      confirmation
        ? signup
          ? t('authentication.signupTitle')
          : t('authentication.linkTitle')
        : t('authentication.callbackTitle')
    "
  >
    <div class="lf-panel space-y-5 p-6">
      <div v-if="loading" class="flex items-center gap-3">
        <NSpin size="small" /><span>{{ t('authentication.callbackLoading') }}</span>
      </div>
      <NAlert v-if="error" type="error" :show-icon="false" role="alert">{{ error }}</NAlert>
      <NAlert v-if="expired" type="warning" :show-icon="false">{{
        t('authentication.flowExpired')
      }}</NAlert>
      <template v-if="confirmation">
        <p class="text-sm leading-6 text-lf-text-muted">
          {{ signup ? t('authentication.signupHint') : t('authentication.linkHint') }}
        </p>
        <dl class="rounded-lg bg-lf-surface-muted p-4 text-sm">
          <dt class="text-xs text-lf-text-muted">{{ t('authentication.connection') }}</dt>
          <dd class="mt-1 break-words font-medium">{{ confirmation.profile.connection_name }}</dd>
          <dt class="mt-3 text-xs text-lf-text-muted">
            {{ t('authentication.externalIdentity') }}
          </dt>
          <dd class="mt-1 break-words">{{ confirmation.profile.external_display }}</dd>
        </dl>
        <NForm
          ref="formRef"
          :model="form"
          :rules="rules"
          :disabled="busy || expired || !canConfirm"
          @submit.prevent="confirm"
        >
          <template v-if="signup">
            <NFormItem :label="t('register.form.username')" path="username"
              ><NInput v-model:value="form.username" :input-props="{ autocomplete: 'username' }"
            /></NFormItem>
            <p class="mb-4 text-xs leading-5 text-lf-text-muted">
              {{ t('authentication.usernameHint') }}
            </p>
            <NFormItem :label="t('register.form.displayName')" path="display_name"
              ><NInput v-model:value="form.display_name"
            /></NFormItem>
            <template v-if="requiresPassword">
              <NAlert class="mb-4" type="info" :show-icon="false">{{
                t('authentication.initialPasswordHint')
              }}</NAlert>
              <NFormItem :label="t('register.form.password')" path="password"
                ><NInput
                  v-model:value="form.password"
                  type="password"
                  show-password-on="click"
                  :input-props="{ autocomplete: 'new-password' }"
              /></NFormItem>
              <NFormItem :label="t('register.form.confirmPassword')" path="confirmPassword"
                ><NInput
                  v-model:value="form.confirmPassword"
                  type="password"
                  show-password-on="click"
                  :input-props="{ autocomplete: 'new-password' }"
              /></NFormItem>
            </template>
          </template>
          <NButton
            type="primary"
            attr-type="submit"
            block
            :loading="busy"
            :disabled="expired || !canConfirm"
            >{{
              signup ? t('authentication.confirmSignup') : t('authentication.confirmLink')
            }}</NButton
          >
        </NForm>
      </template>
      <NButton v-if="!loading" block :disabled="busy" @click="cancel">{{
        confirmation ? t('authentication.cancel') : returnLabel
      }}</NButton>
    </div>
  </BlankLayout>
</template>
