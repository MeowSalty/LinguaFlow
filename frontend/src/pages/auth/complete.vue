<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useMessage, type FormInst, type FormRules } from 'naive-ui'
import BlankLayout from '@/layouts/BlankLayout.vue'
import { useAuthStore } from '@/stores/auth'
import { useAuthCapabilities } from '@/composables/useAuthCapabilities'
import { captureSession, isSessionCurrent, onSessionChange } from '@/api/session-context'
import type { ApiSchemas } from '@/api/client-core'
import {
  readExternalFlow,
  exchangeExternalTicket,
  completeExternalConfirmation,
  cancelExternalFlow,
  clearExternalFlow,
  saveExternalResult,
  type ExternalFlow,
} from '@/auth/external-browser'
import { authenticationFailure } from '@/auth/errors'
import { isSignupRequirements } from '@/api/auth-contract'
import { safeReturnPath } from '@/auth/return-path'
import { relayConnectionTestTicket } from '@/auth/connection-test'
import { validateUsername, normalizeUsername } from '@/utils/username'
import { normalizeEmail, validateEmail } from '@/utils/email'
import { validateNewPassword } from '@/utils/password'
import { extractErrorMessage } from '@/utils/errors'

definePage({ meta: { public: true, layout: 'blank' } })
const { t } = useI18n()
const router = useRouter()
const auth = useAuthStore()
const { capabilities, reload: reloadCapabilities } = useAuthCapabilities()
const message = useMessage()
const formRef = shallowRef<FormInst | null>(null)
const flow = shallowRef<ExternalFlow | null>(null)
const confirmation = shallowRef<ApiSchemas['ExternalConfirmationResponse'] | null>(null)
const loading = shallowRef(true)
const testRelay = shallowRef(false)
const busy = shallowRef(false)
const error = shallowRef('')
const canConfirm = shallowRef(true)
const now = shallowRef(Date.now())
const form = reactive({
  username: '',
  display_name: '',
  email: '',
  wantsPassword: false,
  password: '',
  confirmPassword: '',
})
const signup = computed(() => confirmation.value?.kind === 'signup_confirmation')
const signupSnapshot = computed(() => (signup.value ? (confirmation.value?.signup ?? null) : null))
const confirmationInvalid = computed(
  () => signup.value && !isSignupRequirements(signupSnapshot.value),
)
const emailRequired = computed(() => signupSnapshot.value?.email_requirement === 'required')
const passwordRequirement = computed(() => signupSnapshot.value?.password_requirement ?? null)
const passwordActive = computed(
  () =>
    signup.value &&
    (passwordRequirement.value === 'required' ||
      (passwordRequirement.value === 'optional' && form.wantsPassword)),
)
const expired = computed(
  () =>
    !!confirmation.value &&
    Math.min(
      Date.parse(confirmation.value.expires_at),
      flow.value ? Date.parse(flow.value.expiresAt) : 0,
    ) <= now.value,
)
const returnTo = computed(() => (flow.value?.purpose === 'login' ? '/login' : '/settings/security'))
const returnLabel = computed(() =>
  flow.value && flow.value.purpose !== 'login'
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
      required: passwordActive.value,
      trigger: ['blur', 'input'],
      validator(_rule, value: string) {
        const issue = validateNewPassword(value || '')
        return issue ? new Error(t(`workbench.password.${issue}`)) : true
      },
    },
  ],
  confirmPassword: [
    {
      required: passwordActive.value,
      trigger: ['blur', 'input'],
      validator(_rule, value: string) {
        return value && value === form.password
          ? true
          : new Error(t('register.validation.passwordMismatch'))
      },
    },
  ],
  ...(signup.value
    ? {
        email: [
          {
            trigger: ['blur', 'input'] as ('blur' | 'input')[],
            validator(_rule: unknown, value: string) {
              const issue = validateEmail(value || '', emailRequired.value)
              return issue ? new Error(t(`accountInput.email.${issue}`)) : true
            },
          },
        ],
      }
    : {}),
}))
let alive = true
const interval = setInterval(() => {
  now.value = Date.now()
}, 1000)
const stop = onSessionChange(() => {
  if (flow.value && !readExternalFlow()) {
    confirmation.value = null
    form.wantsPassword = false
    form.email = ''
    form.password = ''
    form.confirmPassword = ''
    error.value = t('authentication.flowInvalid')
  }
})
onBeforeUnmount(() => {
  alive = false
  stop()
  clearInterval(interval)
  form.wantsPassword = false
  form.email = ''
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
    flow.value = null
    clearExternalFlow()
    auth.acceptExternalSession(result.session, context)
    await router.replace(destination)
  } else if (result.kind === 'linked') {
    flow.value = null
    clearExternalFlow()
    message.success(t('authentication.linked'))
    await router.replace('/settings/security')
  } else if (result.kind === 'reauthentication_proof') {
    flow.value = null
    saveExternalResult(result)
    await router.replace('/settings/security')
  } else if (result.kind === 'test_result') {
    throw new Error(t('authentication.flowInvalid'))
  } else {
    confirmation.value = result
    form.wantsPassword = false
    form.email = result.email ?? ''
    form.username = result.username_candidate ?? ''
    form.password = ''
    form.confirmPassword = ''
    if (result.kind === 'signup_confirmation' && !result.signup) void reloadCapabilities()
  }
}
onMounted(async () => {
  const context = captureSession()
  const ticketWindow = window as Window & { __linguaflowAuthTicket?: string }
  const ticket = ticketWindow.__linguaflowAuthTicket
  delete ticketWindow.__linguaflowAuthTicket
  if (relayConnectionTestTicket(ticket)) {
    testRelay.value = true
    loading.value = false
    return
  }
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
  if (busy.value || expired.value || !canConfirm.value || confirmationInvalid.value) return
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
    const email = normalizeEmail(form.email)
    const result = await completeExternalConfirmation(
      signup.value
        ? {
            username: normalizeUsername(form.username),
            display_name: form.display_name.trim() || undefined,
            ...(email ? { email } : {}),
            initial_password: passwordActive.value ? form.password : undefined,
          }
        : {},
    )
    if (alive && isSessionCurrent(context)) await finish(result)
  } catch (cause) {
    if (alive && isSessionCurrent(context)) {
      const failure = authenticationFailure(cause, 'external-complete')
      canConfirm.value = readExternalFlow()?.stage === 'confirming'
      if (failure === 'rule-denied' || failure === 'forbidden') {
        await reloadCapabilities()
        if (!alive || !isSessionCurrent(context)) return
      }
      const currentConnection = capabilities.value?.connections.find(
        (entry) => entry.connection_id === confirmation.value?.profile.connection_id,
      )
      error.value =
        failure === 'forbidden' && currentConnection?.can_signup === false
          ? t('authentication.signupClosed')
          : confirmationError(cause, failure)
    }
  } finally {
    busy.value = false
    form.password = ''
    form.confirmPassword = ''
  }
}

