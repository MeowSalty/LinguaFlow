<script setup lang="ts">
import { useI18n } from 'vue-i18n'

import { useStatsStore } from '@/stores/stats'
import StatsCard from '@/components/dashboard/StatsCard.vue'

const router = useRouter()
const stats = useStatsStore()
const { t } = useI18n()

onMounted(() => {
  stats.loadAll()
})
</script>

<template>
  <div class="lf-page">
    <section class="lf-page-header">
      <div class="flex flex-col gap-4 lg:flex-row lg:items-end lg:justify-between">
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
        <div class="flex flex-wrap gap-3">
          <NButton secondary @click="stats.loadAll()">
            {{ t('projects.actions.refresh') }}
          </NButton>
          <NButton
            type="primary"
            @click="router.push({ path: '/projects', query: { create: '1' } })"
          >
            {{ t('dashboard.quickActions.createProject.title') }}
          </NButton>
        </div>
      </div>
    </section>

    <div class="grid grid-cols-1 gap-4 sm:grid-cols-2 xl:grid-cols-4">
      <StatsCard
        :title="t('dashboard.stats.apiCalls')"
        :value="stats.stats?.api_calls ?? 0"
        icon="carbon:api"
        tone="brand"
        :loading="stats.statsLoading"
      />
      <StatsCard
        :title="t('dashboard.stats.inputTokens')"
        :value="stats.stats?.input_tokens ?? 0"
        icon="carbon:cloud-upload"
        tone="info"
        :loading="stats.statsLoading"
      />
      <StatsCard
        :title="t('dashboard.stats.outputTokens')"
        :value="stats.stats?.output_tokens ?? 0"
        icon="carbon:cloud-download"
        tone="accent"
        :loading="stats.statsLoading"
      />
      <StatsCard
        :title="t('dashboard.stats.segmentCount')"
        :value="stats.stats?.segment_count ?? 0"
        icon="carbon:chart-column"
        tone="neutral"
        :loading="stats.statsLoading"
      />
    </div>

    <div class="grid grid-cols-1 gap-4 md:grid-cols-3">
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

      <button
        type="button"
        class="lf-interactive-card flex items-center gap-3 p-4 text-left"
        @click="router.push({ path: '/backends' })"
      >
        <div
          class="flex h-11 w-11 items-center justify-center rounded-xl bg-lf-accent-soft text-lf-accent"
        >
          <IconCarbonEnterprise class="h-5 w-5" />
        </div>
        <div class="min-w-0">
          <div class="font-medium text-lf-text-strong">
            {{ t('dashboard.quickActions.manageOrganizations.title') }}
          </div>
          <div class="mt-0.5 text-xs leading-5 text-lf-text-muted">
            {{ t('dashboard.quickActions.manageOrganizations.description') }}
          </div>
        </div>
      </button>
    </div>
  </div>
</template>
