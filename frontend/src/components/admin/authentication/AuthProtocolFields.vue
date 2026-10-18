<script setup lang="ts">
import { computed, type InputHTMLAttributes } from 'vue'
import { useI18n } from 'vue-i18n'
import { NAlert, NDynamicTags, NFormItem, NInput, NSelect, NSwitch } from 'naive-ui'
import type { AuthConnectionSnapshot } from '@/api/auth-connections'

defineProps<{
  kind: 'oidc' | 'generic_oauth2'
  disabled?: boolean
  immutable?: boolean
}>()
const value = defineModel<AuthConnectionSnapshot>('value', { required: true })
const { t } = useI18n()

type OIDCConfig = NonNullable<AuthConnectionSnapshot['oidc']>
type OAuth2Config = NonNullable<AuthConnectionSnapshot['oauth2']>

const oidc = computed(() => value.value.oidc)
const oauth2 = computed(() => value.value.oauth2)
const methods = [
  { label: 'client_secret_basic', value: 'client_secret_basic' },
  { label: 'client_secret_post', value: 'client_secret_post' },
]
const subjectTypes = computed(() => [
  { label: t('authAdmin.string'), value: 'string' },
  { label: t('authAdmin.integer'), value: 'integer' },
])

/** Protocol groups are optional in the API; writes only ever update containers that already exist. */
function updateOidc(patch: Partial<OIDCConfig>) {
  if (!value.value.oidc) return
  value.value.oidc = { ...value.value.oidc, ...patch }
}

function updateOauth2(patch: Partial<OAuth2Config>) {
  if (!value.value.oauth2) return
  value.value.oauth2 = { ...value.value.oauth2, ...patch }
}

function updateOauth2Endpoint(
  field: 'authorization_endpoint' | 'token_endpoint' | 'profile_endpoint',
  endpoint: string,
) {
  if (!value.value.oauth2) return
  value.value.oauth2 = { ...value.value.oauth2, [field]: endpoint }
}

function updateSubjectMapping(patch: Partial<NonNullable<OAuth2Config['subject_mapping']>>) {
  const cfg = value.value.oauth2
  if (!cfg) return
  value.value.oauth2 = {
    ...cfg,
    subject_mapping: { pointer: '', type: 'string', ...cfg.subject_mapping, ...patch },
  }
}

/** data-testid keeps full input element placement without tripping Vue's excess-property check. */
function inputProps(
  id: string,
  attrs: InputHTMLAttributes = {},
): InputHTMLAttributes & { 'data-testid': string } {
  return { ...attrs, 'data-testid': id }
}
</script>

<template>
  <n-alert v-if="immutable" type="info" :show-icon="false" class="mb-4">{{
    t('authAdmin.immutable')
  }}</n-alert>
  <template v-if="kind === 'oidc'">
    <n-form-item :label="t('authAdmin.issuer')" data-config-field="oidc.issuer">
      <n-input
        :value="oidc?.issuer ?? ''"
        :disabled="disabled || immutable"
        :input-props="inputProps('auth-connection-issuer', { 'aria-label': t('authAdmin.issuer') })"
        placeholder="https://identity.example.com"
        @update:value="(issuer: string) => updateOidc({ issuer })"
      />
    </n-form-item>
    <n-form-item :label="t('authAdmin.clientId')" data-config-field="oidc.client_id">
      <n-input
        :value="oidc?.client_id ?? ''"
        :disabled="disabled || immutable"
        :input-props="
          inputProps('auth-connection-client-id', { 'aria-label': t('authAdmin.clientId') })
        "
        @update:value="(client_id: string) => updateOidc({ client_id })"
      />
    </n-form-item>
    <n-form-item :label="t('authAdmin.authMethod')">
      <n-select
        :value="oidc?.client_auth_method ?? 'client_secret_basic'"
        :options="methods"
        :disabled="disabled"
        :data-testid="'auth-connection-auth-method'"
        @update:value="
          (method: 'client_secret_basic' | 'client_secret_post') =>
            updateOidc({ client_auth_method: method })
        "
      />
    </n-form-item>
    <n-form-item :label="t('authAdmin.scopes')">
      <NDynamicTags
        :value="oidc?.scopes ?? []"
        :disabled="disabled"
        @update:value="(scopes: string[]) => updateOidc({ scopes })"
      />
    </n-form-item>
    <n-form-item :label="t('authAdmin.userinfo')">
      <NSwitch
        :value="oidc?.userinfo_enabled ?? true"
        :disabled="disabled"
        :aria-label="t('authAdmin.userinfo')"
        :data-testid="'auth-connection-userinfo-enabled'"
        @update:value="(userinfo_enabled: boolean) => updateOidc({ userinfo_enabled })"
      />
    </n-form-item>
  </template>
  <template v-if="kind === 'generic_oauth2'">
    <n-form-item
      v-for="field in ['authorization_endpoint', 'token_endpoint', 'profile_endpoint'] as const"
      :key="field"
      :data-config-field="'oauth2.' + field"
      :label="
        t(
          `authAdmin.${field === 'authorization_endpoint' ? 'authorizationEndpoint' : field === 'token_endpoint' ? 'tokenEndpoint' : 'profileEndpoint'}`,
        )
      "
    >
      <n-input
        :value="oauth2?.[field] ?? ''"
        :disabled="disabled || immutable"
        :input-props="
          inputProps('auth-connection-' + field.replace(/_/g, '-'), { 'aria-label': field })
        "
        @update:value="(endpoint: string) => updateOauth2Endpoint(field, endpoint)"
      />
    </n-form-item>
    <n-form-item :label="t('authAdmin.clientId')" data-config-field="oauth2.client_id">
      <n-input
        :value="oauth2?.client_id ?? ''"
        :disabled="disabled || immutable"
        :input-props="
          inputProps('auth-connection-client-id', { 'aria-label': t('authAdmin.clientId') })
        "
        @update:value="(client_id: string) => updateOauth2({ client_id })"
      />
    </n-form-item>
    <n-form-item :label="t('authAdmin.authMethod')">
      <n-select
        :value="oauth2?.client_auth_method ?? 'client_secret_basic'"
        :options="methods"
        :disabled="disabled"
        :data-testid="'auth-connection-auth-method'"
        @update:value="
          (method: 'client_secret_basic' | 'client_secret_post') =>
            updateOauth2({ client_auth_method: method })
        "
      />
    </n-form-item>
    <n-form-item :label="t('authAdmin.scopes')">
      <NDynamicTags
        :value="oauth2?.scopes ?? []"
        :disabled="disabled"
        @update:value="(scopes: string[]) => updateOauth2({ scopes })"
      />
    </n-form-item>
    <n-form-item
      :label="t('authAdmin.subjectPointer')"
      data-config-field="oauth2.subject_mapping.pointer"
    >
      <n-input
        :value="oauth2?.subject_mapping?.pointer ?? ''"
        :disabled="disabled || immutable"
        :input-props="
          inputProps('auth-connection-subject-pointer', {
            'aria-label': t('authAdmin.subjectPointer'),
          })
        "
        @update:value="(pointer: string) => updateSubjectMapping({ pointer })"
      />
    </n-form-item>
    <n-form-item :label="t('authAdmin.subjectType')">
      <n-select
        :value="oauth2?.subject_mapping?.type ?? 'string'"
        :options="subjectTypes"
        :disabled="disabled || immutable"
        :data-testid="'auth-connection-subject-type'"
        @update:value="(type: 'string' | 'integer') => updateSubjectMapping({ type })"
      />
    </n-form-item>
  </template>
</template>
