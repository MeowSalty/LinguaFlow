<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useMessage, type FormInst, type FormItemRule, type FormRules } from 'naive-ui'

import BlankLayout from '@/layouts/BlankLayout.vue'
import { useAuthStore } from '@/stores/auth'
import {
  captureSession,
  isSessionCurrent,
  onSessionChange,
  StaleSessionError,
} from '@/api/session-context'
import { authenticationFailure } from '@/auth/errors'
import { validateNewPassword } from '@/utils/password'
import { validateUsername, normalizeUsername } from '@/utils/username'
import { normalizeEmail, validateEmail } from '@/utils/email'
import { useAuthCapabilities } from '@/composables/useAuthCapabilities'
import { safeReturnPath } from '@/auth/return-path'
import ExternalLoginOptions from '@/components/auth/ExternalLoginOptions.vue'

definePage({
  meta: {
    public: true,
    layout: 'blank',
  },
})

const router = useRouter()
const route = useRoute()
const auth = useAuthStore()
const message = useMessage()
const { t } = useI18n()
const {
  capabilities,
  signupConnections,
  canSignup,
  loading,
  error: capabilitiesError,
  reload,
} = useAuthCapabilities()
const externalBusy = ref(false)
const returnTo = computed(() => safeReturnPath(route.query.redirect))
const localSignup = computed(() => capabilities.value?.local_account.can_signup === true)
const emailRequired = computed(
  () => capabilities.value?.local_account.signup.email_requirement === 'required',
)

const formRef = ref<FormInst | null>(null)
const submitting = ref(false)
let alive = true
let submissionGeneration = 0
const formValue = reactive({
  username: '',
  email: '',
  display_name: '',
  password: '',
  confirm_password: '',
})
const clearSensitiveInputs = () => {
  formValue.email = ''
  formValue.password = ''
  formValue.confirm_password = ''
}
const stopSession = onSessionChange(() => {
  submitting.value = false
  clearSensitiveInputs()
})
onBeforeUnmount(() => {
  alive = false
  submissionGeneration++
  clearSensitiveInputs()
  stopSession()
})

const validatePasswordConfirm = (_rule: FormItemRule, value: string): boolean | Error => {
  if (value && value !== formValue.password) {
    return new Error(t('register.validation.passwordMismatch'))
  }
  return true
}

const rules = computed<FormRules>(() => ({
  username: [
    {
      required: true,
      trigger: ['blur', 'input'],
      message: t('register.validation.usernameRequired'),
    },
    {
      trigger: ['blur', 'input'],
      validator(_rule, value: string) {
        const issue = validateUsername(value || '')
        return issue ? new Error(t(`accountInput.username.${issue}`)) : true
      },
    },
  ],
  email: [
    {
      trigger: ['blur', 'input'],
      validator(_rule, value: string) {
        const issue = validateEmail(value || '', emailRequired.value)
        return issue ? new Error(t(`accountInput.email.${issue}`)) : true
      },
    },
  ],
  password: [
    {
      required: true,
      trigger: ['blur', 'input'],
      message: t('register.validation.passwordRequired'),
    },
    {
      trigger: ['blur', 'input'],
      validator(_rule, value: string) {
        const issue = validateNewPassword(value || '')
        return issue ? new Error(t(`workbench.password.${issue}`)) : true
      },
    },
  ],
  confirm_password: [
    {
      required: true,
      trigger: ['blur', 'input'],
      message: t('register.validation.confirmPasswordRequired'),
    },
    { trigger: ['blur', 'input'], validator: validatePasswordConfirm },
  ],
}))

const onSubmit = async () => {
  if (submitting.value || externalBusy.value || !localSignup.value) return
  const generation = ++submissionGeneration
  let context = captureSession()
  const current = () => alive && generation === submissionGeneration && isSessionCurrent(context)
  submitting.value = true
  try {
    await formRef.value?.validate()
  } catch {
    if (current()) submitting.value = false
    return
  }

  if (!current()) return
  try {
    const registration = auth.register({
      username: normalizeUsername(formValue.username),
      ...(normalizeEmail(formValue.email) ? { email: normalizeEmail(formValue.email) } : {}),
      display_name: formValue.display_name.trim() || undefined,
      password: formValue.password,
    })
    // Register starts a fresh anonymous session synchronously before awaiting the request.
    context = captureSession()
    submitting.value = true
    context = await registration
    if (!current()) return
    message.success(t('register.messages.success'))
    await router.push(returnTo.value)
  } catch (error) {
    if (
      !current() ||
      error instanceof StaleSessionError ||
      (error instanceof DOMException && error.name === 'AbortError')
    )
      return
    const failure = authenticationFailure(error, 'register')
    if (failure === 'forbidden') await reload()
    message.error(
      failure === 'unavailable'
        ? t('configurationSettings.registrationUnavailable')
        : failure === 'forbidden'
          ? t('authentication.signupClosed')
          : failure === 'username-conflict'
            ? t('authentication.usernameConflict')
            : t('register.messages.failed'),
    )
  } finally {
    clearSensitiveInputs()
    if (current()) submitting.value = false
  }
}
</script>

