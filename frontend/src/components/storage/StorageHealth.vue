<script setup lang="ts">
import { computed } from 'vue'
import { NTag } from 'naive-ui'
import { useI18n } from 'vue-i18n'
const props = defineProps<{ value: string }>()
const { t } = useI18n()
// Unknown server health must never acquire the green success treatment.
const label = computed(
  () =>
    ({
      healthy: 'storage.healthy',
      available: 'storage.healthy',
      degraded: 'storage.healthDegraded',
      auth_required: 'storage.healthAuthRequired',
      permission_denied: 'storage.healthPermissionDenied',
      crypto_unavailable: 'storage.healthCryptoUnavailable',
      source_missing: 'storage.healthMissing',
      missing: 'storage.healthMissing',
      source_corrupt: 'storage.healthCorrupt',
      corrupt: 'storage.healthCorrupt',
      unavailable: 'storage.healthUnavailable',
    })[props.value] ?? 'storage.unknown',
)
const tone = computed(() =>
  ['healthy', 'available'].includes(props.value)
    ? 'success'
    : ['missing', 'corrupt', 'source_missing', 'source_corrupt'].includes(props.value)
      ? 'error'
      : ['degraded', 'auth_required', 'permission_denied', 'crypto_unavailable'].includes(
            props.value,
          )
        ? 'warning'
        : 'default',
)
</script>
<template>
  <NTag :type="tone" size="small" :bordered="false">{{ t(label) }}</NTag>
</template>
