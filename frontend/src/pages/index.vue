<script setup lang="ts">
import { useI18n } from 'vue-i18n'

import { useAuthStore } from '@/stores/auth'
import { useStatsStore } from '@/stores/stats'
import StatsCard from '@/components/dashboard/StatsCard.vue'
import ActivityFeed from '@/components/dashboard/ActivityFeed.vue'
import JobStatusOverview from '@/components/dashboard/JobStatusOverview.vue'

const router = useRouter()
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
    <NCard :bordered="false" class="shadow-sm shadow-lf-shadow">
      <div class="flex flex-col gap-2">
        <h1 class="text-2xl font-semibold tracking-tight text-lf-text-strong">
          {{ greeting }}
        </h1>
        <p class="text-sm text-lf-text-muted">
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
        class="cursor-pointer shadow-sm shadow-lf-shadow transition-shadow hover:shadow-md hover:shadow-lf-shadow-strong"
        @click="router.push({ path: '/projects', query: { create: '1' } })"
      >
        <div class="flex items-center gap-3">
          <div
            class="flex h-10 w-10 items-center justify-center rounded-lg bg-lf-brand-soft text-brand-500"
          >
            ➕
          </div>
          <div>
            <div class="font-medium text-lf-text-strong">
              {{ t('dashboard.quickActions.createProject.title') }}
            </div>
            <div class="text-xs text-lf-text-muted">
              {{ t('dashboard.quickActions.createProject.description') }}
            </div>
          </div>
        </div>
      </NCard>

      <NCard
        hoverable
        :bordered="false"
        class="cursor-pointer shadow-sm shadow-lf-shadow transition-shadow hover:shadow-md hover:shadow-lf-shadow-strong"
        @click="router.push({ path: '/projects' })"
      >
        <div class="flex items-center gap-3">
          <div
            class="flex h-10 w-10 items-center justify-center rounded-lg bg-lf-info-soft text-blue-500"
          >
            📋
          </div>
          <div>
            <div class="font-medium text-lf-text-strong">
              {{ t('dashboard.quickActions.viewProjects.title') }}
            </div>
            <div class="text-xs text-lf-text-muted">
              {{ t('dashboard.quickActions.viewProjects.description') }}
            </div>
          </div>
        </div>
      </NCard>

      <NCard
        hoverable
        :bordered="false"
        class="cursor-pointer shadow-sm shadow-lf-shadow transition-shadow hover:shadow-md hover:shadow-lf-shadow-strong"
      >
        <div class="flex items-center gap-3">
          <div
            class="flex h-10 w-10 items-center justify-center rounded-lg bg-lf-info-soft text-purple-500"
          >
            🏢
          </div>
          <div>
            <div class="font-medium text-lf-text-strong">
              {{ t('dashboard.quickActions.manageOrganizations.title') }}
            </div>
            <div class="text-xs text-lf-text-muted">
              {{ t('dashboard.quickActions.manageOrganizations.description') }}
            </div>
          </div>
        </div>
      </NCard>
    </div>
  </div>
</template>
