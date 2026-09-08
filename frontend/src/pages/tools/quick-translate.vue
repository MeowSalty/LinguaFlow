<script setup lang="ts">
import { useI18n } from 'vue-i18n'

import QuickTranslateWidget from '@/components/QuickTranslateWidget.vue'

const { t } = useI18n()
</script>

<template>
  <div class="lf-page">
    <PageHeader
      :title="t('quickTranslate.pageTitle')"
      :subtitle="t('quickTranslate.pageSubtitle')"
    />

    <QuickTranslateWidget variant="full" />
  </div>
</template>
