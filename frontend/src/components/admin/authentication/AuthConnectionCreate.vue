<script setup lang="ts">
import { computed, onScopeDispose, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NButton, NForm, NFormItem, NInput, NSelect, NSpace, useMessage } from 'naive-ui'
import type { ApiSchemas } from '@/api/client-core'
import {
  AuthConnectionError,
  createAuthConnection,
  type AuthConnection,
} from '@/api/auth-connections'
import { captureSession, isSessionCurrent } from '@/api/session-context'

const emit = defineEmits<{
  created: [record: AuthConnection]
  close: []
  denied: [detail: string]
}>()
const { t } = useI18n()
const message = useMessage()
const kind = ref<'oidc' | 'generic_oauth2'>('oidc')
const displayName = ref('')
const description = ref('')
const busy = ref(false)
let alive = true
onScopeDispose(() => {
  alive = false
})
const options = computed(() => [
  { label: t('authAdmin.oidc'), value: 'oidc' },
  { label: t('authAdmin.generic_oauth2'), value: 'generic_oauth2' },
])
async function create() {
  if (busy.value || !displayName.value.trim()) return
  const context = captureSession()
  busy.value = true
  const body: ApiSchemas['CreateAuthConnectionRequest'] = {
    kind: kind.value,
    display_name: displayName.value.trim(),
    ...(description.value ? { description: description.value } : {}),
  }
  try {
    const data = await createAuthConnection(body)
    if (alive && isSessionCurrent(context)) emit('created', data)
  } catch (cause) {
    if (!alive || !isSessionCurrent(context)) return
    if (cause instanceof AuthConnectionError && [403, 404].includes(cause.status ?? 0)) {
      emit('denied', cause.message)
      return
    }
    message.error(cause instanceof Error ? cause.message : t('authAdmin.requestFailed'))
  } finally {
    if (alive && isSessionCurrent(context)) busy.value = false
  }
}
</script>

<template>
  <div class="space-y-4">
    <n-form :disabled="busy" label-placement="top" @submit.prevent="create">
      <n-form-item :label="t('authAdmin.kind')">
        <n-select v-model:value="kind" :options="options" :aria-label="t('authAdmin.kind')" />
      </n-form-item>
      <n-form-item :label="t('authAdmin.name')" required>
        <n-input v-model:value="displayName" :input-props="{ 'aria-label': t('authAdmin.name') }" />
      </n-form-item>
      <n-form-item :label="t('authAdmin.description')">
        <n-input
          v-model:value="description"
          type="textarea"
          :input-props="{ 'aria-label': t('authAdmin.description') }"
          :autosize="{ minRows: 2, maxRows: 4 }"
        />
      </n-form-item>
      <n-alert type="info" :show-icon="false"
        >{{ t('authAdmin.incomplete') }} · {{ t('authAdmin.setup') }}</n-alert
      >
      <n-space justify="end" class="mt-4">
        <n-button :disabled="busy" @click="emit('close')">{{ t('authAdmin.cancel') }}</n-button>
        <n-button type="primary" attr-type="submit" :disabled="!displayName.trim()" :loading="busy">
          {{ t('authAdmin.create') }}
        </n-button>
      </n-space>
    </n-form>
  </div>
</template>
