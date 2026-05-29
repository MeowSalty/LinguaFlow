<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useMessage, type FormInst, type FormItemRule, type FormRules } from 'naive-ui'

import BlankLayout from '@/layouts/BlankLayout.vue'
import { useAuthStore } from '@/stores/auth'

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

const formRef = ref<FormInst | null>(null)
const submitting = ref(false)

const formValue = reactive({
  username: '',
  email: '',
  display_name: '',
  password: '',
  confirm_password: '',
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
      min: 3,
      max: 32,
      trigger: ['blur', 'input'],
      message: t('register.validation.usernameLength'),
    },
  ],
  email: [
    { required: true, trigger: ['blur', 'input'], message: t('register.validation.emailRequired') },
    {
      trigger: ['blur', 'input'],
      validator(_rule, value: string) {
        if (!value) return true
        const ok = /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value.trim())
        return ok ? true : new Error(t('register.validation.emailInvalid'))
      },
    },
  ],
  password: [
    {
      required: true,
      trigger: ['blur', 'input'],
      message: t('register.validation.passwordRequired'),
    },
    { min: 8, trigger: ['blur', 'input'], message: t('register.validation.passwordMinLength') },
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

interface ApiProblem {
  status?: number
  title?: string
  detail?: string
}

const extractErrorMessage = (error: unknown, fallback: string): string => {
  if (error instanceof Error && error.message) {
    return error.message
  }
  if (error && typeof error === 'object') {
    const problem = error as ApiProblem
    return problem.detail || problem.title || fallback
  }
  return fallback
}

const onSubmit = async () => {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }

  submitting.value = true
  try {
    await auth.register({
      username: formValue.username.trim(),
      email: formValue.email.trim(),
      display_name: formValue.display_name.trim() || undefined,
      password: formValue.password,
    })
    message.success(t('register.messages.success'))
    const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : null
    await router.push(redirect ?? '/')
  } catch (error) {
    console.error(error)
    message.error(extractErrorMessage(error, t('register.messages.failed')))
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <BlankLayout :title="t('register.title')" :subtitle="t('register.subtitle')">
    <NCard :bordered="false" class="shadow-lg shadow-lf-shadow">
      <NForm
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

        <NFormItem :label="t('register.form.email')" path="email">
          <NInput
            v-model:value="formValue.email"
            :placeholder="t('register.form.emailPlaceholder')"
            clearable
            :input-props="{ autocomplete: 'email', type: 'email' }"
          />
        </NFormItem>

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

        <NButton attr-type="submit" type="primary" size="large" block :loading="submitting">
          {{ t('register.form.submit') }}
        </NButton>
      </NForm>
    </NCard>

    <div class="mt-6 text-center text-xs text-lf-text-muted">
      {{ t('register.links.hasAccount') }}
      <RouterLink to="/login" class="text-brand-500 no-underline hover:underline">
        {{ t('register.links.login') }}
      </RouterLink>
    </div>
  </BlankLayout>
</template>
