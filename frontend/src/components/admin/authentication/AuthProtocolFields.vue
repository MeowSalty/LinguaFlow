<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NDynamicTags, NFormItem, NInput, NSelect, NSwitch } from 'naive-ui'
import type { AuthConnectionSnapshot } from '@/api/auth-connections'
const props = defineProps<{
  kind: 'oidc' | 'generic_oauth2'
  disabled?: boolean
  immutable?: boolean
}>()
const value = defineModel<AuthConnectionSnapshot>('value', { required: true })
const { t } = useI18n()
const config = computed(() => (props.kind === 'oidc' ? value.value.oidc : value.value.oauth2))
const methods = [
  { label: 'client_secret_basic', value: 'client_secret_basic' },
  { label: 'client_secret_post', value: 'client_secret_post' },
]
const subjectTypes = computed(() => [
  { label: t('authAdmin.string'), value: 'string' },
  { label: t('authAdmin.integer'), value: 'integer' },
])
function setIssuer(issuer: string) {
  if (value.value.oidc) value.value.oidc.issuer = issuer
}
</script>

<template>
  <n-alert v-if="immutable" type="info" :show-icon="false" class="mb-4">{{
    t('authAdmin.immutable')
  }}</n-alert>
  <n-form-item v-if="kind === 'oidc' && value.oidc" :label="t('authAdmin.issuer')"
    ><n-input
      :value="value.oidc?.issuer"
      :disabled="disabled || immutable"
      :input-props="{ 'aria-label': t('authAdmin.issuer') }"
      placeholder="https://identity.example.com"
      @update:value="setIssuer"
  /></n-form-item>
  <template v-if="kind === 'generic_oauth2' && value.oauth2">
    <n-form-item
      v-for="field in ['authorization_endpoint', 'token_endpoint', 'profile_endpoint'] as const"
      :key="field"
      :label="
        t(
          `authAdmin.${field === 'authorization_endpoint' ? 'authorizationEndpoint' : field === 'token_endpoint' ? 'tokenEndpoint' : 'profileEndpoint'}`,
        )
      "
    >
      <n-input
        v-model:value="value.oauth2[field]"
        :disabled="disabled || immutable"
        :input-props="{ 'aria-label': field }"
      />
    </n-form-item>
    <n-form-item :label="t('authAdmin.subjectPointer')"
      ><n-input
        v-model:value="value.oauth2.subject_mapping.pointer"
        :disabled="disabled || immutable"
        :input-props="{ 'aria-label': t('authAdmin.subjectPointer') }"
    /></n-form-item>
    <n-form-item :label="t('authAdmin.subjectType')"
      ><n-select
        v-model:value="value.oauth2.subject_mapping.type"
        :options="subjectTypes"
        :disabled="disabled || immutable"
    /></n-form-item>
  </template>
  <template v-if="config">
    <n-form-item :label="t('authAdmin.clientId')"
      ><n-input
        v-model:value="config.client_id"
        :disabled="disabled || immutable"
        :input-props="{ 'aria-label': t('authAdmin.clientId') }"
    /></n-form-item>
    <n-form-item :label="t('authAdmin.authMethod')"
      ><n-select v-model:value="config.client_auth_method" :options="methods" :disabled="disabled"
    /></n-form-item>
    <n-form-item :label="t('authAdmin.scopes')"
      ><n-dynamic-tags v-model:value="config.scopes" :disabled="disabled"
    /></n-form-item>
  </template>
  <n-form-item v-if="kind === 'oidc' && value.oidc" :label="t('authAdmin.userinfo')"
    ><n-switch
      v-model:value="value.oidc.userinfo_enabled"
      :disabled="disabled"
      :aria-label="t('authAdmin.userinfo')"
  /></n-form-item>
</template>
