<script setup lang="ts">
import { useI18n } from 'vue-i18n'

import QuickTranslateWidget from '@/components/QuickTranslateWidget.vue'

const router = useRouter()
const { t } = useI18n()
</script>

<template>
  <div class="lf-page">
    <PageHeader :title="t('dashboard.title')" :subtitle="t('dashboard.subtitle')">
      <NButton type="primary" @click="router.push({ path: '/projects', query: { create: '1' } })">
        <IconCarbonAddAlt />
        {{ t('dashboard.quickActions.createProject.title') }}
      </NButton>
    </PageHeader>

    <QuickTranslateWidget />
  </div>
</template>
