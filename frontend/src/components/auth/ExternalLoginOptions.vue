<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import { useMessage } from 'naive-ui'
import type { ApiSchemas } from '@/api/client-core'
import { StaleSessionError } from '@/api/session-context'
import { startExternalLogin } from '@/auth/external-browser'
import { connectionLogoUrl } from '@/auth/connection-logo'
import { extractErrorMessage } from '@/utils/errors'

const props = defineProps<{
  connections: ApiSchemas['PublicAuthConnection'][]
  returnTo: string
  disabled?: boolean
}>()
const busy = defineModel<boolean>('busy', { default: false })
const { t } = useI18n()
const message = useMessage()
const selected = shallowRef<string | null>(null)
async function start(connectionId: string) {
  if (busy.value || props.disabled) return
  busy.value = true
  selected.value = connectionId
  try {
    await startExternalLogin(connectionId, props.returnTo)
  } catch (error) {
    if (!(error instanceof StaleSessionError))
      message.error(extractErrorMessage(error, t('authentication.failed')))
    busy.value = false
    selected.value = null
  }
}
</script>

<template>
  <div class="space-y-3">
    <div v-for="connection in connections" :key="connection.connection_id">
      <NButton
        block
        size="large"
        :loading="selected === connection.connection_id"
        :disabled="disabled || (busy && selected !== connection.connection_id)"
        @click="start(connection.connection_id)"
      >
        <template v-if="connection.logo_url" #icon
          ><img
            :src="connectionLogoUrl(connection.connection_id)"
            alt=""
            width="20"
            height="20"
            referrerpolicy="no-referrer"
            class="object-contain"
        /></template>
        {{ t('authentication.externalLogin', { name: connection.display_name }) }}
      </NButton>
      <p v-if="connection.description" class="mt-1 text-xs leading-5 text-lf-text-muted">
        {{ connection.description }}
      </p>
    </div>
  </div>
</template>
