<script setup lang="ts">
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

const formRef = ref<FormInst | null>(null)
const submitting = ref(false)

const formValue = reactive({
  username: '',
  password: '',
})

const rules: FormRules = {
  username: [
    { required: true, trigger: ['blur', 'input'], message: '请输入用户名' },
  ],
  password: [
    { required: true, trigger: ['blur', 'input'], message: '请输入密码' },
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
    await auth.login({
      username: formValue.username.trim(),
      password: formValue.password,
    })
    message.success('登录成功')
    const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : null
    await router.push(redirect ?? '/')
  } catch (error) {
    console.error(error)
    message.error(extractErrorMessage(error, '登录失败,请检查用户名和密码'))
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <BlankLayout title="登录" subtitle="使用账号登录 LinguaFlow">
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
            placeholder="请输入用户名"
            clearable
            :input-props="{ autocomplete: 'username' }"
          />
        </NFormItem>

        <NFormItem label="密码" path="password">
          <NInput
            v-model:value="formValue.password"
            type="password"
            placeholder="请输入密码"
            show-password-on="click"
            :input-props="{ autocomplete: 'current-password' }"
          />
        </NFormItem>

        <NButton
          attr-type="submit"
          type="primary"
          size="large"
          block
          :loading="submitting"
        >
          登录
        </NButton>
      </NForm>
    </NCard>

    <div class="mt-6 flex items-center justify-between text-xs text-slate-500">
      <RouterLink
        to="/service"
        class="text-slate-500 no-underline transition-colors hover:text-brand-500"
      >
        切换服务器 · <span class="text-slate-400">{{ service.baseUrl }}</span>
      </RouterLink>
      <RouterLink
        to="/register"
        class="text-brand-500 no-underline hover:underline"
      >
        没有账号?去注册
      </RouterLink>
    </div>
  </BlankLayout>
</template>
