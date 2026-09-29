<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useMessage, type FormInst, type FormItemRule, type FormRules } from 'naive-ui'

import PageHeader from '@/components/common/PageHeader.vue'
import { useAuthStore } from '@/stores/auth'
import { useServiceStore } from '@/stores/service'
import { extractErrorMessage } from '@/utils/errors'

const auth = useAuthStore()
const service = useServiceStore()
const message = useMessage()
const { t } = useI18n()

const formRef = ref<FormInst | null>(null)
const formValue = reactive({
  current_password: '',
  new_password: '',
  confirm_password: '',
})

const validatePasswordConfirm = (_rule: FormItemRule, value: string): boolean | Error => {
  if (value && value !== formValue.new_password) {
    return new Error(t('account.security.validation.passwordMismatch'))
  }
  return true
}

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
      required: true,
      trigger: ['blur', 'input'],
      message: t('account.security.validation.newPasswordRequired'),
    },
    {
      min: 8,
      trigger: ['blur', 'input'],
      message: t('account.security.validation.passwordMinLength'),
    },
  ],
  confirm_password: [
    {
      required: true,
      trigger: ['blur', 'input'],
      message: t('account.security.validation.confirmPasswordRequired'),
    },
    { trigger: ['blur', 'input'], validator: validatePasswordConfirm },
  ],
}))

const resetForm = (): void => {
  formValue.current_password = ''
  formValue.new_password = ''
  formValue.confirm_password = ''
}

const onSubmit = async (): Promise<void> => {
  if (service.isLocal) {
    message.warning(t('account.security.messages.localModeUnavailable'))
    return
  }

  try {
    await formRef.value?.validate()
  } catch {
    return
  }

  try {
    await auth.changePassword({
      current_password: formValue.current_password,
      new_password: formValue.new_password,
    })
    resetForm()
    message.success(t('account.security.messages.changeSuccess'))
  } catch (error) {
    console.error(error)
    message.error(extractErrorMessage(error, t('account.security.messages.changeFailed')))
  }
}
</script>

<template>
  <div class="lf-page lf-content-narrow">
    <PageHeader
      :title="t('account.security.title')"
      :subtitle="t('account.security.description')"
    />

    <div class="lf-panel p-6">
      <NAlert v-if="service.isLocal" type="info" :show-icon="false" class="mb-5">
        {{ t('account.security.messages.localModeUnavailable') }}
      </NAlert>

      <NForm
        ref="formRef"
        :model="formValue"
        :rules="rules"
        label-placement="top"
        require-mark-placement="right-hanging"
        @submit.prevent="onSubmit"
      >
        <NFormItem :label="t('account.security.form.currentPassword')" path="current_password">
          <NInput
            v-model:value="formValue.current_password"
            type="password"
            :placeholder="t('account.security.form.currentPasswordPlaceholder')"
            show-password-on="click"
            :input-props="{ autocomplete: 'current-password' }"
          />
        </NFormItem>

        <NFormItem :label="t('account.security.form.newPassword')" path="new_password">
          <NInput
            v-model:value="formValue.new_password"
            type="password"
            :placeholder="t('account.security.form.newPasswordPlaceholder')"
            show-password-on="click"
            :input-props="{ autocomplete: 'new-password' }"
          />
        </NFormItem>

        <NFormItem :label="t('account.security.form.confirmPassword')" path="confirm_password">
          <NInput
            v-model:value="formValue.confirm_password"
            type="password"
            :placeholder="t('account.security.form.confirmPasswordPlaceholder')"
            show-password-on="click"
            :input-props="{ autocomplete: 'new-password' }"
          />
        </NFormItem>

        <NButton
          attr-type="submit"
          type="primary"
          :loading="auth.passwordChanging"
          :disabled="service.isLocal"
        >
          {{ t('account.security.form.submit') }}
        </NButton>
      </NForm>
    </div>
  </div>
</template>
