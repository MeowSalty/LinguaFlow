<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useMessage, type FormInst, type FormRules } from 'naive-ui'

import BlankLayout from '@/layouts/BlankLayout.vue'
import { useAuthStore } from '@/stores/auth'
import { useServiceStore } from '@/stores/service'

definePage({
  meta: {
    public: true,
    layout: 'blank',
  },
})

const router = useRouter()
const route = useRoute()
const auth = useAuthStore()
const service = useServiceStore()
const message = useMessage()
const { t } = useI18n()

const formRef = ref<FormInst | null>(null)
const submitting = ref(false)

const formValue = reactive({
  username: '',
  password: '',
})

const rules = computed<FormRules>(() => ({
  username: [
    { required: true, trigger: ['blur', 'input'], message: t('login.validation.usernameRequired') },
  ],
  password: [
    { required: true, trigger: ['blur', 'input'], message: t('login.validation.passwordRequired') },
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
    await auth.login({
      username: formValue.username.trim(),
      password: formValue.password,
    })
    message.success(t('login.messages.success'))
    const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : null
    await router.push(redirect ?? '/')
  } catch (error) {
    console.error(error)
    message.error(extractErrorMessage(error, t('login.messages.failed')))
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <BlankLayout :title="t('login.title')" :subtitle="t('login.subtitle')">
    <NCard :bordered="false" class="shadow-lg shadow-lf-shadow">
      <NForm
        ref="formRef"
        :model="formValue"
        :rules="rules"
        label-placement="top"
        require-mark-placement="right-hanging"
        @submit.prevent="onSubmit"
      >
        <NFormItem :label="t('login.form.username')" path="username">
          <NInput
            v-model:value="formValue.username"
            :placeholder="t('login.form.usernamePlaceholder')"
            clearable
            :input-props="{ autocomplete: 'username' }"
          />
        </NFormItem>

        <NFormItem :label="t('login.form.password')" path="password">
          <NInput
            v-model:value="formValue.password"
            type="password"
            :placeholder="t('login.form.passwordPlaceholder')"
            show-password-on="click"
            :input-props="{ autocomplete: 'current-password' }"
          />
        </NFormItem>

        <NButton attr-type="submit" type="primary" size="large" block :loading="submitting">
          {{ t('login.form.submit') }}
        </NButton>
      </NForm>
    </NCard>

    <div class="mt-6 flex items-center justify-between text-xs text-lf-text-muted">
      <RouterLink
        to="/service"
        class="text-lf-text-muted no-underline transition-colors hover:text-brand-500"
      >
        {{ t('login.links.switchService', { name: service.displayName }) }}
      </RouterLink>
      <RouterLink to="/register" class="text-brand-500 no-underline hover:underline">
        {{ t('login.links.register') }}
      </RouterLink>
    </div>
  </BlankLayout>
</template>
