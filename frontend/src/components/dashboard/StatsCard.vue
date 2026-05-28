<script setup lang="ts">
defineProps<{
  title: string
  value: number | string
  icon: string
  trend?: 'up' | 'down' | 'neutral'
  trendValue?: string
  loading?: boolean
}>()

const trendColors: Record<string, string> = {
  up: 'text-green-600',
  down: 'text-red-600',
  neutral: 'text-slate-400',
}

const trendIcons: Record<string, string> = {
  up: '↑',
  down: '↓',
  neutral: '→',
}
</script>

<template>
  <div
    class="rounded-xl bg-white p-6 shadow-sm shadow-slate-200/60 transition-shadow hover:shadow-md hover:shadow-slate-200/80"
  >
    <!-- 加载骨架屏 -->
    <template v-if="loading">
      <div class="flex items-center justify-between">
        <div class="h-4 w-20 animate-pulse rounded bg-slate-200" />
        <div class="h-10 w-10 animate-pulse rounded-full bg-slate-200" />
      </div>
      <div class="mt-4 h-8 w-24 animate-pulse rounded bg-slate-200" />
    </template>

    <!-- 正常内容 -->
    <template v-else>
      <div class="flex items-center justify-between">
        <span class="text-sm text-slate-500">{{ title }}</span>
        <div class="flex h-10 w-10 items-center justify-center rounded-full bg-brand-50 text-lg">
          {{ icon }}
        </div>
      </div>

      <div class="mt-4">
        <span class="text-3xl font-bold text-slate-900">
          {{ typeof value === 'number' ? value.toLocaleString() : value }}
        </span>

        <div
          v-if="trend && trendValue"
          class="mt-1 flex items-center gap-1 text-xs"
          :class="trendColors[trend]"
        >
          <span>{{ trendIcons[trend] }}</span>
          <span>{{ trendValue }}</span>
        </div>
      </div>
    </template>
  </div>
</template>
