<script setup lang="ts">
import { useI18n } from 'vue-i18n'

import { useStatsStore } from '@/stores/stats'
import StatsCard from '@/components/dashboard/StatsCard.vue'

const router = useRouter()
const stats = useStatsStore()
const { t } = useI18n()

// 页面加载时获取数据
onMounted(() => {
  stats.loadAll()
})
</script>

<template>
  <div class="space-y-6">
    <!-- 统计卡片区域 -->
    <div class="grid grid-cols-1 gap-6 sm:grid-cols-2 lg:grid-cols-4">
      <StatsCard
        :title="t('dashboard.stats.apiCalls')"
        :value="stats.stats?.api_calls ?? 0"
        icon="carbon:api"
        :loading="stats.statsLoading"
      />
      <StatsCard
        :title="t('dashboard.stats.inputTokens')"
        :value="stats.stats?.input_tokens ?? 0"
        icon="carbon:cloud-upload"
        :loading="stats.statsLoading"
      />
      <StatsCard
        :title="t('dashboard.stats.outputTokens')"
        :value="stats.stats?.output_tokens ?? 0"
        icon="carbon:cloud-download"
        :loading="stats.statsLoading"
      />
      <StatsCard
        :title="t('dashboard.stats.segmentCount')"
        :value="stats.stats?.segment_count ?? 0"
        icon="carbon:chart-column"
        :loading="stats.statsLoading"
      />
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
            <IconCarbonAddAlt />
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
            <IconCarbonDocument />
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
            <IconCarbonEnterprise />
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
