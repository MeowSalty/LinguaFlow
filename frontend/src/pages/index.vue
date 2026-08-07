<script setup lang="ts">
import { useI18n } from 'vue-i18n'

import QuickTranslateWidget from '@/components/QuickTranslateWidget.vue'

const router = useRouter()
const { t } = useI18n()
</script>

<template>
  <div class="lf-page">
    <section class="lf-page-header">
      <div class="space-y-3">
        <div class="lf-eyebrow">{{ t('nav.dashboard') }}</div>
        <div>
          <h1 class="text-3xl font-semibold tracking-tight text-lf-text-strong">
            {{ t('dashboard.title') }}
          </h1>
          <p class="mt-2 max-w-2xl text-sm leading-6 text-lf-text-muted">
            {{ t('dashboard.subtitle') }}
          </p>
        </div>
      </div>
    </section>

    <QuickTranslateWidget variant="hero" />

    <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
      <button
        type="button"
        class="lf-interactive-card flex items-center gap-3 p-4 text-left"
        @click="router.push({ path: '/projects', query: { create: '1' } })"
      >
        <div
          class="flex h-11 w-11 items-center justify-center rounded-xl bg-lf-brand-soft text-brand-600"
        >
          <IconCarbonAddAlt class="h-5 w-5" />
        </div>
        <div class="min-w-0">
          <div class="font-medium text-lf-text-strong">
            {{ t('dashboard.quickActions.createProject.title') }}
          </div>
          <div class="mt-0.5 text-xs leading-5 text-lf-text-muted">
            {{ t('dashboard.quickActions.createProject.description') }}
          </div>
        </div>
      </button>

      <button
        type="button"
        class="lf-interactive-card flex items-center gap-3 p-4 text-left"
        @click="router.push({ path: '/projects' })"
      >
        <div
          class="flex h-11 w-11 items-center justify-center rounded-xl bg-lf-info-soft text-lf-info"
        >
          <IconCarbonDocument class="h-5 w-5" />
        </div>
        <div class="min-w-0">
          <div class="font-medium text-lf-text-strong">
            {{ t('dashboard.quickActions.viewProjects.title') }}
          </div>
          <div class="mt-0.5 text-xs leading-5 text-lf-text-muted">
            {{ t('dashboard.quickActions.viewProjects.description') }}
          </div>
        </div>
      </button>
    </div>
  </div>
</template>
