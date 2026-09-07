<script setup lang="ts">
import { useI18n } from 'vue-i18n'

import ActivityFeed from '@/components/dashboard/ActivityFeed.vue'
import JobStatusOverview from '@/components/dashboard/JobStatusOverview.vue'
import StatsCard from '@/components/dashboard/StatsCard.vue'
import { useStatsStore } from '@/stores/stats'

const stats = useStatsStore()
const { t } = useI18n()

onMounted(() => {
  stats.loadAll()
})
</script>

<template>
  <div class="lf-page">
    <section class="lf-page-header">
      <div class="space-y-1.5">
        <h1 class="text-2xl font-semibold tracking-tight text-lf-text-strong">
          {{ t('stats.title') }}
        </h1>
        <p class="max-w-2xl text-sm leading-6 text-lf-text-muted">
          {{ t('stats.subtitle') }}
        </p>
      </div>
      <div class="flex flex-wrap gap-3">
        <NButton
          secondary
          :loading="stats.statsLoading || stats.activitiesLoading"
          @click="stats.loadAll()"
        >
          {{ t('projects.actions.refresh') }}
        </NButton>
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

    <div class="grid grid-cols-1 gap-4 xl:grid-cols-5">
      <div class="xl:col-span-2">
        <JobStatusOverview />
      </div>
      <div class="xl:col-span-3">
        <ActivityFeed />
      </div>
    </div>
  </div>
</template>
