<script setup lang="ts">
import { useI18n } from 'vue-i18n'
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
const { t } = useI18n()

const formRef = ref<FormInst | null>(null)
const submitting = ref(false)

const formValue = reactive({
  baseUrl: service.baseUrl,
})

const rules = computed<FormRules>(() => ({
  baseUrl: [
    {
      required: true,
      trigger: ['blur', 'input'],
      validator(_rule, value: string) {
        if (!value || !value.trim()) {
          return new Error(t('service.validation.required'))
        }
        const trimmed = value.trim()
        if (trimmed.startsWith('/')) {
          return true
        }
        try {
          new URL(trimmed)
          return true
        } catch {
          return new Error(t('service.validation.invalidUrl'))
        }
      },
    },
  ],
}))

const onSubmit = async () => {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }

  submitting.value = true
  try {
    service.setBaseUrl(formValue.baseUrl)
    message.success(t('service.messages.connected', { url: service.baseUrl }))
    const redirect = typeof route.query.redirect === 'string' ? route.query.redirect : null
    await router.push(redirect ?? '/login')
  } finally {
    submitting.value = false
  }
}
</script>

<template>
  <BlankLayout :title="t('service.title')" :subtitle="t('service.subtitle')">
    <NCard :bordered="false" class="shadow-lg shadow-slate-200/60">
      <NForm
        ref="formRef"
        :model="formValue"
        :rules="rules"
        label-placement="top"
        require-mark-placement="right-hanging"
        @submit.prevent="onSubmit"
      >
        <NFormItem :label="t('service.form.baseUrl')" path="baseUrl">
          <NInput
            v-model:value="formValue.baseUrl"
            :placeholder="t('service.form.baseUrlPlaceholder')"
            clearable
            :input-props="{ autocomplete: 'off' }"
          />
        </NFormItem>

        <NButton attr-type="submit" type="primary" size="large" block :loading="submitting">
          {{ t('service.form.submit') }}
        </NButton>
      </NForm>

      <p class="mt-5 text-center text-xs text-slate-500">
        {{ t('service.hints.prefix') }}
        <code class="rounded bg-slate-100 px-1 py-0.5 text-slate-700">/api/v1</code>
        {{ t('service.hints.suffix') }}
      </p>
    </NCard>
  </BlankLayout>
</template>
