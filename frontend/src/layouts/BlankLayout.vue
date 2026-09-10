<script setup lang="ts">
import { useI18n } from 'vue-i18n'

import AppLogo from '@/components/AppLogo.vue'

defineProps<{
  title?: string
  subtitle?: string
}>()

const { t } = useI18n()
</script>

<template>
  <div class="lf-auth-shell">
    <div class="w-full max-w-105">
      <div class="mb-7 flex flex-col items-center gap-3 text-center">
        <RouterLink
          to="/"
          class="flex flex-col items-center gap-2.5 no-underline"
          :aria-label="t('common.appName')"
        >
          <AppLogo size="lg" :wordmark="false" />
          <span class="text-lg font-bold tracking-tight text-lf-text-strong">
            {{ t('common.appName') }}
          </span>
        </RouterLink>
        <h1 v-if="title" class="text-lg font-semibold text-lf-text-strong">
          {{ title }}
        </h1>
        <p v-if="subtitle" class="max-w-sm text-sm leading-6 text-lf-text-muted">
          {{ subtitle }}
        </p>
      </div>

      <slot />
    </div>
  </div>
</template>
