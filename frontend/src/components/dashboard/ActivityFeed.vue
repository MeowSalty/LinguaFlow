<script setup lang="ts">
import { useStatsStore } from '@/stores/stats'

const stats = useStatsStore()

const relativeTime = (dateStr: string): string => {
  const now = Date.now()
  const date = new Date(dateStr).getTime()
  const diff = now - date

  const seconds = Math.floor(diff / 1000)
  if (seconds < 60) return '刚刚'

  const minutes = Math.floor(seconds / 60)
  if (minutes < 60) return `${minutes}分钟前`

  const hours = Math.floor(minutes / 60)
  if (hours < 24) return `${hours}小时前`

  const days = Math.floor(hours / 24)
  if (days < 30) return `${days}天前`

  return new Date(dateStr).toLocaleDateString('zh-CN')
}

const actionLabels: Record<string, string> = {
  create: '创建了',
  update: '更新了',
  delete: '删除了',
  complete: '完成了',
  fail: '失败了',
  approve: '审核通过了',
  reject: '驳回了',
}

const getActionLabel = (action: string): string => {
  return actionLabels[action] || action
}
</script>

<template>
  <div class="rounded-xl bg-white p-6 shadow-sm shadow-slate-200/60">
    <h2 class="text-lg font-medium text-slate-900">最近活动</h2>

    <!-- 加载状态 -->
    <div v-if="stats.activitiesLoading && stats.activities.length === 0" class="mt-4 space-y-4">
      <div v-for="i in 5" :key="i" class="flex items-start gap-3">
        <div class="mt-1 h-2 w-2 shrink-0 animate-pulse rounded-full bg-slate-200" />
        <div class="flex-1 space-y-1">
          <div class="h-4 w-3/4 animate-pulse rounded bg-slate-200" />
          <div class="h-3 w-1/3 animate-pulse rounded bg-slate-200" />
        </div>
      </div>
    </div>

    <!-- 错误状态 -->
    <NEmpty v-else-if="stats.activitiesError" :description="stats.activitiesError" class="mt-8" />

    <!-- 空状态 -->
    <NEmpty v-else-if="stats.activities.length === 0" description="暂无活动记录" class="mt-8" />

    <!-- 活动列表 -->
    <div v-else class="mt-4 space-y-4">
      <div
        v-for="activity in stats.activities"
        :key="activity.id"
        class="flex items-start gap-3 transition-opacity"
      >
        <!-- 圆点指示器 -->
        <div class="mt-1.5 h-2 w-2 shrink-0 rounded-full bg-brand-500" />

        <div class="min-w-0 flex-1">
          <p class="text-sm text-slate-700">
            <span v-if="activity.actor" class="font-medium">
              {{ activity.actor.display_name?.trim() || activity.actor.username }}
            </span>
            {{ getActionLabel(activity.action) }}
            <span class="font-medium">{{ activity.resource_type }}</span>
            <span v-if="activity.message" class="text-slate-500"> — {{ activity.message }}</span>
          </p>
          <time class="text-xs text-slate-400">{{ relativeTime(activity.created_at) }}</time>
        </div>
      </div>

      <!-- 加载更多 -->
      <div v-if="stats.hasMoreActivities" class="pt-2 text-center">
        <NButton
          quaternary
          size="small"
          :loading="stats.activitiesLoading"
          @click="stats.loadActivities(false)"
        >
          加载更多
        </NButton>
      </div>
    </div>
  </div>
</template>
