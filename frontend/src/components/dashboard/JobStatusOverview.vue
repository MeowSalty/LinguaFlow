<script setup lang="ts">
import { useStatsStore } from '@/stores/stats'

const stats = useStatsStore()

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
  <div class="rounded-xl bg-white p-6 shadow-sm shadow-slate-200/60">
    <h2 class="text-lg font-medium text-slate-900">任务状态概览</h2>

    <!-- 加载状态 -->
    <div v-if="stats.statsLoading" class="mt-6 space-y-4">
      <div class="h-4 w-full animate-pulse rounded bg-slate-200" />
      <div class="grid grid-cols-2 gap-4">
        <div class="h-16 animate-pulse rounded bg-slate-200" />
        <div class="h-16 animate-pulse rounded bg-slate-200" />
      </div>
    </div>

    <!-- 错误状态 -->
    <NEmpty v-else-if="stats.statsError" :description="stats.statsError" class="mt-8" />

    <!-- 内容 -->
    <template v-else>
      <!-- 进度条 -->
      <div class="mt-6">
        <div class="flex items-center justify-between text-xs text-slate-500">
          <span>总计 {{ totalJobs.toLocaleString() }} 个任务</span>
          <span>{{ completedPercent }}% 成功率</span>
        </div>
        <div class="mt-2 h-3 w-full overflow-hidden rounded-full bg-slate-100">
          <div class="flex h-full">
            <div
              class="bg-green-500 transition-all duration-500"
              :style="{ width: `${completedPercent}%` }"
            />
            <div
              class="bg-red-500 transition-all duration-500"
              :style="{ width: `${failedPercent}%` }"
            />
          </div>
        </div>
      </div>

      <!-- 详情卡片 -->
      <div class="mt-6 grid grid-cols-2 gap-4">
        <div class="rounded-lg bg-green-50 p-4">
          <div class="text-sm text-green-600">已完成</div>
          <div class="mt-1 text-2xl font-bold text-green-700">
            {{ completedJobs.toLocaleString() }}
          </div>
        </div>
        <div class="rounded-lg bg-red-50 p-4">
          <div class="text-sm text-red-600">失败</div>
          <div class="mt-1 text-2xl font-bold text-red-700">
            {{ failedJobs.toLocaleString() }}
          </div>
        </div>
      </div>
    </template>
  </div>
</template>
