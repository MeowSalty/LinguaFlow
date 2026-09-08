<script setup lang="ts">
import { useI18n } from 'vue-i18n'

const router = useRouter()
const { t } = useI18n()
</script>

<template>
  <div class="lf-page flex min-h-[60vh] flex-col items-center justify-center text-center">
    <p class="lf-metric-value text-6xl text-lf-text-subtle">{{ t('notFound.code') }}</p>
    <h1 class="mt-4 text-xl font-semibold text-lf-text-strong">{{ t('notFound.title') }}</h1>
    <p class="mt-2 max-w-md text-sm text-lf-text-muted">{{ t('notFound.description') }}</p>
    <div class="mt-8 flex items-center gap-3">
      <NButton type="primary" @click="router.push('/')">
        {{ t('notFound.backHome') }}
      </NButton>
      <NButton secondary @click="router.back()">
        {{ t('notFound.backPrev') }}
      </NButton>
    </div>
  </div>
</template>
