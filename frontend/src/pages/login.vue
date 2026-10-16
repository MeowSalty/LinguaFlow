<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useMessage, type FormInst, type FormRules } from 'naive-ui'

import BlankLayout from '@/layouts/BlankLayout.vue'
import { useAuthStore } from '@/stores/auth'
import { useServiceStore } from '@/stores/service'
import { extractErrorMessage } from '@/utils/errors'
import { captureSession, isSessionCurrent, StaleSessionError } from '@/api/session-context'
import { useAuthCapabilities } from '@/composables/useAuthCapabilities'
import { safeReturnPath } from '@/auth/return-path'
import ExternalLoginOptions from '@/components/auth/ExternalLoginOptions.vue'

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
const {
  capabilities,
  connections,
  canSignup,
  loading,
  error: capabilitiesError,
  reload,
} = useAuthCapabilities()
const externalBusy = ref(false)
const returnTo = computed(() => safeReturnPath(route.query.redirect))
const identifierLabel = computed(() =>
  capabilities.value?.password.email_login_enabled
    ? capabilities.value.password.username_login_enabled
      ? t('authentication.identifier')
      : t('authentication.emailIdentifier')
    : t('authentication.usernameIdentifier'),
)

const formRef = ref<FormInst | null>(null)
const submitting = ref(false)
let alive = true
onBeforeUnmount(() => {
  alive = false
})

const formValue = reactive({
  identifier: '',
  password: '',
})

const rules = computed<FormRules>(() => ({
  identifier: [
    { required: true, trigger: ['blur', 'input'], message: t('login.validation.usernameRequired') },
  ],
  password: [
    { required: true, trigger: ['blur', 'input'], message: t('login.validation.passwordRequired') },
  ],
}))

const onSubmit = async () => {
  if (submitting.value || externalBusy.value || !capabilities.value?.password.can_login) return
  const context = captureSession()
  submitting.value = true
  try {
    await formRef.value?.validate()
  } catch {
    submitting.value = false
    return
  }

  if (!alive || !isSessionCurrent(context)) {
    submitting.value = false
    return
  }
  try {
    await auth.login({
      identifier: formValue.identifier,
      password: formValue.password,
    })
    if (!alive) return
    message.success(t('login.messages.success'))
    await router.push(returnTo.value)
  } catch (error) {
    if (
      !alive ||
      error instanceof StaleSessionError ||
      (error instanceof DOMException && error.name === 'AbortError')
    )
      return
    message.error(extractErrorMessage(error, t('login.messages.failed')))
  } finally {
    if (alive) submitting.value = false
  }
}
</script>

<template>
  <BlankLayout :title="t('login.title')" :subtitle="t('login.subtitle')">
    <div class="lf-panel p-6">
      <NSkeleton v-if="loading" text :repeat="5" />
      <NAlert v-else-if="capabilitiesError" type="error" :show-icon="false">
        {{ capabilitiesError }}
        <NButton class="mt-3" @click="reload">{{ t('authentication.retry') }}</NButton>
      </NAlert>
      <NAlert
        v-else-if="!capabilities?.password.can_login && !connections.length"
        type="info"
        :show-icon="false"
      >
        {{ t('authentication.noLogin') }}
      </NAlert>
      <NForm
        v-if="!loading && capabilities?.password.can_login"
        ref="formRef"
        :model="formValue"
        :rules="rules"
        label-placement="top"
        require-mark-placement="right-hanging"
        @submit.prevent="onSubmit"
      >
        <NFormItem :label="identifierLabel" path="identifier">
          <NInput
            v-model:value="formValue.identifier"
            :placeholder="identifierLabel"
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

        <NButton
          attr-type="submit"
          type="primary"
          size="large"
          block
          :loading="submitting"
          :disabled="externalBusy"
        >
          {{ t('login.form.submit') }}
        </NButton>
      </NForm>
      <NDivider v-if="capabilities?.password.can_login && connections.length">{{
        t('authentication.externalDivider')
      }}</NDivider>
      <ExternalLoginOptions
        v-if="connections.length"
        v-model:busy="externalBusy"
        :connections="connections"
        :return-to="returnTo"
        :disabled="submitting"
      />
    </div>

    <div class="mt-6 flex items-center justify-between text-xs text-lf-text-muted">
      <RouterLink
        to="/service"
        class="text-lf-text-muted no-underline transition-colors hover:text-brand-500"
      >
        {{ t('login.links.switchService', { name: service.displayName }) }}
      </RouterLink>
      <RouterLink
        v-if="canSignup"
        :to="{ path: '/register', query: { redirect: returnTo } }"
        class="text-brand-500 no-underline hover:underline"
      >
        {{ t('login.links.register') }}
      </RouterLink>
    </div>
  </BlankLayout>
</template>
