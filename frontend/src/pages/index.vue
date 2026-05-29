<script setup lang="ts">
import { useI18n } from 'vue-i18n'

import { useAuthStore } from '@/stores/auth'
import { useStatsStore } from '@/stores/stats'
import StatsCard from '@/components/dashboard/StatsCard.vue'
import ActivityFeed from '@/components/dashboard/ActivityFeed.vue'
import JobStatusOverview from '@/components/dashboard/JobStatusOverview.vue'

const auth = useAuthStore()
const stats = useStatsStore()
const { t } = useI18n()

const greeting = computed(() => {
  const name = auth.user?.display_name?.trim() || auth.user?.username
  return name ? t('dashboard.greeting.named', { name }) : t('dashboard.greeting.anonymous')
})

// 页面加载时获取数据
onMounted(() => {
  stats.loadAll()
})
</script>

<template>
  <div class="space-y-6">
    <!-- 欢迎区域 -->
    <NCard :bordered="false" class="shadow-sm shadow-slate-200/60">
      <div class="flex flex-col gap-2">
        <h1 class="text-2xl font-semibold tracking-tight text-slate-900">
          {{ greeting }}
        </h1>
        <p class="text-sm text-slate-500">
          {{ t('dashboard.intro') }}
        </p>
      </div>
    </NCard>

    <!-- 统计卡片区域 -->
    <div class="grid grid-cols-1 gap-6 sm:grid-cols-2 lg:grid-cols-4">
      <StatsCard
        :title="t('dashboard.stats.apiCalls')"
        :value="stats.stats?.api_calls ?? 0"
        icon="🔗"
        :loading="stats.statsLoading"
      />
      <StatsCard
        :title="t('dashboard.stats.inputTokens')"
        :value="stats.stats?.input_tokens ?? 0"
        icon="📥"
        :loading="stats.statsLoading"
      />
      <StatsCard
        :title="t('dashboard.stats.outputTokens')"
        :value="stats.stats?.output_tokens ?? 0"
        icon="📤"
        :loading="stats.statsLoading"
      />
      <StatsCard
        :title="t('dashboard.stats.segmentCount')"
        :value="stats.stats?.segment_count ?? 0"
        icon="📊"
        :loading="stats.statsLoading"
      />
    </div>

    <!-- 主要内容区域 -->
    <div class="grid grid-cols-1 gap-6 lg:grid-cols-5">
      <div class="lg:col-span-3">
        <ActivityFeed />
      </div>
      <div class="lg:col-span-2">
        <JobStatusOverview />
      </div>
    </div>

    <!-- 快速操作区域 -->
    <div class="grid grid-cols-1 gap-4 sm:grid-cols-3">
      <NCard
        hoverable
        :bordered="false"
        class="cursor-pointer shadow-sm shadow-slate-200/60 transition-shadow hover:shadow-md hover:shadow-slate-200/80"
      >
        <div class="flex items-center gap-3">
          <div
            class="flex h-10 w-10 items-center justify-center rounded-lg bg-brand-50 text-brand-500"
          >
            ➕
          </div>
          <div>
            <div class="font-medium text-slate-900">
              {{ t('dashboard.quickActions.createJob.title') }}
            </div>
            <div class="text-xs text-slate-500">
              {{ t('dashboard.quickActions.createJob.description') }}
            </div>
          </div>
        </div>
      </NCard>

      <NCard
        hoverable
        :bordered="false"
        class="cursor-pointer shadow-sm shadow-slate-200/60 transition-shadow hover:shadow-md hover:shadow-slate-200/80"
      >
        <div class="flex items-center gap-3">
          <div
            class="flex h-10 w-10 items-center justify-center rounded-lg bg-blue-50 text-blue-500"
          >
            📋
          </div>
          <div>
            <div class="font-medium text-slate-900">
              {{ t('dashboard.quickActions.viewJobs.title') }}
            </div>
            <div class="text-xs text-slate-500">
              {{ t('dashboard.quickActions.viewJobs.description') }}
            </div>
          </div>
        </div>
      </NCard>

      <NCard
        hoverable
        :bordered="false"
        class="cursor-pointer shadow-sm shadow-slate-200/60 transition-shadow hover:shadow-md hover:shadow-slate-200/80"
      >
        <div class="flex items-center gap-3">
          <div
            class="flex h-10 w-10 items-center justify-center rounded-lg bg-purple-50 text-purple-500"
          >
            🏢
          </div>
          <div>
            <div class="font-medium text-slate-900">
              {{ t('dashboard.quickActions.manageOrganizations.title') }}
            </div>
            <div class="text-xs text-slate-500">
              {{ t('dashboard.quickActions.manageOrganizations.description') }}
            </div>
          </div>
        </div>
      </NCard>
    </div>
  </div>
</template>
