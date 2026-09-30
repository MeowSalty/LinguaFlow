<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useMessage, type FormInst, type FormRules } from 'naive-ui'
import { captureSession, isSessionCurrent, sessionGeneration } from '@/api/session-context'
import { useAuthStore } from '@/stores/auth'
import { useServiceStore } from '@/stores/service'
import { useOrganizationsStore } from '@/stores/organizations'
import { extractErrorMessage } from '@/utils/errors'

const auth = useAuthStore()
const service = useServiceStore()
const organizations = useOrganizationsStore()
const message = useMessage()
const { t } = useI18n()
const formRef = ref<FormInst | null>(null)
const error = ref<string | null>(null)
const formValue = reactive({ email: '', display_name: '' })
const syncForm = (): void => {
  formValue.email = auth.user?.email ?? ''
  formValue.display_name = auth.user?.display_name ?? ''
}
const rules = computed<FormRules>(() => ({
  email: [
    {
      trigger: ['blur', 'input'],
      validator(_rule, value: string) {
        if (!value?.trim()) return new Error(t('account.profile.validation.emailRequired'))
        return (
          /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(value.trim()) ||
          new Error(t('account.profile.validation.emailInvalid'))
        )
      },
    },
  ],
}))
const onSubmit = async (): Promise<void> => {
  if (service.isLocal || !auth.user || auth.profileUpdating) return
  try {
    await formRef.value?.validate()
  } catch {
    return
  }
  const snapshot = captureSession()
  const payload: { email?: string; display_name?: string } = {}
  const email = formValue.email.trim()
  const displayName = formValue.display_name.trim()
  if (email !== auth.user.email) payload.email = email
  if (displayName !== (auth.user.display_name ?? '')) payload.display_name = displayName
  if (!Object.keys(payload).length) {
    message.info(t('workbench.settings.noChanges'))
    return
  }
  error.value = null
  try {
    await auth.updateProfile(payload)
    if (!isSessionCurrent(snapshot)) return
    syncForm()
    message.success(t('account.profile.messages.updateSuccess'))
  } catch (cause) {
    if (isSessionCurrent(snapshot))
      error.value = extractErrorMessage(cause, t('account.profile.messages.updateFailed'))
  }
}
watch(
  sessionGeneration,
  () => {
    error.value = null
    syncForm()
  },
  { flush: 'sync' },
)
watch(() => auth.user, syncForm, { immediate: true })
onMounted(() => {
  if (!service.isLocal) void organizations.refresh()
})
</script>
<template>
  <section class="lf-panel p-5 sm:p-6">
    <h2 class="mb-2 text-lg font-semibold text-lf-text-strong">
      {{ t('workbench.settings.profile') }}
    </h2>
    <p class="mb-5 text-sm text-lf-text-muted">{{ t('account.profile.description') }}</p>
    <NAlert v-if="service.isLocal" type="info" class="mb-5">{{
      t('workbench.settings.localProfile')
    }}</NAlert>
    <NAlert v-if="error" type="error" class="mb-5">{{ error }}</NAlert>
    <NForm
      ref="formRef"
      :model="formValue"
      :rules="rules"
      label-placement="top"
      @submit.prevent="onSubmit"
    >
      <NFormItem :label="t('account.profile.form.username')"
        ><NInput :value="auth.user?.username ?? ''" disabled
      /></NFormItem>
      <NFormItem :label="t('account.profile.form.email')" path="email">
        <NInput
          v-model:value="formValue.email"
          :disabled="service.isLocal"
          :input-props="{ autocomplete: 'email', type: 'email' }"
        />
      </NFormItem>
      <NFormItem :label="t('account.profile.form.displayName')" path="display_name">
        <NInput
          v-model:value="formValue.display_name"
          :disabled="service.isLocal"
          clearable
          :input-props="{ autocomplete: 'name' }"
        />
      </NFormItem>
      <div class="mb-6 grid grid-cols-2 gap-4 text-sm">
        <div>
          <span class="text-lf-text-muted">{{ t('workbench.settings.role') }}</span>
          <p class="mt-1">{{ auth.user?.role ?? '—' }}</p>
        </div>
        <div>
          <span class="text-lf-text-muted">{{ t('workbench.settings.status') }}</span>
          <p class="mt-1">
            {{
              auth.user
                ? t(auth.user.active ? 'workbench.settings.active' : 'workbench.settings.inactive')
                : '—'
            }}
          </p>
        </div>
      </div>
      <NButton
        v-if="!service.isLocal"
        attr-type="submit"
        type="primary"
        :loading="auth.profileUpdating"
        :disabled="!auth.user"
        >{{ t('account.profile.form.submit') }}</NButton
      >
    </NForm>
  </section>
  <section v-if="!service.isLocal" class="lf-panel p-5 sm:p-6">
    <div class="mb-4 flex items-center justify-between gap-4">
      <h2 class="text-sm font-semibold">{{ t('workbench.settings.organizations') }}</h2>
      <NButton text @click="organizations.refresh()">{{ t('common.actions.refresh') }}</NButton>
    </div>
    <NSkeleton v-if="organizations.loading && !organizations.items.length" height="48px" />
    <NAlert v-if="organizations.error" type="warning" class="mb-3">{{
      organizations.error
    }}</NAlert>
    <div v-if="organizations.items.length" class="flex flex-wrap gap-2">
      <RouterLink
        v-for="org in organizations.items"
        :key="org.id"
        :to="{ path: '/settings/team', query: { org_id: String(org.id) } }"
        class="rounded-lg border border-lf-border-soft px-3 py-2 text-sm text-lf-text no-underline hover:text-brand-500"
        >{{ org.name }}</RouterLink
      >
    </div>
    <NEmpty
      v-else-if="!organizations.loading && !organizations.error"
      :description="t('workbench.settings.noOrganizations')"
    />
  </section>
</template>
