<script setup lang="ts">
import { computed, onScopeDispose, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NForm, NFormItem, NInput, NSelect, NSpace } from 'naive-ui'
import type { ApiSchemas } from '@/api/client-core'
import {
  createAuthConnection,
  AuthConnectionError,
  type AuthConnection,
} from '@/api/auth-connections'
import { captureSession, isSessionCurrent } from '@/api/session-context'
import AuthProtocolFields from './AuthProtocolFields.vue'
import { newConnectionSnapshot, connectionFormValid } from './auth-connection-model'
const emit = defineEmits<{
  created: [record: AuthConnection]
  close: []
  denied: [detail: string]
}>()
const { t } = useI18n()
const kind = ref<'oidc' | 'generic_oauth2'>('oidc')
const draft = ref(newConnectionSnapshot())
const secret = ref('')
const error = ref('')
const busy = ref(false)
let alive = true
onScopeDispose(() => {
  alive = false
  secret.value = ''
})
const options = computed(() => [
  { label: t('authAdmin.oidc'), value: 'oidc' },
  { label: t('authAdmin.generic_oauth2'), value: 'generic_oauth2' },
])
async function create() {
  if (busy.value || !connectionFormValid(kind.value, draft.value)) return
  const context = captureSession()
  busy.value = true
  error.value = ''
  const common = {
    display_name: draft.value.display_name.trim(),
    description: draft.value.description,
    ...(secret.value ? { client_secret: secret.value } : {}),
  }
  const body: ApiSchemas['CreateAuthConnectionRequest'] =
    kind.value === 'oidc'
      ? { ...common, kind: 'oidc', oidc: draft.value.oidc! }
      : { ...common, kind: 'generic_oauth2', oauth2: draft.value.oauth2! }
  try {
    const data = await createAuthConnection(body)
    if (alive && isSessionCurrent(context)) {
      secret.value = ''
      emit('created', data)
    }
  } catch (cause) {
    if (
      alive &&
      isSessionCurrent(context) &&
      cause instanceof AuthConnectionError &&
      [403, 404].includes(cause.status ?? 0)
    ) {
      secret.value = ''
      emit('denied', cause.message)
      return
    }
    if (alive && isSessionCurrent(context))
      error.value = cause instanceof Error ? cause.message : t('authAdmin.requestFailed')
  } finally {
    if (alive && isSessionCurrent(context)) busy.value = false
  }
}
</script>
<template>
  <div class="space-y-4">
    <n-alert v-if="error" type="error" role="alert">{{ error }}</n-alert>
    <n-form :disabled="busy" label-placement="top" @submit.prevent="create">
      <n-form-item :label="t('authAdmin.kind')"
        ><n-select v-model:value="kind" :options="options"
      /></n-form-item>
      <n-form-item :label="t('authAdmin.name')"
        ><n-input
          v-model:value="draft.display_name"
          :input-props="{ 'aria-label': t('authAdmin.name') }"
      /></n-form-item>
      <n-form-item :label="t('authAdmin.description')"
        ><n-input
          v-model:value="draft.description"
          :input-props="{ 'aria-label': t('authAdmin.description') }"
      /></n-form-item>
      <AuthProtocolFields v-model:value="draft" :kind="kind" :disabled="busy" />
      <n-form-item :label="t('authAdmin.secret')"
        ><n-input
          v-model:value="secret"
          type="password"
          show-password-on="click"
          :input-props="{ autocomplete: 'new-password', 'aria-label': t('authAdmin.secret') }"
      /></n-form-item>
      <n-space justify="end"
        ><n-button :disabled="busy" @click="emit('close')">{{ t('authAdmin.cancel') }}</n-button
        ><n-button
          type="primary"
          attr-type="submit"
          :disabled="!connectionFormValid(kind, draft)"
          :loading="busy"
          >{{ t('authAdmin.createDraft') }}</n-button
        ></n-space
      >
    </n-form>
  </div>
</template>
