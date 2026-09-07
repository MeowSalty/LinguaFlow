<script setup lang="ts">
import { useI18n } from 'vue-i18n'

import QuickTranslateWidget from '@/components/QuickTranslateWidget.vue'

const router = useRouter()
const { t } = useI18n()
</script>

<template>
  <div class="lf-page">
    <section class="lf-page-header">
      <div class="space-y-1.5">
        <h1 class="text-2xl font-semibold tracking-tight text-lf-text-strong">
          {{ t('dashboard.title') }}
        </h1>
        <p class="max-w-2xl text-sm leading-6 text-lf-text-muted">
          {{ t('dashboard.subtitle') }}
        </p>
      </div>
      <NButton
        type="primary"
        @click="router.push({ path: '/projects', query: { create: '1' } })"
      >
        <IconCarbonAddAlt />
        {{ t('dashboard.quickActions.createProject.title') }}
      </NButton>
    </section>

    <QuickTranslateWidget variant="hero" />
  </div>
</template>
