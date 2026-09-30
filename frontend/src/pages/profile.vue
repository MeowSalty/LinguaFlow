<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useMessage, type FormInst, type FormRules } from 'naive-ui'

import PageHeader from '@/components/common/PageHeader.vue'
import { useAuthStore } from '@/stores/auth'
import { extractErrorMessage } from '@/utils/errors'

const auth = useAuthStore()
const message = useMessage()
const { t } = useI18n()

const formRef = ref<FormInst | null>(null)
const formValue = reactive({
  email: '',
  display_name: '',
})

const syncForm = (): void => {
  formValue.email = auth.user?.email ?? ''
  formValue.display_name = auth.user?.display_name ?? ''
}

const rules = computed<FormRules>(() => ({
  email: [
    {
      required: true,
      trigger: ['blur', 'input'],
      message: t('account.profile.validation.emailRequired'),
    },
    {
      trigger: ['blur', 'input'],
      validator(_rule, value: string) {
        if (!value || /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value.trim())) {
          return true
        }
        return new Error(t('account.profile.validation.emailInvalid'))
      },
    },
  ],
}))

const onSubmit = async (): Promise<void> => {
  try {
    await formRef.value?.validate()
  } catch {
    return
  }

  try {
    await auth.updateProfile({
      email: formValue.email.trim(),
      display_name: formValue.display_name.trim() || undefined,
    })
    message.success(t('account.profile.messages.updateSuccess'))
    syncForm()
  } catch (error) {
    console.error(error)
    message.error(extractErrorMessage(error, t('account.profile.messages.updateFailed')))
  }
}

onMounted(async () => {
  if (!auth.user) {
    try {
      await auth.fetchCurrentUser()
    } catch (error) {
      console.error(error)
    }
  }
  syncForm()
})
</script>

<template>
  <div class="lf-page lf-content-narrow">
    <PageHeader :title="t('account.profile.title')" :subtitle="t('account.profile.description')" />

    <div class="lf-panel p-6">
      <NForm
        ref="formRef"
        :model="formValue"
        :rules="rules"
        label-placement="top"
        require-mark-placement="right-hanging"
        @submit.prevent="onSubmit"
      >
        <NFormItem :label="t('account.profile.form.username')">
          <NInput :value="auth.user?.username ?? ''" disabled />
        </NFormItem>

        <NFormItem :label="t('account.profile.form.email')" path="email">
          <NInput
            v-model:value="formValue.email"
            :placeholder="t('account.profile.form.emailPlaceholder')"
            clearable
            :input-props="{ autocomplete: 'email', type: 'email' }"
          />
        </NFormItem>

        <NFormItem :label="t('account.profile.form.displayName')" path="display_name">
          <NInput
            v-model:value="formValue.display_name"
            :placeholder="t('account.profile.form.displayNamePlaceholder')"
            clearable
            :input-props="{ autocomplete: 'name' }"
          />
        </NFormItem>

        <NButton
          attr-type="submit"
          type="primary"
          :loading="auth.profileUpdating"
          :disabled="!auth.user"
        >
          {{ t('account.profile.form.submit') }}
        </NButton>
      </NForm>
    </div>
  </div>
</template>