function confirmationError(
  cause: unknown,
  failure: ReturnType<typeof authenticationFailure>,
): string {
  if (failure === 'username-conflict') return t('authentication.usernameConflict')
  if (failure === 'rule-denied') return t('authentication.ruleDenied')
  if (failure === 'flow-invalid') return t('authentication.flowInvalid')
  if (failure === 'result-unknown') return t('authentication.resultUnknown')
  return canConfirm.value
    ? extractErrorMessage(cause, t('authentication.failed'))
    : t('authentication.failed')
}

watch(
  () => form.wantsPassword,
  (value) => {
    if (!value) {
      form.password = ''
      form.confirmPassword = ''
    }
  },
)
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
function closeTestWindow() {
  window.close()
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
      <NAlert v-if="testRelay" type="info">{{ t('authAdmin.testRelayHint') }}</NAlert>
      <NAlert v-if="expired" type="warning" :show-icon="false">{{
        t('authentication.flowExpired')
      }}</NAlert>
      <NAlert v-if="confirmationInvalid" type="error" :show-icon="false">{{
        t('authentication.signupRequirementsInvalid')
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
          :disabled="busy || expired || !canConfirm || confirmationInvalid"
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
            <NFormItem
              :label="
                t(emailRequired ? 'authentication.requiredEmail' : 'authentication.optionalEmail')
              "
              path="email"
            >
              <NInput
                v-model:value="form.email"
                :placeholder="t('register.form.emailPlaceholder')"
                clearable
                :input-props="{ autocomplete: 'email', type: 'email' }"
              />
            </NFormItem>
            <p class="mb-4 text-xs leading-5 text-lf-text-muted">
              {{
                t(emailRequired ? 'authentication.requiredEmailHint' : 'authentication.emailHint')
              }}
            </p>
            <template v-if="passwordRequirement === 'optional'">
              <NFormItem :label="t('authentication.externalOptionalPassword')" path="wantsPassword">
                <NSwitch v-model:value="form.wantsPassword" />
              </NFormItem>
              <p class="mb-4 text-xs leading-5 text-lf-text-muted">
                {{ t('authentication.externalOptionalPasswordHint') }}
              </p>
            </template>
            <template v-if="passwordActive">
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
            :disabled="expired || !canConfirm || confirmationInvalid"
            >{{
              signup ? t('authentication.confirmSignup') : t('authentication.confirmLink')
            }}</NButton
          >
        </NForm>
      </template>
      <NButton v-if="testRelay" block @click="closeTestWindow">{{
        t('authAdmin.closeTestWindow')
      }}</NButton>
      <NButton v-else-if="!loading" block :disabled="busy" @click="cancel">{{
        confirmation ? t('authentication.cancel') : returnLabel
      }}</NButton>
    </div>
  </BlankLayout>
</template>
