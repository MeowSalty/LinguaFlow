<script setup lang="ts">
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
    return new Error('两次输入的密码不一致')
  }
  return true
}

const rules: FormRules = {
  username: [
    { required: true, trigger: ['blur', 'input'], message: '请输入用户名' },
    { min: 3, max: 32, trigger: ['blur', 'input'], message: '用户名长度需在 3-32 之间' },
  ],
  email: [
    { required: true, trigger: ['blur', 'input'], message: '请输入邮箱' },
    {
      trigger: ['blur', 'input'],
      validator(_rule, value: string) {
        if (!value) return true
        const ok = /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value.trim())
        return ok ? true : new Error('请输入合法的邮箱地址')
      },
    },
  ],
  password: [
    { required: true, trigger: ['blur', 'input'], message: '请输入密码' },
    { min: 8, trigger: ['blur', 'input'], message: '密码至少 8 位' },
  ],
  confirm_password: [
    { required: true, trigger: ['blur', 'input'], message: '请再次输入密码' },
    { trigger: ['blur', 'input'], validator: validatePasswordConfirm },
  ],
}

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
    message.success('注册成功,欢迎使用 LinguaFlow')
    const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : null
    await router.push(redirect ?? '/')
  } catch (error) {
    console.error(error)
    message.error(extractErrorMessage(error, '注册失败,请稍后重试'))
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <BlankLayout title="注册账号" subtitle="创建一个 LinguaFlow 账号">
    <NCard :bordered="false" class="shadow-lg shadow-slate-200/60">
      <NForm
        ref="formRef"
        :model="formValue"
        :rules="rules"
        label-placement="top"
        require-mark-placement="right-hanging"
        @submit.prevent="onSubmit"
      >
        <NFormItem label="用户名" path="username">
          <NInput
            v-model:value="formValue.username"
            placeholder="3-32 位,字母 / 数字 / 下划线"
            clearable
            :input-props="{ autocomplete: 'username' }"
          />
        </NFormItem>

        <NFormItem label="邮箱" path="email">
          <NInput
            v-model:value="formValue.email"
            placeholder="you@example.com"
            clearable
            :input-props="{ autocomplete: 'email', type: 'email' }"
          />
        </NFormItem>

        <NFormItem label="显示名(可选)" path="display_name">
          <NInput
            v-model:value="formValue.display_name"
            placeholder="留空则使用用户名"
            clearable
          />
        </NFormItem>

        <NFormItem label="密码" path="password">
          <NInput
            v-model:value="formValue.password"
            type="password"
            placeholder="至少 8 位"
            show-password-on="click"
            :input-props="{ autocomplete: 'new-password' }"
          />
        </NFormItem>

        <NFormItem label="确认密码" path="confirm_password">
          <NInput
            v-model:value="formValue.confirm_password"
            type="password"
            placeholder="再次输入密码"
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
        >
          注册并登录
        </NButton>
      </NForm>
    </NCard>

    <div class="mt-6 text-center text-xs text-slate-500">
      已经有账号了?
      <RouterLink to="/login" class="text-brand-500 no-underline hover:underline">
        去登录
      </RouterLink>
    </div>
  </BlankLayout>
</template>