<template>
  <BlankLayout :title="t('register.title')" :subtitle="t('register.subtitle')">
    <div class="lf-panel p-6">
      <NSkeleton v-if="loading" text :repeat="5" />
      <NAlert v-else-if="capabilitiesError" type="error" :show-icon="false">
        {{ capabilitiesError }}
        <NButton class="mt-3" @click="reload">{{ t('authentication.retry') }}</NButton>
      </NAlert>
      <NAlert v-else-if="!canSignup" type="info" :show-icon="false">{{
        t('authentication.signupClosed')
      }}</NAlert>
      <NForm
        v-if="!loading && localSignup"
        ref="formRef"
        :model="formValue"
        :rules="rules"
        label-placement="top"
        require-mark-placement="right-hanging"
        @submit.prevent="onSubmit"
      >
        <NFormItem :label="t('register.form.username')" path="username">
          <NInput
            v-model:value="formValue.username"
            :placeholder="t('register.form.usernamePlaceholder')"
            clearable
            :input-props="{ autocomplete: 'username' }"
          />
        </NFormItem>
        <p class="mb-4 text-xs leading-5 text-lf-text-muted">
          {{ t('authentication.usernameHint') }}
        </p>
        <NFormItem
          :label="
            t(emailRequired ? 'authentication.requiredEmail' : 'authentication.optionalEmail')
          "
          path="email"
        >
          <NInput
            v-model:value="formValue.email"
            :placeholder="t('register.form.emailPlaceholder')"
            clearable
            :input-props="{ autocomplete: 'email', type: 'email' }"
          />
        </NFormItem>
        <p class="mb-4 text-xs leading-5 text-lf-text-muted">
          {{ t(emailRequired ? 'authentication.requiredEmailHint' : 'authentication.emailHint') }}
        </p>

        <NFormItem :label="t('register.form.displayName')" path="display_name">
          <NInput
            v-model:value="formValue.display_name"
            :placeholder="t('register.form.displayNamePlaceholder')"
            clearable
          />
        </NFormItem>

        <NFormItem :label="t('register.form.password')" path="password">
          <NInput
            v-model:value="formValue.password"
            type="password"
            :placeholder="t('register.form.passwordPlaceholder')"
            show-password-on="click"
            :input-props="{ autocomplete: 'new-password' }"
          />
        </NFormItem>

        <NFormItem :label="t('register.form.confirmPassword')" path="confirm_password">
          <NInput
            v-model:value="formValue.confirm_password"
            type="password"
            :placeholder="t('register.form.confirmPasswordPlaceholder')"
            show-password-on="click"
            :input-props="{ autocomplete: 'new-password' }"
          />
        </NFormItem>

        <NButton
          attr-type="submit"
          type="primary"
          size="large"
          block
          :loading="submitting"
          :disabled="externalBusy"
        >
          {{ t('register.form.submit') }}
        </NButton>
      </NForm>
      <NDivider v-if="localSignup && signupConnections.length">{{
        t('authentication.externalDivider')
      }}</NDivider>
      <ExternalLoginOptions
        v-if="signupConnections.length"
        v-model:busy="externalBusy"
        :connections="signupConnections"
        :return-to="returnTo"
        :disabled="submitting"
      />
    </div>

    <div class="mt-6 text-center text-xs text-lf-text-muted">
      {{ t('register.links.hasAccount') }}
      <RouterLink
        :to="{ path: '/login', query: { redirect: returnTo } }"
        class="text-brand-500 no-underline hover:underline"
      >
        {{ t('register.links.login') }}
      </RouterLink>
    </div>
  </BlankLayout>
</template>
