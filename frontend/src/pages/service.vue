<script setup lang="ts">
import { useMessage, type FormInst, type FormRules } from 'naive-ui'

import BlankLayout from '@/layouts/BlankLayout.vue'
import { useServiceStore } from '@/stores/service'

definePage({
  meta: {
    public: true,
    layout: 'blank',
  },
})

const router = useRouter()
const route = useRoute()
const service = useServiceStore()
const message = useMessage()

const formRef = ref<FormInst | null>(null)
const submitting = ref(false)

const formValue = reactive({
  baseUrl: service.baseUrl,
})

const rules: FormRules = {
  baseUrl: [
    {
      required: true,
      trigger: ['blur', 'input'],
      validator(_rule, value: string) {
        if (!value || !value.trim()) {
          return new Error('请填写服务器地址')
        }
        const trimmed = value.trim()
        if (trimmed.startsWith('/')) {
          return true
        }
        try {
          new URL(trimmed)
          return true
        } catch {
          return new Error('请填写合法的 URL，例如 https://linguaflow.example.com/api/v1')
        }
      },
    },
  ],
}

const onSubmit = async () => {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }

  submitting.value = true
  try {
    service.setBaseUrl(formValue.baseUrl)
    message.success('已连接到 ' + service.baseUrl)
    const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : null
    await router.push(redirect ?? '/login')
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <BlankLayout
    title="选择 LinguaFlow 服务器"
    subtitle="填写你要连接的后端 API 地址,可以是自部署实例或托管服务"
  >
    <NCard :bordered="false" class="shadow-lg shadow-slate-200/60">
      <NForm
        ref="formRef"
        :model="formValue"
        :rules="rules"
        label-placement="top"
        require-mark-placement="right-hanging"
        @submit.prevent="onSubmit"
      >
        <NFormItem label="服务器地址" path="baseUrl">
          <NInput
            v-model:value="formValue.baseUrl"
            placeholder="https://linguaflow.example.com/api/v1"
            clearable
            :input-props="{ autocomplete: 'off' }"
          />
        </NFormItem>

        <NButton attr-type="submit" type="primary" size="large" block :loading="submitting">
          连接
        </NButton>
      </NForm>

      <p class="mt-5 text-center text-xs text-slate-500">
        留空或填写
        <code class="rounded bg-slate-100 px-1 py-0.5 text-slate-700">/api/v1</code>
        将使用当前页面的同源地址
      </p>
    </NCard>
  </BlankLayout>
</template>
