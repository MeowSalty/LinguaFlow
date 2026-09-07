<script setup lang="ts">
import { useI18n } from 'vue-i18n'

import StackedProgressBar from '@/components/common/StackedProgressBar.vue'
import { useStatsStore } from '@/stores/stats'

const stats = useStatsStore()
const { n, t } = useI18n()

const completedJobs = computed(() => stats.stats?.completed_jobs ?? 0)
const failedJobs = computed(() => stats.stats?.failed_jobs ?? 0)

const totalJobs = computed(() => completedJobs.value + failedJobs.value)

const completedPercent = computed(() => {
  if (totalJobs.value === 0) return 0
  return Math.round((completedJobs.value / totalJobs.value) * 100)
})

const failedPercent = computed(() => {
  if (totalJobs.value === 0) return 0
  return 100 - completedPercent.value
})
</script>

<template>
  <div class="lf-panel h-full p-5">
    <h2 class="text-sm font-semibold tracking-wide text-lf-text-strong">
      {{ t('dashboard.jobStatus.title') }}
    </h2>

    <div v-if="stats.statsLoading" class="mt-6 space-y-4">
      <div class="h-3 w-full animate-pulse rounded-full bg-lf-border-soft" />
      <div class="grid grid-cols-2 gap-3">
        <div class="h-20 animate-pulse rounded-lf-card bg-lf-border-soft" />
        <div class="h-20 animate-pulse rounded-lf-card bg-lf-border-soft" />
      </div>
    </div>

    <NEmpty v-else-if="stats.statsError" :description="stats.statsError" class="mt-8" />

    <template v-else>
      <div class="mt-5">
        <div class="flex items-center justify-between text-xs text-lf-text-muted">
          <span>{{ t('dashboard.jobStatus.total', { count: n(totalJobs) }) }}</span>
          <span class="font-medium text-lf-text-strong">{{
            t('dashboard.jobStatus.successRate', { percent: completedPercent })
          }}</span>
        </div>
        <StackedProgressBar
          class="mt-2"
          :value="completedPercent"
          :error-value="failedPercent"
          height="8px"
        />
      </div>

      <div class="mt-5 grid grid-cols-2 gap-3">
        <div class="rounded-lf-card border border-lf-border-soft bg-lf-success-soft p-4">
          <div class="text-xs font-medium text-brand-600">
            {{ t('dashboard.jobStatus.completed') }}
          </div>
          <div class="mt-1.5 text-2xl font-semibold tabular-nums tracking-tight text-lf-text-strong">
            {{ n(completedJobs) }}
          </div>
        </div>
        <div class="rounded-lf-card border border-lf-border-soft bg-lf-danger-soft p-4">
          <div class="text-xs font-medium text-red-500">
            {{ t('dashboard.jobStatus.failed') }}
          </div>
          <div class="mt-1.5 text-2xl font-semibold tabular-nums tracking-tight text-lf-text-strong">
            {{ n(failedJobs) }}
          </div>
        </div>
      </div>
    </template>
  </div>
</template>
