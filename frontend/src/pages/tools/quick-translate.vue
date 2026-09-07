<script setup lang="ts">
import { useI18n } from 'vue-i18n'

import QuickTranslateWidget from '@/components/QuickTranslateWidget.vue'

const { t } = useI18n()
</script>

<template>
  <div class="lf-page">
    <section class="lf-page-header">
      <div class="space-y-1.5">
        <h1 class="text-2xl font-semibold tracking-tight text-lf-text-strong">
          {{ t('quickTranslate.pageTitle') }}
        </h1>
        <p class="max-w-2xl text-sm leading-6 text-lf-text-muted">
          {{ t('quickTranslate.pageSubtitle') }}
        </p>
      </div>
    </section>

    <QuickTranslateWidget variant="full" />
  </div>
</template>
