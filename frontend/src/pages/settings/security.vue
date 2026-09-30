<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useMessage, type FormInst, type FormRules } from 'naive-ui'
import { captureSession, isSessionCurrent, sessionGeneration } from '@/api/session-context'
import { useAuthStore } from '@/stores/auth'
import { useServiceStore } from '@/stores/service'
import { extractErrorMessage } from '@/utils/errors'
import { validateNewPassword } from '@/utils/password'

const auth = useAuthStore()
const service = useServiceStore()
const router = useRouter()
const message = useMessage()
const { t } = useI18n()
const formRef = ref<FormInst | null>(null)
const currentError = ref<string | null>(null)
const error = ref<string | null>(null)
const loggingOut = ref(false)
const formValue = reactive({ current_password: '', new_password: '', confirm_password: '' })
const rules = computed<FormRules>(() => ({
  current_password: [
    {
      required: true,
      trigger: ['blur', 'input'],
      message: t('account.security.validation.currentPasswordRequired'),
    },
  ],
  new_password: [
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
      trigger: ['blur', 'input'],
      validator(_rule, value: string) {
        if (!value) return new Error(t('account.security.validation.confirmPasswordRequired'))
        return (
          value === formValue.new_password ||
          new Error(t('account.security.validation.passwordMismatch'))
        )
      },
    },
  ],
}))
const reset = (): void => {
  Object.assign(formValue, { current_password: '', new_password: '', confirm_password: '' })
  currentError.value = null
  error.value = null
}
watch(sessionGeneration, reset, { flush: 'sync' })
watch(
  () => formValue.current_password,
  () => {
    currentError.value = null
  },
)
const onSubmit = async (): Promise<void> => {
  if (service.isLocal || auth.passwordChanging) return
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  const snapshot = captureSession()
  error.value = null
  currentError.value = null
  try {
    await auth.changePassword({
      current_password: formValue.current_password,
      new_password: formValue.new_password,
    })
    if (!isSessionCurrent(snapshot)) return
    reset()
    formRef.value?.restoreValidation()
    message.success(t('account.security.messages.changeSuccess'))
  } catch (cause) {
    if (!isSessionCurrent(snapshot)) return
    const problem =
      cause && typeof cause === 'object' && 'problem' in cause
        ? (cause.problem as { type?: string; title?: string } | undefined)
        : undefined
    if (
      problem?.type?.endsWith('current-password-mismatch') ||
      problem?.title === 'current_password_mismatch'
    )
      currentError.value = t('workbench.password.currentMismatch')
    else error.value = extractErrorMessage(cause, t('account.security.messages.changeFailed'))
  }
}
const logout = async (): Promise<void> => {
  if (loggingOut.value) return
  loggingOut.value = true
  try {
    await auth.logout()
    message.success(t('workbench.settings.logoutSuccess'))
  } catch {
    message.warning(t('workbench.settings.logoutFailed'))
  } finally {
    loggingOut.value = false
    await router.replace('/login')
  }
}
</script>
<template>
  <section class="lf-panel p-5 sm:p-6">
    <h2 class="mb-2 text-lg font-semibold text-lf-text-strong">
      {{ t('workbench.settings.security') }}
    </h2>
    <p class="mb-5 text-sm text-lf-text-muted">{{ t('account.security.description') }}</p>
    <template v-if="service.isLocal">
      <NAlert type="info" class="mb-5">{{ t('workbench.settings.localSecurity') }}</NAlert>
      <NButton @click="router.push('/service?force=1')">{{
        t('workbench.settings.service')
      }}</NButton>
    </template>
    <template v-else>
      <NAlert v-if="error" type="error" class="mb-5">{{ error }}</NAlert>
      <NForm
        ref="formRef"
        :model="formValue"
        :rules="rules"
        label-placement="top"
        @submit.prevent="onSubmit"
      >
        <NFormItem
          :label="t('account.security.form.currentPassword')"
          path="current_password"
          :validation-status="currentError ? 'error' : undefined"
          :feedback="currentError ?? undefined"
        >
          <NInput
            v-model:value="formValue.current_password"
            type="password"
            show-password-on="click"
            :input-props="{ autocomplete: 'current-password' }"
          />
        </NFormItem>
        <NFormItem :label="t('account.security.form.newPassword')" path="new_password">
          <NInput
            v-model:value="formValue.new_password"
            type="password"
            show-password-on="click"
            :input-props="{ autocomplete: 'new-password' }"
          />
        </NFormItem>
        <p class="mb-5 text-xs text-lf-text-muted">{{ t('workbench.password.hint') }}</p>
        <NFormItem :label="t('account.security.form.confirmPassword')" path="confirm_password">
          <NInput
            v-model:value="formValue.confirm_password"
            type="password"
            show-password-on="click"
            :input-props="{ autocomplete: 'new-password' }"
          />
        </NFormItem>
        <NButton attr-type="submit" type="primary" :loading="auth.passwordChanging">{{
          t('account.security.form.submit')
        }}</NButton>
      </NForm>
      <div
        class="mt-7 flex flex-wrap items-center justify-between gap-4 border-t border-lf-border-soft pt-5"
      >
        <p class="text-sm text-lf-text-muted">{{ t('workbench.settings.logoutHint') }}</p>
        <NButton :loading="loggingOut" @click="logout">{{
          t('workbench.settings.logout')
        }}</NButton>
      </div>
    </template>
  </section>
</template>
